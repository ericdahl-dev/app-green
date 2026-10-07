// Command app-green is a terminal app that traces Jira tickets through
// GitHub PRs to the AWS pipeline stages running them.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericdahl-dev/app-green/internal/config"
	"github.com/ericdahl-dev/app-green/internal/resolver"
	"github.com/ericdahl-dev/app-green/internal/ui"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `app-green - Jira tickets traced through GitHub PRs to AWS pipeline stages

Usage:
  app-green                  launch the app
  app-green --config <path>  use this config instead of the default
  app-green --version        print the version
  app-green --help           show this help
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("app-green", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {}
	configPath := flags.String("config", "", "config file (default $XDG_CONFIG_HOME/app-green/config.toml)")
	showVersion := flags.Bool("version", false, "print the version")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprint(stdout, usage)
			return 0
		}
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "app-green: unexpected argument %q\n%s", flags.Arg(0), usage)
		return 2
	}
	if *showVersion {
		_, _ = fmt.Fprintf(stdout, "app-green %s\n", version)
		return 0
	}
	log, closeLog, err := startLog()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "app-green: %v\n", err)
		return 1
	}
	defer closeLog()
	path := *configPath
	if path == "" {
		p, err := config.DefaultPath()
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "app-green: %v\n", err)
			return 1
		}
		path = p
	}
	cfg, err := config.Load(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			_, _ = fmt.Fprintf(stderr, "no config at %s; see README\n", path)
			return 1
		}
		_, _ = fmt.Fprintf(stderr, "app-green: %v\n", err)
		return 1
	}
	log.Info("start", "version", version, "config", path)
	for _, w := range cfg.Warnings() {
		log.Warn("config", "warning", w)
	}
	r, err := resolver.FromConfig(context.Background(), cfg, nil, log)
	if err != nil {
		log.Error("resolve tokens", "err", err)
		_, _ = fmt.Fprintf(stderr, "app-green: %v\n", err)
		return 1
	}
	if err := runApp(r, log); err != nil {
		log.Error("ui", "err", err)
		_, _ = fmt.Fprintf(stderr, "app-green: %v\n", err)
		return 1
	}
	return 0
}

// runner is the part of the resolver runApp drives.
type runner interface {
	Run(ctx context.Context, out chan<- resolver.Snapshot, refresh <-chan struct{})
}

// runProgram runs m full screen until the user quits; tests replace it.
var runProgram = func(m tea.Model) error {
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

// stopTimeout is how long runApp waits for the resolver after the UI quits.
var stopTimeout = 2 * time.Second

// runApp runs the resolver in the background and the UI in front of it.
// When the UI quits (q, ctrl+c, or SIGINT/SIGTERM, which Bubble Tea
// catches), it cancels the resolver and waits up to stopTimeout for it to
// stop.
func runApp(r runner, log *slog.Logger) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	snaps := make(chan resolver.Snapshot)
	refresh := make(chan struct{}, 1) // one pending press coalesces the rest
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(snaps)
		r.Run(ctx, snaps, refresh)
	}()
	err := runProgram(ui.NewApp(snaps, refresh, openBrowser))
	cancel()
	select {
	case <-done:
	case <-time.After(stopTimeout):
		log.Warn("resolver did not stop in time", "timeout", stopTimeout)
	}
	if errors.Is(err, tea.ErrInterrupted) {
		return nil // SIGINT from outside; SIGTERM already quits cleanly
	}
	return err
}

// startLog opens the log file and makes it the default slog (and log)
// output too, so nothing a dependency logs can draw over the screen. Its
// close func restores the previous default. Tokens are never logged.
func startLog() (*slog.Logger, func(), error) {
	path, err := logPath()
	if err != nil {
		return nil, nil, err
	}
	f, err := openLog(path)
	if err != nil {
		return nil, nil, err
	}
	log := slog.New(slog.NewTextHandler(f, nil))
	prev := slog.Default()
	slog.SetDefault(log)
	return log, func() {
		slog.SetDefault(prev)
		_ = f.Close()
	}, nil
}

// logPath is $XDG_STATE_HOME/app-green/app-green.log, or
// ~/.local/state/app-green/app-green.log when XDG_STATE_HOME is unset or
// relative (the XDG spec says to ignore a relative value).
func logPath() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find log path: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "app-green", "app-green.log"), nil
}

// openLog opens path for appending, creating it and its directory. The
// directory is app-green's own, so both are made private (0700 and 0600)
// even when they already exist with wider modes.
func openLog(path string) (*os.File, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("open log: %w", err)
	}
	return f, nil
}

// openerCommand is the command that opens u in the default browser on goos.
// u must be an absolute http or https URL with a host, so it can never be
// read as an option: neither opener accepts "--", and macOS open gets the
// URL as the value of -u.
func openerCommand(goos, u string) (string, []string, error) {
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
		return "", nil, fmt.Errorf("not a web page: %q", u)
	}
	switch goos {
	case "darwin":
		return "open", []string{"-u", u}, nil
	case "windows", "plan9", "js", "wasip1":
		return "", nil, fmt.Errorf("opening a browser is not supported on %s", goos)
	default:
		return "xdg-open", []string{u}, nil
	}
}

// openBrowser opens u in the default browser without waiting for it.
func openBrowser(u string) error {
	name, args, err := openerCommand(runtime.GOOS, u)
	if err != nil {
		return err
	}
	return startCommand(name, args...)
}

// startCommand starts name without waiting for it. Its stdin, stdout and
// stderr are the null device, so it cannot draw over the screen, and a
// goroutine reaps it so it does not linger as a zombie. Tests replace it.
var startCommand = func(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

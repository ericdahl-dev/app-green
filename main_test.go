package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericdahl-dev/app-green/internal/resolver"
	"github.com/ericdahl-dev/app-green/internal/ui"
)

// isolate points every XDG directory at a fresh temp dir, so a test never
// reads a real config or writes a real log.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

func TestRunVersion(t *testing.T) {
	isolate(t)
	var out, errOut bytes.Buffer
	if code := run([]string{"--version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	if got := out.String(); got != "app-green dev\n" {
		t.Fatalf("got %q", got)
	}
}

func TestRunHelp(t *testing.T) {
	isolate(t)
	var out, errOut bytes.Buffer
	if code := run([]string{"--help"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"--config", "--version", "--help"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help missing %q: %q", want, out.String())
		}
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr %q", errOut.String())
	}
}

func TestRunBadArgs(t *testing.T) {
	for _, args := range [][]string{{"--nope"}, {"extra"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			isolate(t)
			var out, errOut bytes.Buffer
			if code := run(args, &out, &errOut); code != 2 {
				t.Fatalf("exit %d", code)
			}
			if !strings.Contains(errOut.String(), "Usage:") {
				t.Errorf("stderr %q", errOut.String())
			}
		})
	}
}

func TestRunMissingConfigFlag(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), "nope.toml")
	var out, errOut bytes.Buffer
	if code := run([]string{"--config", path}, &out, &errOut); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if want := "no config at " + path + "; see README\n"; errOut.String() != want {
		t.Fatalf("stderr %q, want %q", errOut.String(), want)
	}
}

func TestRunMissingConfigDefaultPath(t *testing.T) {
	isolate(t)
	want := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "app-green", "config.toml")
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if got := errOut.String(); got != "no config at "+want+"; see README\n" {
		t.Fatalf("stderr %q", got)
	}
}

func TestLogPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fallback := filepath.Join(home, ".local", "state", "app-green", "app-green.log")
	for _, tc := range []struct{ name, xdg, want string }{
		{"xdg set", "/xdg/state", "/xdg/state/app-green/app-green.log"},
		{"unset", "", fallback},
		{"relative ignored", "rel/state", fallback},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", tc.xdg)
			got, err := logPath()
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOpenLogCreatesPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "app-green", "app-green.log")
	f, err := openLog(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	assertMode(t, filepath.Dir(path), 0o700)
	assertMode(t, path, 0o600)
}

func TestOpenLogAppendsAndTightens(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "app-green")
	path := filepath.Join(dir, "app-green.log")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := openLog(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("new\n")
	_ = f.Close()
	got, _ := os.ReadFile(path)
	if string(got) != "old\nnew\n" {
		t.Fatalf("content %q", got)
	}
	assertMode(t, dir, 0o700)
	assertMode(t, path, 0o600)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s mode %o, want %o", path, got, want)
	}
}

func TestOpenerCommand(t *testing.T) {
	const u = "https://example.com/a?b=c"
	for _, tc := range []struct {
		goos, name string
		args       []string
	}{
		{"darwin", "open", []string{"-u", u}},
		{"linux", "xdg-open", []string{u}},
		{"freebsd", "xdg-open", []string{u}},
	} {
		name, args, err := openerCommand(tc.goos, u)
		if err != nil {
			t.Fatalf("%s: %v", tc.goos, err)
		}
		if name != tc.name || !slices.Equal(args, tc.args) {
			t.Errorf("%s: got %s %q", tc.goos, name, args)
		}
	}
}

func TestOpenerCommandRejects(t *testing.T) {
	for _, u := range []string{"-a Calculator", "--help", "file:///etc/passwd", "javascript:alert(1)", "https://", "example.com",
		"https://example.com/x?q=$(id) `id`", "https://example.com/a b", "https://example.com/\"x", "https://example.com/<x>", "https://example.com/a\\b", "https://example.com/a\x7fb"} {
		if _, _, err := openerCommand("darwin", u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
	if _, _, err := openerCommand("windows", "https://example.com"); err == nil {
		t.Error("windows accepted")
	}
}

// tokenFailConfig is a valid config whose env lists no repos (a warning)
// and whose GitHub token variable is unset.
const tokenFailConfig = `
[jira]
  site = "https://example.atlassian.net"
  email = "me@example.com"
  token_env = "APP_GREEN_TEST_JIRA_TOKEN"
  projects = ["ABC"]

[github]
  author = "me"
  token_env = "APP_GREEN_TEST_GITHUB_TOKEN"
  repos = ["acme/app"]

[[aws.accounts]]
  name = "stage-acct"
  profile = "stage"
  region = "us-east-1"

[[envs]]
  account = "stage-acct"
  pipeline = "app-pipeline"
  stage = "Test"
  deploy_action = "Deploy"
`

func TestRunTokenError(t *testing.T) {
	isolate(t)
	const secret = "fake-SECRET-123"
	t.Setenv("APP_GREEN_TEST_JIRA_TOKEN", secret)
	t.Setenv("APP_GREEN_TEST_GITHUB_TOKEN", "")
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(tokenFailConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"--config", path}, &out, &errOut); code != 1 {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	if !strings.HasPrefix(errOut.String(), "app-green: ") || !strings.Contains(errOut.String(), "APP_GREEN_TEST_GITHUB_TOKEN") {
		t.Errorf("stderr %q", errOut.String())
	}
	if strings.Contains(errOut.String(), "lists no repos") {
		t.Errorf("warning on stderr: %q", errOut.String())
	}
	logBytes, err := os.ReadFile(filepath.Join(os.Getenv("XDG_STATE_HOME"), "app-green", "app-green.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logBytes), "lists no repos") {
		t.Errorf("log missing warning: %q", logBytes)
	}
	for name, s := range map[string]string{"stdout": out.String(), "stderr": errOut.String(), "log": string(logBytes)} {
		if strings.Contains(s, secret) {
			t.Errorf("%s leaks the token: %q", name, s)
		}
	}
}

// fakeRunner stands in for the resolver: it runs until its ctx is canceled.
type fakeRunner struct {
	stopped chan struct{}
	refresh <-chan struct{}
}

func (f *fakeRunner) Run(ctx context.Context, _ chan<- resolver.Snapshot, refresh <-chan struct{}) {
	f.refresh = refresh
	<-ctx.Done()
	close(f.stopped)
}

// fakeProgram makes runProgram return err at once, recording the model.
func fakeProgram(t *testing.T, err error) *tea.Model {
	t.Helper()
	var got tea.Model
	old := runProgram
	runProgram = func(m tea.Model) error { got = m; return err }
	t.Cleanup(func() { runProgram = old })
	return &got
}

func TestRunAppStopsResolverOnQuit(t *testing.T) {
	model := fakeProgram(t, nil)
	r := &fakeRunner{stopped: make(chan struct{})}
	if err := runApp(r, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.stopped:
	default:
		t.Fatal("resolver still running after runApp returned")
	}
	if _, ok := (*model).(ui.App); !ok {
		t.Fatalf("program model %T", *model)
	}
	if cap(r.refresh) != 1 {
		t.Errorf("refresh cap %d, want 1", cap(r.refresh))
	}
}

func TestRunAppProgramErrors(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		name     string
		err, out error
	}{
		{"interrupt is a clean exit", tea.ErrInterrupted, nil},
		{"other error returned", boom, boom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeProgram(t, tc.err)
			r := &fakeRunner{stopped: make(chan struct{})}
			if err := runApp(r, slog.New(slog.DiscardHandler)); !errors.Is(err, tc.out) || (tc.out == nil && err != nil) {
				t.Fatalf("got %v, want %v", err, tc.out)
			}
			<-r.stopped
		})
	}
}

// stuckRunner ignores cancellation until release is closed.
type stuckRunner struct{ release chan struct{} }

func (s stuckRunner) Run(context.Context, chan<- resolver.Snapshot, <-chan struct{}) { <-s.release }

func TestRunAppDoesNotHangOnStuckResolver(t *testing.T) {
	fakeProgram(t, nil)
	old := stopTimeout
	stopTimeout = 10 * time.Millisecond
	t.Cleanup(func() { stopTimeout = old })
	r := stuckRunner{release: make(chan struct{})}
	defer close(r.release)
	finished := make(chan error, 1)
	go func() { finished <- runApp(r, slog.New(slog.DiscardHandler)) }()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runApp hung waiting for the resolver")
	}
}

func TestOpenBrowser(t *testing.T) {
	var got []string
	old := startCommand
	startCommand = func(name string, args ...string) error { got = append([]string{name}, args...); return nil }
	t.Cleanup(func() { startCommand = old })

	if err := openBrowser("-a Calculator"); err == nil || got != nil {
		t.Fatalf("err %v, started %q", err, got)
	}
	const u = "https://example.com/x"
	if err := openBrowser(u); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("no opener on windows")
		}
		t.Fatal(err)
	}
	name, args, _ := openerCommand(runtime.GOOS, u)
	if want := append([]string{name}, args...); !slices.Equal(got, want) {
		t.Fatalf("started %q, want %q", got, want)
	}
}

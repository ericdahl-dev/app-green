package ui_test

import (
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/ericdahl-dev/app-green/internal/resolver"
	"github.com/ericdahl-dev/app-green/internal/ui"
)

func ok(name string, ago time.Duration) resolver.AdapterStatus {
	return resolver.AdapterStatus{Name: name, OK: true, At: now.Add(-ago)}
}

func TestHeaderAllOK(t *testing.T) {
	st := []resolver.AdapterStatus{ok("jira", 12*time.Second), ok("github", 8*time.Second), ok("aws stage-acct", 3*time.Minute)}
	want := "app-green  jira ✓ 12s  github ✓ 8s  aws stage-acct ✓ 3m"
	if got := ui.RenderHeader(st, now, 100); got != want {
		t.Errorf("header\n got %q\nwant %q", got, want)
	}
}

func TestHeaderSegments(t *testing.T) {
	tests := []struct {
		name string
		st   resolver.AdapterStatus
		want string
	}{
		{"ok with no time", resolver.AdapterStatus{Name: "jira", OK: true}, "jira ✓"},
		{"token rejected", resolver.AdapterStatus{Name: "github", Err: "token rejected", Auth: true, At: now.Add(-time.Hour)}, "github ✗ token rejected"},
		{"sso expired", resolver.AdapterStatus{Name: "aws prod-acct", Err: "operation error STS: GetCallerIdentity, token has expired", SSO: true}, "aws prod-acct ✗ sso expired"},
		{"throttled", resolver.AdapterStatus{Name: "aws stage-acct", Err: "ThrottlingException: Rate exceeded", Throttled: true, RetryAt: now.Add(2*time.Minute + 5*time.Second)}, "aws stage-acct ⏸ throttled 2m"},
		{"throttled, retry due", resolver.AdapterStatus{Name: "jira", Err: "HTTP 429", Throttled: true, RetryAt: now.Add(-time.Second)}, "jira ⏸ throttled"},
		{"auth beats throttled", resolver.AdapterStatus{Name: "jira", Err: "token rejected", Auth: true, Throttled: true, RetryAt: now.Add(time.Minute)}, "jira ✗ token rejected"},
		{"sso beats throttled", resolver.AdapterStatus{Name: "aws prod-acct", Err: "expired", SSO: true, Throttled: true, RetryAt: now.Add(time.Minute)}, "aws prod-acct ✗ sso expired"},
		{"generic error", resolver.AdapterStatus{Name: "jira", Err: "HTTP 502 Bad Gateway"}, "jira ✗ HTTP 502 Bad Gateway"},
		{"long error", resolver.AdapterStatus{Name: "jira", Err: "Get \"https://example.atlassian.net/rest/api/3/search\": dial tcp: lookup example.atlassian.net: no such host"}, "jira ✗ Get \"https://example.atlassia…"},
		{"multi-line error", resolver.AdapterStatus{Name: "github", Err: "HTTP 500\nupstream body"}, "github ✗ HTTP 500"},
		{"not loaded yet", resolver.AdapterStatus{Name: "aws stage-acct"}, "aws stage-acct …"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := "app-green  " + tt.want
			if got := ui.RenderHeader([]resolver.AdapterStatus{tt.st}, now, 100); got != want {
				t.Errorf("header\n got %q\nwant %q", got, want)
			}
		})
	}
}

func TestHeaderMixed(t *testing.T) {
	st := []resolver.AdapterStatus{
		ok("jira", 12*time.Second),
		{Name: "github", Err: "token rejected", Auth: true},
		{Name: "aws stage-acct", Throttled: true, RetryAt: now.Add(2 * time.Minute)},
		{Name: "aws prod-acct", SSO: true, Err: "expired"},
	}
	want := "app-green  jira ✓ 12s  github ✗ token rejected  aws stage-acct ⏸ throttled 2m  aws prod-acct ✗ sso expired"
	if got := ui.RenderHeader(st, now, 120); got != want {
		t.Errorf("header\n got %q\nwant %q", got, want)
	}
}

func TestHeaderNarrow(t *testing.T) {
	st := []resolver.AdapterStatus{ok("jira", 12*time.Second), ok("github", 8*time.Second), {Name: "aws prod-acct", SSO: true}}
	full := "app-green  jira ✓ 12s  github ✓ 8s  aws prod-acct ✗ sso expired"
	if got := ui.RenderHeader(st, now, 63); got != full {
		t.Errorf("fits exactly:\n got %q\nwant %q", got, full)
	}
	// One cell short: OK sources drop their ages before anything is cut.
	want := "app-green  jira ✓  github ✓  aws prod-acct ✗ sso expired"
	if got := ui.RenderHeader(st, now, 62); got != want {
		t.Errorf("compact:\n got %q\nwant %q", got, want)
	}
	// Still too wide: cut at the right edge.
	want = "app-green  jira ✓  github ✓  aws prod-a…"
	if got := ui.RenderHeader(st, now, 40); got != want {
		t.Errorf("cut:\n got %q\nwant %q", got, want)
	}
	for w := 0; w <= 120; w++ {
		if got := ui.RenderHeader(st, now, w); ansi.StringWidth(got) > w {
			t.Fatalf("width %d: header is %d cells: %q", w, ansi.StringWidth(got), got)
		}
	}
}

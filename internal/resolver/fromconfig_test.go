package resolver_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericdahl-dev/app-green/internal/resolver"
)

// fakeAWSConfig points the SDK at a shared config with the test profiles,
// so building clients reads no real AWS settings (and calls nothing).
func fakeAWSConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, []byte("[profile stage]\nregion = us-east-1\n[profile prod]\nregion = us-east-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
}

func TestFromConfigBuildsAdapters(t *testing.T) {
	fakeAWSConfig(t)
	t.Setenv("APP_GREEN_TEST_JIRA_TOKEN", "jira-tok")
	t.Setenv("APP_GREEN_TEST_GITHUB_TOKEN", "gh-tok")

	r, err := resolver.FromConfig(context.Background(), loadConfig(t, testEnv, prodEnv), nil, nil)
	if err != nil || r == nil {
		t.Fatalf("FromConfig = %v, %v; want a resolver", r, err)
	}
}

func TestFromConfigReportsEveryTokenError(t *testing.T) {
	fakeAWSConfig(t)
	t.Setenv("APP_GREEN_TEST_JIRA_TOKEN", "")
	t.Setenv("APP_GREEN_TEST_GITHUB_TOKEN", "")

	_, err := resolver.FromConfig(context.Background(), loadConfig(t, testEnv), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "jira.token_env") || !strings.Contains(err.Error(), "github.token_env") {
		t.Errorf("err = %v, want both token errors", err)
	}
}

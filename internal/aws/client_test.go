package aws

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/smithy-go"
)

func TestIsSSOExpired(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"typed", &ssocreds.InvalidTokenError{}, true},
		{"wrapped typed", &smithy.OperationError{ServiceID: "CodePipeline", OperationName: "GetPipeline", Err: fmt.Errorf("get identity: %w", &ssocreds.InvalidTokenError{Err: errors.New("x")})}, true},
		{"session message", errors.New("operation error: failed to refresh cached credentials, the SSO session has expired or is invalid"), true},
		{"token expired message", errors.New("refresh cached SSO token failed, unable to refresh SSO token, token has expired"), true},
		{"cached token", errors.New("cached SSO token is expired, or not present, and cannot be refreshed"), true},
		// sso-session profiles: the token provider's errors arrive unwrapped.
		{"refresh rejected", fmt.Errorf("get identity: %w", fmt.Errorf("refresh cached SSO token failed, %w", fmt.Errorf("unable to refresh SSO token, %w", &smithy.GenericAPIError{Code: "InvalidGrantException"}))), true},
		{"never logged in", errors.New("failed to read cached SSO token file, open /home/u/.aws/sso/cache/x.json: no such file or directory"), true},
		{"access denied", &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized"}, false},
		{"other", errors.New("pipeline not found"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSSOExpired(tc.err); got != tc.want {
				t.Errorf("IsSSOExpired(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestSSOExpiredThroughAdapterErrors(t *testing.T) {
	expired := &smithy.OperationError{ServiceID: "CodePipeline", Err: &ssocreds.InvalidTokenError{}}
	c := &Client{cp: &fakePipeline{err: expired}, ecs: &fakeECS{err: expired}}
	ctx := context.Background()
	if _, err := c.Sources(ctx, "app-pipeline"); !IsSSOExpired(err) {
		t.Errorf("Sources err = %v, want SSO expired", err)
	}
	if _, _, err := c.History(ctx, "app-pipeline", testSources, testSpec); !IsSSOExpired(err) {
		t.Errorf("History err = %v, want SSO expired", err)
	}
	if _, _, err := c.Health(ctx, []Service{{Cluster: "c1", Name: "s1"}}); !IsSSOExpired(err) {
		t.Errorf("Health err = %v, want SSO expired", err)
	}
	if err := c.Approve(ctx, "app-pipeline", "Test", "a", "tok", true, ""); !IsSSOExpired(err) {
		t.Errorf("Approve err = %v, want SSO expired", err)
	}
}

func TestIsThrottledAndIsAccessDenied(t *testing.T) {
	op := func(code string) error {
		return &smithy.OperationError{ServiceID: "CodePipeline", Err: &smithy.GenericAPIError{Code: code, Message: "m"}}
	}
	cases := []struct {
		err               error
		throttled, denied bool
	}{
		{op("ThrottlingException"), true, false},
		{op("TooManyRequestsException"), true, false},
		{op("RequestLimitExceeded"), true, false},
		{op("Throttling"), true, false},
		{op("AccessDeniedException"), false, true},
		{op("AccessDenied"), false, true},
		{op("PipelineNotFoundException"), false, false},
		{errors.New("ThrottlingException"), false, false},
		{nil, false, false},
	}
	for _, tc := range cases {
		if got := IsThrottled(tc.err); got != tc.throttled {
			t.Errorf("IsThrottled(%v) = %v", tc.err, got)
		}
		if got := IsAccessDenied(tc.err); got != tc.denied {
			t.Errorf("IsAccessDenied(%v) = %v", tc.err, got)
		}
	}
}

func TestThrottledAndDeniedThroughAdapterErrors(t *testing.T) {
	for _, code := range []string{"ThrottlingException", "AccessDeniedException"} {
		e := &smithy.OperationError{ServiceID: "CodePipeline", Err: &smithy.GenericAPIError{Code: code}}
		check := IsThrottled
		if code == "AccessDeniedException" {
			check = IsAccessDenied
		}
		c := &Client{cp: &fakePipeline{err: e}, ecs: &fakeECS{err: e}}
		ctx := context.Background()
		if _, err := c.Sources(ctx, "app-pipeline"); !check(err) {
			t.Errorf("%s: Sources err = %v", code, err)
		}
		if _, _, err := c.History(ctx, "app-pipeline", testSources, testSpec); !check(err) {
			t.Errorf("%s: History err = %v", code, err)
		}
		if _, _, err := c.Health(ctx, []Service{{Cluster: "c1", Name: "s1"}}); !check(err) {
			t.Errorf("%s: Health err = %v", code, err)
		}
		if err := c.Approve(ctx, "app-pipeline", "Test", "a", "tok", true, ""); !check(err) {
			t.Errorf("%s: Approve err = %v", code, err)
		}
	}
}

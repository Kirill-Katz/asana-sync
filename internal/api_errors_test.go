package internal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClientErrorsAreClearAndNotRetried(t *testing.T) {
	for _, resource := range []string{"projects", "users"} {
		for _, tc := range []struct {
			status  int
			message string
		}{
			{http.StatusBadRequest, "bad request; check WORKSPACE_ID and request parameters"},
			{http.StatusUnauthorized, "authentication failed; check that API_TOKEN is valid and has not expired"},
			{http.StatusForbidden, "access denied; check token scopes and workspace permissions"},
			{http.StatusNotFound, "resource not found; check API_HOST, WORKSPACE_ID, and the requested resource"},
		} {
			t.Run(fmt.Sprintf("%s/%d", resource, tc.status), func(t *testing.T) {
				attempts := 0
				client, err := NewAsanaAPIClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					attempts++
					return &http.Response{
						StatusCode: tc.status,
						Header:     http.Header{"Retry-After": {"0"}},
						Body:       io.NopCloser(strings.NewReader(`{"errors":[]}`)),
					}, nil
				})}, testAPIConfig(), RetryConfig{MaxAttempts: 5, MaxBackoff: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				client.waitRetry = func(context.Context, time.Duration) error {
					t.Fatal("permanent HTTP error triggered a retry wait")
					return nil
				}
				if resource == "projects" {
					_, err = client.GetProjects(context.Background())
				} else {
					_, err = client.GetUsers(context.Background())
				}
				want := fmt.Sprintf("fetch %s: HTTP status %d (%s)", resource, tc.status, tc.message)
				if err == nil || err.Error() != want {
					t.Fatalf("got %v, want %q", err, want)
				}
				var transient *retryableError
				if errors.As(err, &transient) {
					t.Fatal("permanent HTTP error classified as retryable")
				}
				if attempts != 1 {
					t.Fatalf("made %d attempts, want 1", attempts)
				}
			})
		}
	}
}

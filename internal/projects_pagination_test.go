package internal

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGetProjectsPagination(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lastBody string
		status   int
		wantErr  bool
	}{
		{"success after retry", `{"data":[{"gid":"2","name":"Second"}],"next_page":null}`, 200, false},
		{"later page fails", `{}`, 403, true},
		{"later page malformed", `{`, 200, true},
		{"repeated offset", `{"data":[],"next_page":{"offset":"next +/&"}}`, 200, true},
		{"empty offset", `{"data":[],"next_page":{"offset":""}}`, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			client, err := NewAsanaAPIClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				if req.URL.Query().Get("limit") != strconv.Itoa(projectsPageLimit) || req.URL.Query().Get("workspace") != testWorkspaceID {
					t.Error("missing page size")
				}
				body := tc.lastBody
				status := tc.status
				if attempts == 1 {
					if req.URL.Query().Get("offset") != "" {
						t.Error("first page has an offset")
					}
					body = `{"data":[{"gid":"1","name":"First"}],"next_page":{"offset":"next +/&","uri":"https://untrusted.invalid"}}`
					status = 200
				} else {
					if req.URL.Query().Get("offset") != "next +/&" || req.URL.Host != "configured.example.test" {
						t.Error("unexpected pagination request")
					}
					if attempts == 2 {
						status = 503
					}
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}, testAPIConfig(), RetryConfig{MaxAttempts: 2, MaxBackoff: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			var delays []time.Duration
			recordRetryWaits(client, &delays)
			got, err := client.GetProjects(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				if got != nil {
					t.Fatal("returned partial results")
				}
			} else if want := []Project{{GID: "1", Name: "First"}, {GID: "2", Name: "Second"}}; !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			if attempts != 3 || !reflect.DeepEqual(delays, []time.Duration{time.Second}) {
				t.Fatalf("attempts=%d delays=%v", attempts, delays)
			}
		})
	}
}

func TestRetryAttemptsUseLimiterAndRespectCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	client, err := NewAsanaAPIClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, errors.New("connection failed")
	})}, testAPIConfig(), RetryConfig{MaxAttempts: 5, MaxBackoff: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	waits := 0
	client.waitRetry = func(ctx context.Context, _ time.Duration) error {
		waits++
		if waits == 2 {
			client.limiter.mu.Lock()
			tokens := client.limiter.tokens
			client.limiter.mu.Unlock()
			if tokens != 0 {
				t.Errorf("retry attempts left %d tokens, want 0", tokens)
			}
			cancel()
		}
		return ctx.Err()
	}
	if _, err := client.GetProjects(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts=%d, want 2", attempts)
	}
}

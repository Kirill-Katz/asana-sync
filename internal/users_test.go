package internal

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func newUsersTestClient(t *testing.T, handler http.HandlerFunc, retry RetryConfig) *AsanaAPIClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	httpClient := server.Client()
	transport := httpClient.Transport
	httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "configured.example.test" || req.URL.Scheme != "https" {
			t.Error("unexpected API origin")
		}
		req = req.Clone(req.Context())
		req.URL.Scheme = serverURL.Scheme
		req.URL.Host = serverURL.Host
		return transport.RoundTrip(req)
	})
	client, err := NewAsanaAPIClient(httpClient, testAPIConfig(), retry)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestGetUsersPaginationAndRetry(t *testing.T) {
	var attempts atomic.Int32
	client := newUsersTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/1.0/users" || r.URL.Query().Get("limit") != strconv.Itoa(usersPageLimit) || r.URL.Query().Get("workspace") != testWorkspaceID {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Accept") != "application/json" {
			t.Error("missing API headers")
		}
		if attempt == 1 {
			if r.URL.Query().Get("offset") != "" {
				t.Error("unexpected initial offset")
			}
			_, _ = io.WriteString(w, `{"data":[{"gid":"1","name":"First"}],"next_page":{"offset":"next +/&"}}`)
			return
		}
		if r.URL.Query().Get("offset") != "next +/&" {
			t.Error("offset not preserved across pages and retries")
		}
		if attempt == 2 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"gid":"2","name":"Second"}],"next_page":null}`)
	}, RetryConfig{MaxAttempts: 2, MaxBackoff: time.Second, MaxJitter: time.Second})

	var delays []time.Duration
	recordRetryWaits(client, &delays)
	got, err := client.GetUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []User{{GID: "1", Name: "First"}, {GID: "2", Name: "Second"}}
	if !reflect.DeepEqual(got, want) || attempts.Load() != 3 || !reflect.DeepEqual(delays, []time.Duration{3 * time.Second}) {
		t.Fatalf("got=%v attempts=%d delays=%v", got, attempts.Load(), delays)
	}
}

func TestGetUsersFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		attempts int
	}{
		{"unauthorized", 401, `{}`, 1},
		{"invalid JSON", 200, `{`, 1},
		{"server failure", 503, `{}`, 2},
		{"later page fails", 403, `{}`, 2},
		{"repeated offset", 200, `{"data":[],"next_page":{"offset":"next"}}`, 2},
		{"empty offset", 200, `{"data":[],"next_page":{"offset":""}}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			client := newUsersTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				attempt := attempts.Add(1)
				if tc.name == "later page fails" && attempt == 1 {
					_, _ = io.WriteString(w, `{"data":[{"gid":"1","name":"First"}],"next_page":{"offset":"next"}}`)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}, RetryConfig{MaxAttempts: 2, MaxBackoff: time.Second})
			var delays []time.Duration
			recordRetryWaits(client, &delays)
			got, err := client.GetUsers(context.Background())
			if err == nil || got != nil || int(attempts.Load()) != tc.attempts {
				t.Fatalf("got=%v err=%v attempts=%d, want no partial results and %d attempts", got, err, attempts.Load(), tc.attempts)
			}
		})
	}
}

func TestProjectsAndUsersShareLimiter(t *testing.T) {
	var requests atomic.Int32
	client := newUsersTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"data":[],"next_page":null}`)
	}, testRetryConfig())
	if _, err := client.GetProjects(context.Background()); err != nil {
		t.Fatal(err)
	}
	users, err := client.GetUsers(context.Background())
	if err != nil || len(users) != 0 {
		t.Fatalf("users=%v err=%v", users, err)
	}
	client.limiter.mu.Lock()
	tokens := client.limiter.tokens
	client.limiter.mu.Unlock()
	if tokens != 0 || requests.Load() != 2 {
		t.Fatalf("tokens=%d requests=%d, want 0 tokens and 2 requests", tokens, requests.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.GetUsers(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
	if requests.Load() != 2 {
		t.Fatal("canceled call reached server")
	}
}

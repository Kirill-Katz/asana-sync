package internal

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

const partialPageJSON = `{"data":[{"gid":"partial","name":"Incomplete attempt"},`
const completePageJSON = `{"data":[{"gid":"complete","name":"Complete attempt"}],"next_page":null}`

type trackedResponseBody struct {
	io.Reader
	closed bool
}

func (b *trackedResponseBody) Close() error {
	b.closed = true
	return nil
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

type contextReader struct{ ctx context.Context }

func (r contextReader) Read([]byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func fetchResponseTestIDs(ctx context.Context, client *AsanaAPIClient, resource string) ([]string, error) {
	var ids []string
	if resource == "projects" {
		projects, err := client.GetProjects(ctx)
		for _, project := range projects {
			ids = append(ids, project.GID)
		}
		return ids, err
	}
	users, err := client.GetUsers(ctx)
	for _, user := range users {
		ids = append(ids, user.GID)
	}
	return ids, err
}

func TestBodyReadFailuresRetry(t *testing.T) {
	for _, resource := range []string{"projects", "users"} {
		for _, failure := range []struct {
			name string
			err  error
		}{
			{"timeout", os.ErrDeadlineExceeded},
			{"connection reset", syscall.ECONNRESET},
			{"truncated HTTP body", io.ErrUnexpectedEOF},
		} {
			for _, recover := range []bool{true, false} {
				name := resource + "/" + failure.name + "/exhaustion"
				if recover {
					name = resource + "/" + failure.name + "/recovery"
				}
				t.Run(name, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						var bodies []*trackedResponseBody
						var urls []string
						client, err := NewAsanaAPIClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
							urls = append(urls, req.URL.String())
							var reader io.Reader = io.MultiReader(strings.NewReader(partialPageJSON), errorReader{failure.err})
							if recover && len(urls) == 2 {
								reader = strings.NewReader(completePageJSON)
							}
							body := &trackedResponseBody{Reader: reader}
							bodies = append(bodies, body)
							return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
						})}, testAPIConfig(), RetryConfig{MaxAttempts: 2, MaxBackoff: time.Second})
						if err != nil {
							t.Fatal(err)
						}
						var delays []time.Duration
						client.waitRetry = func(ctx context.Context, delay time.Duration) error {
							if !bodies[len(bodies)-1].closed {
								t.Error("failed body was not closed before the retry wait")
							}
							delays = append(delays, delay)
							return ctx.Err()
						}
						ids, err := fetchResponseTestIDs(context.Background(), client, resource)
						if recover {
							if err != nil || !reflect.DeepEqual(ids, []string{"complete"}) {
								t.Fatalf("ids=%v err=%v, want only the complete retried page", ids, err)
							}
						} else if !errors.Is(err, failure.err) || ids != nil {
							t.Fatalf("ids=%v err=%v, want wrapped read error and no partial results", ids, err)
						}
						if len(urls) != 2 || urls[0] != urls[1] || !reflect.DeepEqual(delays, []time.Duration{time.Second}) {
							t.Fatalf("requests=%v delays=%v, want two identical requests and one backoff", urls, delays)
						}
						for _, body := range bodies {
							if !body.closed {
								t.Error("response body was not closed")
							}
						}
						client.limiter.mu.Lock()
						tokens := client.limiter.tokens
						client.limiter.mu.Unlock()
						if tokens != 0 {
							t.Fatalf("remaining tokens=%d, want both attempts to consume the common limiter", tokens)
						}
					})
				})
			}
		}
	}
}

func TestHTTPClientBodyTimeoutRetries(t *testing.T) {
	for _, resource := range []string{"projects", "users"} {
		t.Run(resource, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var bodies []*trackedResponseBody
				const timeout = 50 * time.Millisecond
				client, err := NewAsanaAPIClient(&http.Client{
					Timeout: timeout,
					Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						var reader io.Reader = strings.NewReader(completePageJSON)
						if len(bodies) == 0 {
							reader = io.MultiReader(strings.NewReader(partialPageJSON), contextReader{req.Context()})
						} else if !bodies[0].closed {
							t.Error("timed-out body was not closed before retry")
						}
						body := &trackedResponseBody{Reader: reader}
						bodies = append(bodies, body)
						return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
					}),
				}, testAPIConfig(), RetryConfig{MaxAttempts: 2, MaxBackoff: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				ctx := context.Background()
				start := time.Now()
				ids, err := fetchResponseTestIDs(ctx, client, resource)
				if err != nil || ctx.Err() != nil || !reflect.DeepEqual(ids, []string{"complete"}) {
					t.Fatalf("ids=%v err=%v ctx.Err=%v, want successful recovery with active caller context", ids, err, ctx.Err())
				}
				if len(bodies) != 2 || time.Since(start) != timeout+time.Second {
					t.Fatalf("attempts=%d elapsed=%v, want two attempts with client timeout and backoff", len(bodies), time.Since(start))
				}
				for _, body := range bodies {
					if !body.closed {
						t.Error("response body was not closed")
					}
				}
			})
		})
	}
}

func TestMalformedJSONDoesNotRetry(t *testing.T) {
	for _, resource := range []string{"projects", "users"} {
		for _, bodyJSON := range []string{`{"data":`, `{"data":!}`, `{"data":[{"gid":123}]}`} {
			t.Run(resource+"/"+bodyJSON, func(t *testing.T) {
				attempts := 0
				body := &trackedResponseBody{Reader: strings.NewReader(bodyJSON)}
				client, err := NewAsanaAPIClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					attempts++
					return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
				})}, testAPIConfig(), RetryConfig{MaxAttempts: 2, MaxBackoff: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				client.waitRetry = func(context.Context, time.Duration) error {
					t.Fatal("malformed JSON triggered a retry")
					return nil
				}
				ids, err := fetchResponseTestIDs(context.Background(), client, resource)
				var transient *retryableError
				if err == nil || errors.As(err, &transient) || ids != nil || attempts != 1 || !body.closed {
					t.Fatalf("ids=%v err=%v attempts=%d closed=%v", ids, err, attempts, body.closed)
				}
			})
		}
	}
}

func TestBodyReadCancellationDoesNotRetry(t *testing.T) {
	for _, resource := range []string{"projects", "users"} {
		for _, deadline := range []bool{false, true} {
			t.Run(resource+"/"+map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					if deadline {
						cancel()
						ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
					}
					defer cancel()
					attempts := 0
					var body *trackedResponseBody
					client, err := NewAsanaAPIClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						attempts++
						body = &trackedResponseBody{Reader: io.MultiReader(strings.NewReader(partialPageJSON), readFunc(func([]byte) (int, error) {
							if !deadline {
								cancel()
							}
							<-req.Context().Done()
							// The caller's context error must take precedence over this I/O error.
							return 0, io.ErrUnexpectedEOF
						}))}
						return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
					})}, testAPIConfig(), RetryConfig{MaxAttempts: 2, MaxBackoff: time.Second})
					if err != nil {
						t.Fatal(err)
					}
					client.waitRetry = func(context.Context, time.Duration) error {
						t.Fatal("caller cancellation triggered a retry")
						return nil
					}
					ids, err := fetchResponseTestIDs(ctx, client, resource)
					if !errors.Is(err, ctx.Err()) || ctx.Err() == nil || ids != nil || attempts != 1 || !body.closed {
						t.Fatalf("ids=%v err=%v context=%v attempts=%d closed=%v", ids, err, ctx.Err(), attempts, body.closed)
					}
				})
			})
		}
	}
}

type readFunc func([]byte) (int, error)

func (f readFunc) Read(p []byte) (int, error) { return f(p) }

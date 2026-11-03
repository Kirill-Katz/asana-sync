package internal

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRetryConfigValidation(t *testing.T) {
	for _, config := range []RetryConfig{
		{},
		{MaxAttempts: -1, MaxBackoff: time.Second},
		{MaxAttempts: 1, MaxBackoff: time.Millisecond},
		{MaxAttempts: 1, MaxBackoff: time.Second, MaxJitter: -1},
		{MaxAttempts: 1, MaxBackoff: math.MaxInt64, MaxJitter: 1},
	} {
		if _, err := NewAsanaAPIClient(http.DefaultClient, testAPIConfig(), config); err == nil {
			t.Fatalf("accepted invalid config: %+v", config)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"0", 0},
		{"12", 12 * time.Second},
		{" 2 ", 2 * time.Second},
		{"", -1},
		{"-1", -1},
		{"1.5", -1},
		{"Wed, 21 Oct 2015 07:28:00 GMT", -1},
		{"9223372036854775807", -1},
	} {
		if got := parseRetryAfter(tc.value); got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestRetryDelayJitterBounds(t *testing.T) {
	for _, jitter := range []time.Duration{0, 1, time.Second} {
		for range 100 {
			got := retryDelay(2*time.Second, jitter)
			if got < 2*time.Second || got > 2*time.Second+jitter {
				t.Fatalf("delay %v outside configured bounds", got)
			}
		}
	}
	if got := nextBackoff(time.Duration(math.MaxInt64/2+1), math.MaxInt64); got != math.MaxInt64 {
		t.Fatalf("overflow-safe backoff got %v", got)
	}
}

// recordRetryWaits advances the limiter to a refill boundary without sleeping.
func recordRetryWaits(client *AsanaAPIClient, delays *[]time.Duration) {
	client.waitRetry = func(ctx context.Context, delay time.Duration) error {
		*delays = append(*delays, delay)
		client.limiter.mu.Lock()
		client.limiter.nextRefill = time.Now()
		client.limiter.mu.Unlock()
		return ctx.Err()
	}
}

func TestProjectsRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		retryAfter string
		transport  error
		jitter     time.Duration
		wantDelay  []time.Duration
	}{
		{name: "429 Retry-After", status: 429, retryAfter: "8", jitter: time.Second, wantDelay: []time.Duration{8 * time.Second}},
		{name: "zero Retry-After", status: 429, retryAfter: "0", jitter: time.Second, wantDelay: []time.Duration{0}},
		{name: "503 fallback", status: 503, wantDelay: []time.Duration{time.Second}},
		{name: "invalid header fallback", status: 503, retryAfter: "invalid", wantDelay: []time.Duration{time.Second}},
		{name: "transport", transport: errors.New("connection failed"), wantDelay: []time.Duration{time.Second}},
		{name: "transport timeout", transport: context.DeadlineExceeded, wantDelay: []time.Duration{time.Second}},
		{name: "401 permanent", status: 401},
		{name: "400 permanent", status: 400},
		{name: "cancellation permanent", transport: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			client, err := NewAsanaAPIClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				if req.URL.Query().Get("limit") != "50" || req.URL.Query().Get("offset") != "a +/&" {
					t.Error("page parameters not preserved")
				}
				if tc.transport != nil {
					return nil, tc.transport
				}
				return &http.Response{
					StatusCode: tc.status,
					Header:     http.Header{"Retry-After": {tc.retryAfter}},
					Body:       io.NopCloser(strings.NewReader(`{}`)),
				}, nil
			})}, testAPIConfig(), RetryConfig{MaxAttempts: 2, MaxBackoff: time.Second, MaxJitter: tc.jitter})
			if err != nil {
				t.Fatal(err)
			}
			var delays []time.Duration
			recordRetryWaits(client, &delays)
			if _, err := client.fetchProjectsPageWithRetries(context.Background(), 50, "a +/&"); err == nil {
				t.Fatal("expected failure after attempts exhausted")
			}
			if !reflect.DeepEqual(delays, tc.wantDelay) || attempts != len(tc.wantDelay)+1 {
				t.Fatalf("attempts=%d delays=%v; want %v", attempts, delays, tc.wantDelay)
			}
		})
	}
}

func TestProjectsExponentialBackoff(t *testing.T) {
	attempts := 0
	client, err := NewAsanaAPIClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, errors.New("connection failed")
	})}, testAPIConfig(), RetryConfig{MaxAttempts: 7, MaxBackoff: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var delays []time.Duration
	recordRetryWaits(client, &delays)
	if _, err := client.fetchProjectsPageWithRetries(context.Background(), 100, ""); err == nil {
		t.Fatal("expected failure")
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second, 5 * time.Second}
	if attempts != 7 || !reflect.DeepEqual(delays, want) {
		t.Fatalf("attempts=%d delays=%v, want %v", attempts, delays, want)
	}
}

func TestWaitForRetryCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- waitForRetry(ctx, time.Hour) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("retry wait ignored cancellation")
	}
}

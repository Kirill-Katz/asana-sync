package internal

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewAsanaAPIClientRejectsNilHTTPClient(t *testing.T) {
	if _, err := NewAsanaAPIClient(nil, testAPIConfig(), testRetryConfig()); err == nil {
		t.Fatal("expected an error for a nil HTTP client")
	}
}

func TestRequestLimiterRefill(t *testing.T) {
	now := time.Now()
	limiter := requestLimiter{tokens: 2, nextRefill: now.Add(2 * time.Second)}

	for _, step := range []struct {
		at   time.Duration
		want time.Duration
	}{
		{0, 0},
		{0, 0},
		{0, 2 * time.Second},
		{time.Second, time.Second}, // No individual token is added halfway through.
		{2 * time.Second, 0},
		{2 * time.Second, 0},
		{2 * time.Second, 2 * time.Second},
		{11 * time.Second, 0}, // Idle time cannot accumulate more than two tokens.
		{11 * time.Second, 0},
		{11 * time.Second, time.Second},
	} {
		if got := limiter.take(now.Add(step.at)); got != step.want {
			t.Fatalf("take at %v: got delay %v, want %v", step.at, got, step.want)
		}
	}
}

func TestRequestLimiterConcurrent(t *testing.T) {
	now := time.Now()
	limiter := requestLimiter{tokens: 2, nextRefill: now.Add(2 * time.Second)}
	var acquired atomic.Int32
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			if limiter.take(now) == 0 {
				acquired.Add(1)
			}
		})
	}
	workers.Wait()
	if got := acquired.Load(); got != 2 {
		t.Fatalf("acquired %d tokens, want 2", got)
	}
}

func TestRequestLimiterWaitCancellation(t *testing.T) {
	limiter := requestLimiter{nextRefill: time.Now().Add(time.Hour)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- limiter.wait(ctx)
	}()
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("limiter did not respect cancellation")
	}
}

func TestAsanaAPIClientDo(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing expected bearer authorization")
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Error("missing expected JSON accept header")
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "response")
	}))
	defer server.Close()

	client, err := NewAsanaAPIClient(server.Client(), testAPIConfig(), testRetryConfig())
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusAccepted || string(body) != "response" {
			t.Fatalf("unexpected response: status=%d body=%q", resp.StatusCode, body)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("sent %d requests, want 2", got)
	}
}

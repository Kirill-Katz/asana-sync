package internal

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	requestBucketCapacity = 2
	requestRefillInterval = 2 * time.Second
)

// APIConfig identifies the Asana API endpoint, credentials, and workspace.
// Host is the API base URL, including its path (for example, /api/1.0).
type APIConfig struct {
	Host        string
	Token       string
	WorkspaceID string
	Logger      *slog.Logger // Defaults to slog.Default when nil.
}

// AsanaAPIClient executes API requests through a shared request limiter.
// It is safe for concurrent use.
type AsanaAPIClient struct {
	httpClient  *http.Client
	logger      *slog.Logger
	token       string
	workspaceID string
	apiHost     string
	retry       RetryConfig
	waitRetry   func(context.Context, time.Duration) error
	limiter     requestLimiter
}

// NewAsanaAPIClient creates a client with a full, two-token bucket.
// Every two seconds, two tokens are added, up to the bucket's capacity.
// The supplied HTTP client must not be nil. The API and retry configurations
// are validated before any requests are made.
func NewAsanaAPIClient(httpClient *http.Client, config APIConfig, retry RetryConfig) (*AsanaAPIClient, error) {
	if httpClient == nil {
		return nil, errors.New("HTTP client must not be nil")
	}

	if strings.TrimSpace(config.Token) == "" || strings.ContainsAny(config.Token, "\r\n") {
		return nil, errors.New("API_TOKEN must be non-empty and contain no line breaks")
	}

	if strings.TrimSpace(config.WorkspaceID) == "" {
		return nil, errors.New("WORKSPACE_ID must not be empty")
	}

	baseURL, err := url.Parse(config.Host)
	if err != nil || baseURL.Hostname() == "" ||
		(baseURL.Scheme != "http" && baseURL.Scheme != "https") ||
		baseURL.User != nil || baseURL.RawQuery != "" || baseURL.ForceQuery || baseURL.Fragment != "" {
		return nil, errors.New("API_HOST must be an absolute HTTP(S) API base URL without credentials, query, or fragment")
	}

	if err := retry.validate(); err != nil {
		return nil, err
	}

	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &AsanaAPIClient{
		httpClient:  httpClient,
		logger:      logger,
		token:       config.Token,
		workspaceID: config.WorkspaceID,
		apiHost:     strings.TrimRight(config.Host, "/"),
		retry:       retry,
		waitRetry:   waitForRetry,
		limiter: requestLimiter{
			tokens:     requestBucketCapacity,
			nextRefill: time.Now().Add(requestRefillInterval),
		},
	}, nil
}

// Do waits for a request token, adds API headers, and executes req using the injected client.
// Only use Do for Asana requests: it attaches the client's bearer token.
// The request context controls both the limiter wait and the HTTP request.
// The caller is responsible for closing the response body.
func (c *AsanaAPIClient) Do(req *http.Request) (*http.Response, error) {
	if err := c.limiter.wait(req.Context()); err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	return c.httpClient.Do(req)
}

type requestLimiter struct {
	mu         sync.Mutex
	tokens     int
	nextRefill time.Time
}

func (l *requestLimiter) wait(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		delay := l.take(time.Now())
		if delay == 0 {
			return nil
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// take consumes a token, or returns the duration until the next refill.
// Refilling on demand avoids a background goroutine and requires no cleanup.
func (l *requestLimiter) take(now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	if !now.Before(l.nextRefill) {
		intervals := now.Sub(l.nextRefill)/requestRefillInterval + 1
		l.nextRefill = l.nextRefill.Add(intervals * requestRefillInterval)
		l.tokens = requestBucketCapacity
	}

	if l.tokens > 0 {
		l.tokens--
		return 0
	}

	return l.nextRefill.Sub(now)
}

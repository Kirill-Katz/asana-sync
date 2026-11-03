package internal

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSyncAndPageLogging(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		var output bytes.Buffer
		config := testAPIConfig()
		config.Logger = slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: level}))
		client, err := NewAsanaAPIClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[]}`))}, nil
		})}, config, testRetryConfig())
		if err != nil {
			t.Fatal(err)
		}
		integration, err := NewAsanaIntegration(client, &integrationStoreStub{})
		if err != nil {
			t.Fatal(err)
		}
		if err := integration.SyncProjects(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := integration.SyncUsers(context.Background()); err != nil {
			t.Fatal(err)
		}
		logs := output.String()
		for _, resource := range []string{"projects", "users"} {
			if !strings.Contains(logs, `level=INFO msg="Starting `+resource+` synchronization"`) {
				t.Errorf("missing %s synchronization start log", resource)
			}
			if strings.Contains(logs, `level=DEBUG msg="Fetched `+resource+` page"`) != (level == slog.LevelDebug) {
				t.Errorf("unexpected %s page logging at level %v", resource, level)
			}
		}
		if strings.Contains(logs, config.Token) || strings.Contains(logs, "level=ERROR") {
			t.Fatal("successful sync logs contained a token or an error")
		}
	}
}

func TestRetryAndFailureLogging(t *testing.T) {
	for _, resource := range []string{"projects", "users"} {
		t.Run(resource, func(t *testing.T) {
			var output bytes.Buffer
			config := testAPIConfig()
			config.Logger = slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
			attempts := 0
			client, err := NewAsanaAPIClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				attempts++
				if attempts == 1 {
					return &http.Response{
						StatusCode: http.StatusTooManyRequests,
						Header:     http.Header{"Retry-After": {"3"}},
						Body:       io.NopCloser(strings.NewReader(`{}`)),
					}, nil
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[]}`))}, nil
			})}, config, RetryConfig{MaxAttempts: 2, MaxBackoff: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			var delays []time.Duration
			recordRetryWaits(client, &delays)
			if resource == "projects" {
				_, err = client.GetProjects(context.Background())
			} else {
				_, err = client.GetUsers(context.Background())
			}
			if err != nil {
				t.Fatal(err)
			}
			logs := output.String()
			for _, expected := range []string{
				`level=ERROR msg="Page fetch failed" resource=` + resource + ` attempt=1`,
				`level=INFO msg="Retrying page fetch" resource=` + resource + ` attempt=2 max_attempts=2 delay=3s retry_after=true`,
				`level=DEBUG msg="Fetched ` + resource + ` page"`,
			} {
				if !strings.Contains(logs, expected) {
					t.Errorf("missing %q in logs: %s", expected, logs)
				}
			}
			if strings.Contains(logs, config.Token) {
				t.Fatal("logs contained a token")
			}
		})
	}
}

func TestEachFetchedPageIsLogged(t *testing.T) {
	for _, resource := range []string{"projects", "users"} {
		var output bytes.Buffer
		config := testAPIConfig()
		config.Logger = slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
		pages := 0
		client, err := NewAsanaAPIClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			pages++
			body := `{"data":[],"next_page":null}`
			if pages == 1 {
				body = `{"data":[],"next_page":{"offset":"private-pagination-token"}}`
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}, config, testRetryConfig())
		if err != nil {
			t.Fatal(err)
		}
		if resource == "projects" {
			_, err = client.GetProjects(context.Background())
		} else {
			_, err = client.GetUsers(context.Background())
		}
		if err != nil {
			t.Fatal(err)
		}
		logs := output.String()
		if strings.Count(logs, `level=DEBUG msg="Fetched `+resource+` page"`) != 2 {
			t.Fatalf("expected two page logs for %s: %s", resource, logs)
		}
		if strings.Contains(logs, "private-pagination-token") {
			t.Fatal("logs contained a pagination token")
		}
	}
}

func TestCancellationDoesNotLogErrors(t *testing.T) {
	var output bytes.Buffer
	config := testAPIConfig()
	config.Logger = slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client, err := NewAsanaAPIClient(http.DefaultClient, config, testRetryConfig())
	if err != nil {
		t.Fatal(err)
	}
	integration, err := NewAsanaIntegration(client, &integrationStoreStub{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = integration.SyncProjects(ctx)
	_ = integration.SyncUsers(ctx)
	if output.Len() != 0 {
		t.Fatalf("canceled synchronization produced logs: %s", output.String())
	}
}

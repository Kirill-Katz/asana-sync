package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kirill-Katz/interview-reco/internal"
)

func TestRunRequiresOutputDirectory(t *testing.T) {
	if err := run(context.Background(), "", time.Minute, time.Minute, internal.RetryConfig{}); err == nil || !strings.Contains(err.Error(), "-output-dir") {
		t.Fatalf("got %v, want missing output directory error", err)
	}
}

func TestRunValidatesRetryConfiguration(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("API_TOKEN", "test-token")
	t.Setenv("API_HOST", "https://example.test/api/1.0")
	t.Setenv("WORKSPACE_ID", "test-workspace")

	for _, tc := range []struct {
		retry internal.RetryConfig
		field string
	}{
		{internal.RetryConfig{MaxAttempts: 0, MaxBackoff: time.Second}, "MaxAttempts"},
		{internal.RetryConfig{MaxAttempts: 1, MaxBackoff: time.Millisecond}, "MaxBackoff"},
		{internal.RetryConfig{MaxAttempts: 1, MaxBackoff: time.Second, MaxJitter: -time.Second}, "MaxJitter"},
	} {
		err := run(context.Background(), filepath.Join(t.TempDir(), "output"), time.Minute, time.Minute, tc.retry)
		if err == nil || !strings.Contains(err.Error(), tc.field) {
			t.Fatalf("got %v, want validation error for %s", err, tc.field)
		}
	}
}

func TestRunSyncsProjectsAndUsersToJSON(t *testing.T) {
	t.Chdir(t.TempDir())
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/api/1.0/projects":
			_, _ = io.WriteString(w, `{"data":[{"gid":"p1","name":"Example project"}]}`)
		case "/api/1.0/users":
			_, _ = io.WriteString(w, `{"data":[{"gid":"u1","name":"Example user"}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("API_TOKEN", "test-token")
	t.Setenv("API_HOST", server.URL+"/api/1.0")
	t.Setenv("WORKSPACE_ID", "test-workspace")

	directory := filepath.Join(t.TempDir(), "output")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, directory, time.Hour, time.Hour, internal.RetryConfig{
			MaxAttempts: 1,
			MaxBackoff:  time.Second,
		})
	}()
	for {
		_, projectErr := os.Stat(filepath.Join(directory, "projects", "project_p1.json"))
		_, userErr := os.Stat(filepath.Join(directory, "users", "user_u1.json"))
		if projectErr == nil && userErr == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("polling stopped before files were saved: %v", err)
		case <-ctx.Done():
			t.Fatal("timed out waiting for saved files")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
	projectJSON, err := os.ReadFile(filepath.Join(directory, "projects", "project_p1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var project internal.Project
	if err := json.Unmarshal(projectJSON, &project); err != nil {
		t.Fatal(err)
	}
	if project != (internal.Project{GID: "p1", Name: "Example project"}) {
		t.Fatalf("unexpected project: %+v", project)
	}
	userJSON, err := os.ReadFile(filepath.Join(directory, "users", "user_u1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var user internal.User
	if err := json.Unmarshal(userJSON, &user); err != nil {
		t.Fatal(err)
	}
	if user != (internal.User{GID: "u1", Name: "Example user"}) {
		t.Fatalf("unexpected user: %+v", user)
	}
	if requests.Load() != 2 {
		t.Fatalf("made %d requests, want exactly one per endpoint", requests.Load())
	}
}

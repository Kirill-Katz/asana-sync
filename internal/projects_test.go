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
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

const (
	testWorkspaceID = "test-workspace"
	testAPIHost     = "https://configured.example.test/api/1.0"
)

func testAPIConfig() APIConfig {
	return APIConfig{Token: "test-token", WorkspaceID: testWorkspaceID, Host: testAPIHost}
}

func testRetryConfig() RetryConfig {
	return RetryConfig{MaxAttempts: 1, MaxBackoff: time.Second}
}

func TestGetProjects(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		want    []Project
		wantErr bool
	}{
		{"success", http.StatusOK, `{"data":[{"gid":"123","name":"Example"}]}`, []Project{{GID: "123", Name: "Example"}}, false},
		{"empty", http.StatusOK, `{"data":[]}`, []Project{}, false},
		{"unauthorized", http.StatusUnauthorized, `{"errors":[]}`, nil, true},
		{"server error", http.StatusInternalServerError, `{"errors":[]}`, nil, true},
		{"invalid JSON", http.StatusOK, `{"data":`, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/1.0/projects" || r.URL.Query().Get("limit") != strconv.Itoa(projectsPageLimit) || r.URL.Query().Get("workspace") != testWorkspaceID {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
				}
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("missing expected bearer authorization")
				}
				if r.Header.Get("Accept") != "application/json" {
					t.Error("missing expected JSON accept header")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()

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
			client, err := NewAsanaAPIClient(httpClient, testAPIConfig(), testRetryConfig())
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.GetProjects(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("FetchProjects error = %v, wantErr %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestFetchProjectsCanceled(t *testing.T) {
	client, err := NewAsanaAPIClient(&http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("canceled request reached transport")
			return nil, nil
		}),
	}, testAPIConfig(), testRetryConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.GetProjects(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestNewAsanaAPIClientRejectsInvalidToken(t *testing.T) {
	for _, token := range []string{"", "   ", "test\r\ntoken"} {
		config := testAPIConfig()
		config.Token = token
		if _, err := NewAsanaAPIClient(http.DefaultClient, config, testRetryConfig()); err == nil {
			t.Fatal("expected invalid token error")
		}
	}
}

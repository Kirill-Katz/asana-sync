package internal

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIConfigValidation(t *testing.T) {
	for _, host := range []string{
		"",
		"app.asana.com/api/1.0",
		"/api/1.0",
		"https:///api/1.0",
		"ftp://example.test/api",
		"https://user:password@example.test/api",
		"https://example.test/api?workspace=other",
		"https://example.test/api?",
		"https://example.test/api#fragment",
		"https://example.test:invalid/api",
	} {
		config := testAPIConfig()
		config.Host = host
		if _, err := NewAsanaAPIClient(http.DefaultClient, config, testRetryConfig()); err == nil {
			t.Error("accepted invalid API host")
		}
	}
	for _, workspace := range []string{"", " \t"} {
		config := testAPIConfig()
		config.WorkspaceID = workspace
		if _, err := NewAsanaAPIClient(http.DefaultClient, config, testRetryConfig()); err == nil {
			t.Error("accepted empty workspace")
		}
	}
}

func TestConfiguredHostAndWorkspace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom/api/projects" && r.URL.Path != "/custom/api/users" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.URL.Query().Get("workspace") != "workspace +/&" || r.URL.Query().Get("limit") != "100" {
			t.Errorf("unexpected query %q", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing configured authorization")
		}
		_, _ = io.WriteString(w, `{"data":[],"next_page":null}`)
	}))
	defer server.Close()

	client, err := NewAsanaAPIClient(server.Client(), APIConfig{
		Host:        server.URL + "/custom/api/",
		Token:       "test-token",
		WorkspaceID: "workspace +/&",
	}, testRetryConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetProjects(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetUsers(context.Background()); err != nil {
		t.Fatal(err)
	}
}

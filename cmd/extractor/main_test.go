package main

import (
	"os"
	"testing"

	"github.com/Kirill-Katz/interview-reco/internal"
)

func TestReadAPIConfig(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     map[string]string
		dotenv  string
		want    internal.APIConfig
		wantErr bool
	}{
		{
			name:   "dotenv",
			dotenv: "API_TOKEN='file-token'\nWORKSPACE_ID=file-workspace\nAPI_HOST=https://example.test/api/1.0\n",
			want:   internal.APIConfig{Token: "file-token", WorkspaceID: "file-workspace", Host: "https://example.test/api/1.0"},
		},
		{
			name:   "environment overrides dotenv per key",
			env:    map[string]string{"API_TOKEN": "env-token", "API_HOST": "https://override.test/api/1.0"},
			dotenv: "API_TOKEN=file-token\nWORKSPACE_ID=file-workspace\nAPI_HOST=https://example.test/api/1.0\n",
			want:   internal.APIConfig{Token: "env-token", WorkspaceID: "file-workspace", Host: "https://override.test/api/1.0"},
		},
		{
			name: "environment without file",
			env:  map[string]string{"API_TOKEN": "env-token", "WORKSPACE_ID": "env-workspace", "API_HOST": "https://example.test"},
			want: internal.APIConfig{Token: "env-token", WorkspaceID: "env-workspace", Host: "https://example.test"},
		},
		{
			name:   "explicitly empty environment overrides file",
			env:    map[string]string{"API_TOKEN": ""},
			dotenv: "API_TOKEN=file-token\n",
		},
		{name: "missing settings left for constructor validation"},
		{name: "malformed file", dotenv: "API_TOKEN='unterminated\n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			for _, key := range []string{"API_TOKEN", "WORKSPACE_ID", "API_HOST"} {
				t.Setenv(key, "")
				if err := os.Unsetenv(key); err != nil {
					t.Fatal(err)
				}
			}
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			if tc.dotenv != "" {
				if err := os.WriteFile(".env", []byte(tc.dotenv), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := readAPIConfig()
			if (err != nil) != tc.wantErr {
				t.Fatalf("readAPIConfig error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Error("unexpected configuration")
			}
		})
	}
}

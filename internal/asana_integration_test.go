package internal

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

type integrationStoreStub struct {
	saveProject func(context.Context, Project) error
	saveUser    func(context.Context, User) error
}

func (s *integrationStoreStub) SaveProject(ctx context.Context, project Project) error {
	return s.saveProject(ctx, project)
}

func (s *integrationStoreStub) SaveUser(ctx context.Context, user User) error {
	return s.saveUser(ctx, user)
}

func newIntegrationTestClient(t *testing.T, handler http.HandlerFunc) *AsanaAPIClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	config := testAPIConfig()
	config.Host = server.URL + "/api/1.0"
	client, err := NewAsanaAPIClient(server.Client(), config, testRetryConfig())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestAsanaIntegrationConstructor(t *testing.T) {
	if _, err := NewAsanaIntegration(nil, &integrationStoreStub{}); err == nil {
		t.Fatal("accepted nil client")
	}
	if _, err := NewAsanaIntegration(&AsanaAPIClient{}, nil); err == nil {
		t.Fatal("accepted nil storer")
	}
}

func TestAsanaIntegrationSync(t *testing.T) {
	client := newIntegrationTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/1.0/projects":
			_, _ = io.WriteString(w, `{"data":[{"gid":"p1","name":"Project one"},{"gid":"p2","name":"Project two"}]}`)
		case "/api/1.0/users":
			_, _ = io.WriteString(w, `{"data":[{"gid":"u1","name":"User one"},{"gid":"u2","name":"User two"}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var projects []Project
	var users []User
	store := &integrationStoreStub{
		saveProject: func(received context.Context, project Project) error {
			if received != ctx {
				t.Error("project save did not receive the original context")
			}
			projects = append(projects, project)
			return nil
		},
		saveUser: func(received context.Context, user User) error {
			if received != ctx {
				t.Error("user save did not receive the original context")
			}
			users = append(users, user)
			return nil
		},
	}
	integration, err := NewAsanaIntegration(client, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := integration.SyncProjects(ctx); err != nil {
		t.Fatal(err)
	}
	if err := integration.SyncUsers(ctx); err != nil {
		t.Fatal(err)
	}
	if want := []Project{{GID: "p1", Name: "Project one"}, {GID: "p2", Name: "Project two"}}; !reflect.DeepEqual(projects, want) {
		t.Fatalf("projects=%v, want %v", projects, want)
	}
	if want := []User{{GID: "u1", Name: "User one"}, {GID: "u2", Name: "User two"}}; !reflect.DeepEqual(users, want) {
		t.Fatalf("users=%v, want %v", users, want)
	}
}

func TestAsanaIntegrationCancellationDuringFetch(t *testing.T) {
	for _, syncMethod := range []func(*AsanaIntegration, context.Context) error{
		(*AsanaIntegration).SyncProjects,
		(*AsanaIntegration).SyncUsers,
	} {
		ctx, cancel := context.WithCancel(context.Background())
		client, err := NewAsanaAPIClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Context() != ctx {
				t.Error("request did not receive the sync context")
			}
			cancel()
			return nil, context.Canceled
		})}, testAPIConfig(), testRetryConfig())
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		// No save functions are supplied: any attempt to save would fail the test.
		integration, err := NewAsanaIntegration(client, &integrationStoreStub{})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		err = syncMethod(integration, ctx)
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want cancellation", err)
		}
	}
}

func TestAsanaIntegrationFailuresAndCancellation(t *testing.T) {
	for _, syncMethod := range []struct {
		name string
		sync func(*AsanaIntegration, context.Context) error
	}{
		{"projects", (*AsanaIntegration).SyncProjects},
		{"users", (*AsanaIntegration).SyncUsers},
	} {
		for _, scenario := range []string{"fetch failure", "later page failure", "save failure", "already canceled", "canceled during save", "canceled during final save", "empty results"} {
			t.Run(syncMethod.name+"/"+scenario, func(t *testing.T) {
				var requests atomic.Int32
				client := newIntegrationTestClient(t, func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if scenario == "fetch failure" || scenario == "later page failure" && r.URL.Query().Get("offset") != "" {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if scenario == "later page failure" {
						_, _ = io.WriteString(w, `{"data":[{"gid":"1","name":"One"}],"next_page":{"offset":"next"}}`)
						return
					}
					if scenario == "empty results" {
						_, _ = io.WriteString(w, `{"data":[]}`)
						return
					}
					if scenario == "canceled during final save" {
						_, _ = io.WriteString(w, `{"data":[{"gid":"1","name":"One"}]}`)
						return
					}
					_, _ = io.WriteString(w, `{"data":[{"gid":"1","name":"One"},{"gid":"2","name":"Two"}]}`)
				})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if scenario == "already canceled" {
					cancel()
				}
				calls := 0
				saveFailure := errors.New("storage failed")
				save := func() error {
					calls++
					if scenario == "save failure" {
						return saveFailure
					}
					if scenario == "canceled during save" || scenario == "canceled during final save" {
						cancel()
					}
					return nil
				}
				store := &integrationStoreStub{
					saveProject: func(context.Context, Project) error { return save() },
					saveUser:    func(context.Context, User) error { return save() },
				}
				integration, err := NewAsanaIntegration(client, store)
				if err != nil {
					t.Fatal(err)
				}
				err = syncMethod.sync(integration, ctx)
				switch scenario {
				case "fetch failure", "later page failure":
					if err == nil || calls != 0 {
						t.Fatalf("err=%v saves=%d, want error and no saves", err, calls)
					}
				case "save failure":
					if !errors.Is(err, saveFailure) || calls != 1 {
						t.Fatalf("err=%v saves=%d, want wrapped save error and one save", err, calls)
					}
				case "already canceled":
					if !errors.Is(err, context.Canceled) || requests.Load() != 0 || calls != 0 {
						t.Fatalf("err=%v requests=%d saves=%d", err, requests.Load(), calls)
					}
				case "canceled during save", "canceled during final save":
					if !errors.Is(err, context.Canceled) || calls != 1 {
						t.Fatalf("err=%v saves=%d, want cancellation and one save", err, calls)
					}
				case "empty results":
					if err != nil || calls != 0 {
						t.Fatalf("err=%v saves=%d, want success and no saves", err, calls)
					}
				}
			})
		}
	}
}

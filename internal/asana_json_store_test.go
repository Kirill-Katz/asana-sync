package internal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestNewAsanaJsonStore(t *testing.T) {
	if _, err := NewAsanaJsonStore(" "); err == nil {
		t.Fatal("accepted empty directory")
	}
	directory := filepath.Join(t.TempDir(), "nested", "store")
	store, err := NewAsanaJsonStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, subdirectory := range []string{"projects", "users"} {
		if info, err := os.Stat(filepath.Join(store.directory, subdirectory)); err != nil || !info.IsDir() {
			t.Fatalf("destination %s directory not created: %v", subdirectory, err)
		}
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAsanaJsonStore(file); err == nil {
		t.Fatal("accepted a regular file as the store directory")
	}
}

func TestAsanaJsonStoreSave(t *testing.T) {
	directory := t.TempDir()
	store, err := NewAsanaJsonStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	project := Project{GID: "123", Name: "Example project"}
	user := User{GID: "123", Name: "Example user"}
	if err := store.SaveProject(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}

	var gotProject Project
	readStoreJSON(t, filepath.Join(directory, "projects", "project_123.json"), &gotProject)
	var gotUser User
	readStoreJSON(t, filepath.Join(directory, "users", "user_123.json"), &gotUser)
	if gotProject != project || gotUser != user {
		t.Fatalf("unexpected records: project=%+v user=%+v", gotProject, gotUser)
	}
	project.Name = "Updated project"
	if err := store.SaveProject(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	readStoreJSON(t, filepath.Join(directory, "projects", "project_123.json"), &gotProject)
	if gotProject != project {
		t.Fatal("replacement was not saved")
	}
	assertStoreFiles(t, directory, []string{"projects", "users"})
	assertStoreFiles(t, filepath.Join(directory, "projects"), []string{"project_123.json"})
	assertStoreFiles(t, filepath.Join(directory, "users"), []string{"user_123.json"})
}

func TestAsanaJsonStoreRejectsUnsafeIDs(t *testing.T) {
	directory := t.TempDir()
	store, err := NewAsanaJsonStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "  ", "../../outside", "a/b", `a\b`, "a\x00b"} {
		if err := store.SaveProject(context.Background(), Project{GID: id}); err == nil {
			t.Error("accepted unsafe project ID")
		}
		if err := store.SaveUser(context.Background(), User{GID: id}); err == nil {
			t.Error("accepted unsafe user ID")
		}
	}
	assertStoreFiles(t, directory, []string{"projects", "users"})
	assertStoreFiles(t, filepath.Join(directory, "projects"), []string{})
	assertStoreFiles(t, filepath.Join(directory, "users"), []string{})
}

func TestAsanaJsonStorePreservesExistingFile(t *testing.T) {
	directory := t.TempDir()
	store, err := NewAsanaJsonStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	original := Project{GID: "123", Name: "Original"}
	if err := store.SaveProject(context.Background(), original); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.SaveProject(ctx, Project{GID: "123", Name: "Replacement"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
	if err := store.SaveUser(ctx, User{GID: "456"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
	if err := store.save(context.Background(), "projects", "project_123.json", make(chan int)); err == nil {
		t.Fatal("expected JSON encoding error")
	}

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	if err := store.save(ctx, "projects", "project_123.json", cancelingJSON{cancel: cancel}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation before publication", err)
	}
	var got Project
	readStoreJSON(t, filepath.Join(directory, "projects", "project_123.json"), &got)
	if got != original {
		t.Fatal("failed save overwrote good data")
	}
	assertStoreFiles(t, directory, []string{"projects", "users"})
	assertStoreFiles(t, filepath.Join(directory, "projects"), []string{"project_123.json"})
	assertStoreFiles(t, filepath.Join(directory, "users"), []string{})
}

type cancelingJSON struct{ cancel context.CancelFunc }

func (v cancelingJSON) MarshalJSON() ([]byte, error) {
	v.cancel()
	return []byte(`{"gid":"123","name":"Replacement"}`), nil
}

func TestAsanaJsonStoreRenameFailureCleansTempFile(t *testing.T) {
	directory := t.TempDir()
	store, err := NewAsanaJsonStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	// A directory at the target path makes rename fail without relying on permissions.
	if err := os.Mkdir(filepath.Join(directory, "users", "user_123.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUser(context.Background(), User{GID: "123"}); err == nil {
		t.Fatal("expected rename error")
	}
	assertStoreFiles(t, filepath.Join(directory, "users"), []string{"user_123.json"})
}

func TestAsanaJsonStoreTempCreationFailure(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "store")
	store, err := NewAsanaJsonStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(directory, "users")); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUser(context.Background(), User{GID: "123"}); err == nil {
		t.Fatal("expected temporary file creation error")
	}
}

func TestAsanaJsonStoreConcurrentSaves(t *testing.T) {
	directory := t.TempDir()
	store, err := NewAsanaJsonStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			if err := store.SaveUser(context.Background(), User{GID: "123", Name: "Example"}); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	var got User
	readStoreJSON(t, filepath.Join(directory, "users", "user_123.json"), &got)
	if got != (User{GID: "123", Name: "Example"}) {
		t.Fatalf("unexpected user: %+v", got)
	}
	assertStoreFiles(t, filepath.Join(directory, "users"), []string{"user_123.json"})
}

func readStoreJSON(t *testing.T, path string, destination any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, destination); err != nil {
		t.Fatal(err)
	}
}

func assertStoreFiles(t *testing.T, directory string, want []string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("store files=%v, want %v", names, want)
	}
}

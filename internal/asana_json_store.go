package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AsanaJsonStore writes one JSON file per entity under projects/ or users/
// within its configured directory.
// Concurrent saves are safe; the last successful rename for an entity wins.
type AsanaJsonStore struct {
	directory string
}

// Compile time check if AsanaJsonStore correctly implemenets its Store interface
var _ AsanaStorer = (*AsanaJsonStore)(nil)

// NewAsanaJsonStore creates the projects and users directories if necessary.
func NewAsanaJsonStore(directory string) (*AsanaJsonStore, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("JSON store directory must not be empty")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve JSON store directory: %w", err)
	}
	for _, subdirectory := range []string{"projects", "users"} {
		if err := os.MkdirAll(filepath.Join(directory, subdirectory), 0750); err != nil {
			return nil, fmt.Errorf("create JSON store %s directory: %w", subdirectory, err)
		}
	}
	return &AsanaJsonStore{directory: directory}, nil
}

// SaveProject atomically publishes projects/project_<gid>.json.
func (s *AsanaJsonStore) SaveProject(ctx context.Context, project Project) error {
	if err := validateEntityID(project.GID); err != nil {
		return err
	}
	return s.save(ctx, "projects", "project_"+project.GID+".json", project)
}

// SaveUser atomically publishes users/user_<gid>.json.
func (s *AsanaJsonStore) SaveUser(ctx context.Context, user User) error {
	if err := validateEntityID(user.GID); err != nil {
		return err
	}
	return s.save(ctx, "users", "user_"+user.GID+".json", user)
}

func validateEntityID(id string) error {
	if strings.TrimSpace(id) == "" || strings.ContainsAny(id, "/\\\x00") {
		return errors.New("entity GID must be non-empty and contain no path separators or NUL bytes")
	}
	return nil
}

func (s *AsanaJsonStore) save(ctx context.Context, subdirectory, filename string, entity any) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Creating the temporary file in the destination directory keeps the rename
	// on the same filesystem so readers never see a partially written entity.
	directory := filepath.Join(s.directory, subdirectory)
	temp, err := os.CreateTemp(directory, "."+filename+"-*")
	if err != nil {
		return fmt.Errorf("create temporary JSON file: %w", err)
	}
	defer os.Remove(temp.Name())
	defer temp.Close()

	if err := json.NewEncoder(temp).Encode(entity); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("encode %s: %w", filename, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary JSON file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), filepath.Join(directory, filename)); err != nil {
		return fmt.Errorf("publish %s: %w", filename, err)
	}
	return nil
}

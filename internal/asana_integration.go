package internal

import (
	"context"
	"errors"
	"fmt"
)

// AsanaIntegration fetches Asana entities and persists them through a storer.
type AsanaIntegration struct {
	client *AsanaAPIClient
	storer AsanaStorer
}

func NewAsanaIntegration(client *AsanaAPIClient, storer AsanaStorer) (*AsanaIntegration, error) {
	if client == nil {
		return nil, errors.New("Asana API client must not be nil")
	}
	if storer == nil {
		return nil, errors.New("Asana storer must not be nil")
	}
	return &AsanaIntegration{client: client, storer: storer}, nil
}

// SyncProjects fetches all projects, then saves them sequentially.
// It stops on the first error; previously saved projects remain stored.
func (i *AsanaIntegration) SyncProjects(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	i.client.logger.InfoContext(ctx, "Starting projects synchronization")
	projects, err := i.client.GetProjects(ctx)
	if err != nil {
		return fmt.Errorf("sync projects: %w", err)
	}
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := i.storer.SaveProject(ctx, project); err != nil {
			return fmt.Errorf("save project %s: %w", project.GID, err)
		}
	}
	return ctx.Err()
}

// SyncUsers fetches all users, then saves them sequentially.
// It stops on the first error; previously saved users remain stored.
func (i *AsanaIntegration) SyncUsers(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	i.client.logger.InfoContext(ctx, "Starting users synchronization")
	users, err := i.client.GetUsers(ctx)
	if err != nil {
		return fmt.Errorf("sync users: %w", err)
	}
	for _, user := range users {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := i.storer.SaveUser(ctx, user); err != nil {
			return fmt.Errorf("save user %s: %w", user.GID, err)
		}
	}
	return ctx.Err()
}

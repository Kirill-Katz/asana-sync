package internal

import "context"

// AsanaStorer persists individual Asana entities.
type AsanaStorer interface {
	SaveProject(ctx context.Context, project Project) error
	SaveUser(ctx context.Context, user User) error
}

package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// User is a compact user record returned by Asana.
type User struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

type usersPage struct {
	Data     []User `json:"data"`
	NextPage *struct {
		Offset string `json:"offset"`
	} `json:"next_page"`
}

// GetUsers fetches all pages accessible to the authenticated user.
// It returns no partial results if any page fails.
func (c *AsanaAPIClient) GetUsers(ctx context.Context) ([]User, error) {
	const limit = 100
	var offset string
	users := []User{}
	seen := map[string]bool{"": true}
	for {
		page, err := c.fetchUsersPageWithRetries(ctx, limit, offset)
		if err != nil {
			return nil, err
		}
		users = append(users, page.Data...)
		if page.NextPage == nil {
			return users, nil
		}
		offset = page.NextPage.Offset
		if seen[offset] {
			return nil, errors.New("fetch users: empty or repeated pagination offset")
		}
		seen[offset] = true
	}
}

func (c *AsanaAPIClient) fetchUsersPageWithRetries(ctx context.Context, limit int, offset string) (usersPage, error) {
	return fetchWithRetries(ctx, c, "users", func() (usersPage, error) {
		return c.fetchUsersPage(ctx, limit, offset)
	})
}

// fetchUsersPage makes exactly one rate-limited request.
func (c *AsanaAPIClient) fetchUsersPage(ctx context.Context, limit int, offset string) (usersPage, error) {
	query := url.Values{
		"limit":     {strconv.Itoa(limit)},
		"workspace": {c.workspaceID},
	}
	if offset != "" {
		query.Set("offset", offset)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiHost+"/users?"+query.Encode(), nil)
	if err != nil {
		return usersPage{}, fmt.Errorf("create users request: %w", err)
	}

	resp, err := c.Do(req)
	if err != nil {
		err = fmt.Errorf("fetch users: %w", err)
		if ctx.Err() != nil {
			return usersPage{}, ctx.Err()
		}
		if errors.Is(err, context.Canceled) {
			return usersPage{}, err
		}
		return usersPage{}, &retryableError{err: err, retryAfter: -1}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err := apiStatusError("users", resp.StatusCode)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 && resp.StatusCode <= 599 {
			return usersPage{}, &retryableError{err: err, retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
		}
		return usersPage{}, err
	}

	var result usersPage
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		if ctx.Err() != nil {
			return usersPage{}, ctx.Err()
		}
		return usersPage{}, fmt.Errorf("decode users: %w", err)
	}

	c.logger.DebugContext(ctx, "Fetched users page",
		"limit", limit,
		"count", len(result.Data),
		"has_next_page", result.NextPage != nil,
	)
	return result, nil
}

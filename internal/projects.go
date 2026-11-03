package internal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

const projectsPageLimit = 100

// Project is a compact project record returned by Asana.
type Project struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

type projectsPage struct {
	Data     []Project `json:"data"`
	NextPage *struct {
		Offset string `json:"offset"`
	} `json:"next_page"`
}

// GetProjects fetches all pages accessible to the authenticated user.
// It returns no partial results if any page fails.
func (c *AsanaAPIClient) GetProjects(ctx context.Context) ([]Project, error) {
	var offset string
	projects := []Project{}
	seen := map[string]bool{"": true}
	for {
		page, err := c.fetchProjectsPageWithRetries(ctx, projectsPageLimit, offset)
		if err != nil {
			return nil, err
		}
		projects = append(projects, page.Data...)
		if page.NextPage == nil {
			return projects, nil
		}
		offset = page.NextPage.Offset
		if seen[offset] {
			return nil, errors.New("fetch projects: empty or repeated pagination offset")
		}
		seen[offset] = true
	}
}

func (c *AsanaAPIClient) fetchProjectsPageWithRetries(ctx context.Context, limit int, offset string) (projectsPage, error) {
	return fetchWithRetries(ctx, c, "projects", func() (projectsPage, error) {
		return c.fetchProjectsPage(ctx, limit, offset)
	})
}

// fetchProjectsPage makes exactly one rate-limited request.
func (c *AsanaAPIClient) fetchProjectsPage(ctx context.Context, limit int, offset string) (projectsPage, error) {
	query := url.Values{
		"limit":     {strconv.Itoa(limit)},
		"workspace": {c.workspaceID},
	}
	if offset != "" {
		query.Set("offset", offset)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiHost+"/projects?"+query.Encode(), nil)
	if err != nil {
		return projectsPage{}, fmt.Errorf("create projects request: %w", err)
	}

	resp, err := c.Do(req)
	if err != nil {
		err = fmt.Errorf("fetch projects: %w", err)
		if ctx.Err() != nil {
			return projectsPage{}, ctx.Err()
		}
		if errors.Is(err, context.Canceled) {
			return projectsPage{}, err
		}
		return projectsPage{}, &retryableError{err: err, retryAfter: -1}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err := apiStatusError("projects", resp.StatusCode)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 && resp.StatusCode <= 599 {
			return projectsPage{}, &retryableError{err: err, retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
		}
		return projectsPage{}, err
	}

	var result projectsPage
	if err := decodeAPIResponse(ctx, "projects", resp.Body, &result); err != nil {
		return projectsPage{}, err
	}

	c.logger.DebugContext(ctx, "Fetched projects page",
		"limit", limit,
		"count", len(result.Data),
		"has_next_page", result.NextPage != nil,
	)
	return result, nil
}

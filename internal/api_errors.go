package internal

import (
	"fmt"
	"net/http"
)

func apiStatusError(resource string, status int) error {
	message := http.StatusText(status)
	switch status {
	case http.StatusBadRequest:
		message = "bad request; check WORKSPACE_ID and request parameters"
	case http.StatusUnauthorized:
		message = "authentication failed; check that API_TOKEN is valid and has not expired"
	case http.StatusForbidden:
		message = "access denied; check token scopes and workspace permissions"
	case http.StatusNotFound:
		message = "resource not found; check API_HOST, WORKSPACE_ID, and the requested resource"
	}
	return fmt.Errorf("fetch %s: HTTP status %d (%s)", resource, status, message)
}

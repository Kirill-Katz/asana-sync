package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

func decodeAPIResponse(ctx context.Context, resource string, body io.Reader, result any) error {
	reader := &bodyReadTracker{reader: body}
	if err := json.NewDecoder(reader).Decode(result); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		if reader.err != nil && !errors.As(err, &syntaxErr) && !errors.As(err, &typeErr) {
			readErr := fmt.Errorf("read %s response body: %w", resource, reader.err)
			if errors.Is(reader.err, context.Canceled) {
				return readErr
			}
			return &retryableError{err: readErr, retryAfter: -1}
		}
		return fmt.Errorf("decode %s: %w", resource, err)
	}
	return nil
}

// bodyReadTracker distinguishes response-body I/O failures from errors generated
// by the JSON decoder. In particular, a clean EOF with incomplete JSON is a
// malformed response, while an unexpected EOF from the body is a read failure.
type bodyReadTracker struct {
	reader io.Reader
	err    error
}

func (r *bodyReadTracker) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err != nil && err != io.EOF {
		r.err = err
	}
	return n, err
}

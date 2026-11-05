// Package bounded, an optional helper, reads a service's answer in bounded memory: Read returns the body, refuses a 2xx answer longer than limit by naming call and limit, and cuts a refusal's text to limit, since its status already tells the caller what happened.
package bounded

import (
	"fmt"
	"io"
	"net/http"
)

func Read(resp *http.Response, call string, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%s: reading the answer: %w", call, err)
	}
	if int64(len(raw)) <= limit {
		return raw, nil
	}
	if resp.StatusCode < http.StatusMultipleChoices {
		return nil, fmt.Errorf("%s: the answer is longer than %d bytes", call, limit)
	}
	return raw[:limit], nil
}

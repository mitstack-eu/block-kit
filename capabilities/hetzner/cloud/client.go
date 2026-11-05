// Package cloud calls the Hetzner Cloud API for a block that creates Cloud servers, with the API token the caller passes.
package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/mitstack-eu/block-kit/helpers/bounded"
)

// Endpoint is the Cloud API, which a block never takes from a request.
const Endpoint = "https://api.hetzner.cloud/v1"

const (
	timeout        = 60 * time.Second
	maxAnswerBytes = 1 << 20
)

type Client struct {
	base, token string
	http        *http.Client
}

func New(token string) *Client { return NewAt(Endpoint, token) }

// NewAt calls the API at base, which only a test's fake sets.
func NewAt(base, token string) *Client {
	return &Client{base: base, token: token, http: &http.Client{Timeout: timeout}}
}

// Error is an error answer of the API, such as 422 must_be_unassigned.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("Hetzner answered %d %s: %s", e.Status, e.Code, e.Message)
}

// ErrorCode is the code of a Hetzner error answer within err, or "".
func ErrorCode(err error) string {
	var he *Error
	if errors.As(err, &he) {
		return he.Code
	}
	return ""
}

// do sends in as JSON when it is not nil, and decodes the answer into out when out is not nil.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encoding the Hetzner request %s %s: %w", method, path, err)
		}
		body = bytes.NewReader(raw)
	}
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return fmt.Errorf("building the Hetzner request %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("calling Hetzner %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := bounded.Read(resp, "Hetzner "+method+" "+path, maxAnswerBytes)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var answer struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &answer)
		return &Error{Status: resp.StatusCode, Code: answer.Error.Code, Message: answer.Error.Message}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decoding Hetzner's answer to %s %s: %w", method, path, err)
	}
	return nil
}

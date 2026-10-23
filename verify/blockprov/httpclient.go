package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// mintOIDCToken adds audience to any query parameters tokenURL already carries rather than replacing them.
func mintOIDCToken(tokenURL, requestToken, audience string) (string, error) {
	u, err := url.Parse(tokenURL)
	if err != nil {
		return "", fmt.Errorf("parsing token URL: %w", err)
	}
	q := u.Query()
	q.Set("audience", audience)
	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("building token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+requestToken)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("requesting OIDC token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OIDC token endpoint returned %d: %s", resp.StatusCode, body)
	}

	var out struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decoding OIDC token response: %w", err)
	}
	if out.Value == "" {
		return "", fmt.Errorf("OIDC token endpoint returned an empty token")
	}
	return out.Value, nil
}

func readPathOrURL(ref string) ([]byte, error) {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		resp, err := http.Get(ref)
		if err != nil {
			return nil, fmt.Errorf("fetching %s: %w", ref, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetching %s: status %d", ref, resp.StatusCode)
		}
		return io.ReadAll(resp.Body)
	}
	return os.ReadFile(ref)
}

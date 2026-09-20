package main

// http.go posts a derived delivery to the board's /v1/delivered endpoint.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// deliveryRecord is the body /v1/delivered expects: the delivered sha and
// the board keys it delivers, in the derived order. Named distinctly from
// the test fixture's own deliveredPayload decoding type, which lives in the
// same package.
type deliveryRecord struct {
	Delivery string   `json:"delivery"`
	Keys     []string `json:"keys"`
}

// postDelivered posts exactly one request recording that delivery shipped
// keys. Authorization is sent only when token is non-empty — the header
// must be entirely absent otherwise, never sent empty. A non-2xx response
// and a transport failure are both hard failures; a non-2xx error carries
// the response body verbatim, so a caller can see what the board rejected.
func postDelivered(baseURL, token, delivery string, keys []string) error {
	body, err := json.Marshal(deliveryRecord{Delivery: delivery, Keys: keys})
	if err != nil {
		return fmt.Errorf("encoding delivered payload: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/delivered", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("posting delivery: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("board returned %d: %s", resp.StatusCode, raw)
	}
	return nil
}

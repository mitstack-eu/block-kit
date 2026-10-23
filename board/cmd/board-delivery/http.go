package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type deliveryRecord struct {
	Delivery string   `json:"delivery"`
	Keys     []string `json:"keys"`
}

// postDelivered sends no Authorization header for an empty token, and a non-2xx error carries the board's answer verbatim.
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

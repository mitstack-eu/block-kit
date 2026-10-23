package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/observe"
)

// hetznerAPI is the one host the operator sends a token to, fixed in code so that no request can point it elsewhere.
const hetznerAPI = "https://api.hetzner.cloud/v1"

const hetznerTimeout = 30 * time.Second

var errNotFound = errors.New("hetzner: not found")

// hetzner calls the Hetzner Cloud API with the api_token each lifecycle request carries in its hetzner secrets.
type hetzner struct {
	base   string
	client *http.Client
}

type call struct {
	method, path string
	body, answer any
}

func newHetzner(base string) *hetzner {
	return &hetzner{base: base, client: &http.Client{Timeout: hetznerTimeout}}
}

// ensureFirewall creates the subscription's firewall only when none carries its label, so a repeated subscribe changes nothing.
func (h *hetzner) ensureFirewall(ctx context.Context, d contract.Desired) (string, error) {
	held, err := h.ListByLabel(ctx, d, subscriptionSelector(d))
	if err != nil {
		return "", err
	}
	if len(held) > 0 {
		return held[0].ID, nil
	}
	var created struct {
		Firewall struct {
			ID int64 `json:"id"`
		} `json:"firewall"`
	}
	body := map[string]any{
		"name":   "mitstack-" + d.SubscriptionID,
		"labels": map[string]string{observe.HetznerSubscriptionLabel: d.SubscriptionID},
	}
	if err := h.send(ctx, d, call{method: http.MethodPost, path: "/firewalls", body: body, answer: &created}); err != nil {
		return "", err
	}
	return strconv.FormatInt(created.Firewall.ID, 10), nil
}

// deleteFirewalls deletes every firewall carrying the subscription's label, so it needs no record of what an earlier subscribe made.
func (h *hetzner) deleteFirewalls(ctx context.Context, d contract.Desired) error {
	held, err := h.ListByLabel(ctx, d, subscriptionSelector(d))
	if err != nil {
		return err
	}
	for _, fw := range held {
		err := h.send(ctx, d, call{method: http.MethodDelete, path: "/firewalls/" + fw.ID})
		if err != nil && !errors.Is(err, errNotFound) {
			return err
		}
	}
	return nil
}

// FirewallExists serves observe.HetznerFirewall.
func (h *hetzner) FirewallExists(ctx context.Context, d contract.Desired, firewallID string) (bool, error) {
	err := h.send(ctx, d, call{method: http.MethodGet, path: "/firewalls/" + url.PathEscape(firewallID)})
	if errors.Is(err, errNotFound) {
		return false, nil
	}
	return err == nil, err
}

// ListByLabel serves observe.HetznerLabelled with the firewalls, the one kind of object this block creates.
func (h *hetzner) ListByLabel(ctx context.Context, d contract.Desired, selector string) ([]observe.HetznerObject, error) {
	var listed struct {
		Firewalls []struct {
			ID int64 `json:"id"`
		} `json:"firewalls"`
	}
	path := "/firewalls?" + url.Values{"label_selector": {selector}}.Encode()
	if err := h.send(ctx, d, call{method: http.MethodGet, path: path, answer: &listed}); err != nil {
		return nil, err
	}
	objects := make([]observe.HetznerObject, 0, len(listed.Firewalls))
	for _, fw := range listed.Firewalls {
		objects = append(objects, observe.HetznerObject{Kind: "firewall", ID: strconv.FormatInt(fw.ID, 10)})
	}
	return objects, nil
}

func subscriptionSelector(d contract.Desired) string {
	return observe.HetznerSubscriptionLabel + "=" + d.SubscriptionID
}

// send answers errNotFound for a 404, and its errors name the route and status but never the token.
func (h *hetzner) send(ctx context.Context, d contract.Desired, c call) error {
	token := d.Secrets["hetzner"]["api_token"]
	if token == "" {
		return handler.Refuse("no-hetzner-token", "the request carries no hetzner api_token secret")
	}
	body := &bytes.Buffer{}
	if c.body != nil {
		if err := json.NewEncoder(body).Encode(c.body); err != nil {
			return fmt.Errorf("hetzner: encoding %s %s: %w", c.method, c.path, err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, c.method, h.base+c.path, body)
	if err != nil {
		return fmt.Errorf("hetzner: building %s %s: %w", c.method, c.path, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("hetzner: %s %s: %w", c.method, c.path, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errNotFound
	case resp.StatusCode >= http.StatusMultipleChoices:
		return fmt.Errorf("hetzner: %s %s answered %s", c.method, c.path, resp.Status)
	case c.answer == nil:
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(c.answer); err != nil {
		return fmt.Errorf("hetzner: reading the answer to %s %s: %w", c.method, c.path, err)
	}
	return nil
}

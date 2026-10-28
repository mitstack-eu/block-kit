// Package robot calls Hetzner's Robot webservice for a block that adopts a dedicated server, with the login the caller passes.
package robot

// The webservice offers no server cancellation and no order through these calls, so no block can end a contract or buy a server.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Endpoint is Robot's webservice, which a block never takes from a request.
const Endpoint = "https://robot-ws.your-server.de"

const (
	timeout        = 60 * time.Second
	maxAnswerBytes = 1 << 20
	resetType      = "hw"
	rescueOS       = "linux"
)

type Client struct {
	base, user, password string
	http                 *http.Client
	// PollInterval is the wait between reads while Robot applies a change.
	PollInterval time.Duration
}

func New(user, password string) *Client { return NewAt(Endpoint, user, password) }

// NewAt calls the webservice at base, which only a test's fake sets.
func NewAt(base, user, password string) *Client {
	return &Client{base: base, user: user, password: password, http: &http.Client{Timeout: timeout}, PollInterval: 10 * time.Second}
}

type Server struct {
	Number int64  `json:"server_number"`
	Name   string `json:"server_name"`
	IP     string `json:"server_ip"`
	// DC is the datacenter as Robot spells it, such as HEL1-DC1.
	DC        string `json:"dc"`
	Product   string `json:"product"`
	Status    string `json:"status"`
	Cancelled bool   `json:"cancelled"`
}

// Location is the Hetzner location the datacenter lies in, such as hel1.
func (s Server) Location() string {
	prefix, _, _ := strings.Cut(s.DC, "-")
	return strings.ToLower(prefix)
}

type Key struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
}

// Rescue carries the host key fingerprints Robot returned for the rescue system, which may be none.
type Rescue struct {
	Active   bool
	HostKeys []string
}

type Firewall struct {
	Status       string
	FilterIPv6   bool
	WhitelistHOS bool
	Input        []Rule
	Output       []Rule
}

// Rule leaves an empty field out, which Robot reads as a wildcard.
type Rule struct {
	Name, IPVersion, SrcIP, DstIP, Protocol, SrcPort, DstPort, TCPFlags, Action string
}

// VSwitch carries its servers only when read by id.
type VSwitch struct {
	ID            int64
	Name          string
	VLAN          int
	Cancelled     bool
	Servers       []VSwitchServer
	CloudNetworks []int64
}

type VSwitchServer struct {
	Number int64
	Status string
}

// Error is an error answer of the webservice, such as 404 SERVER_NOT_FOUND.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("Robot answered %d %s: %s", e.Status, e.Code, e.Message)
}

// ErrorCode is the code of a Robot error answer within err, or "".
func ErrorCode(err error) string {
	var re *Error
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

// do sends form form-encoded, as the webservice requires, and decodes the answer into out when out is not nil.
func (c *Client) do(ctx context.Context, method, path string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return fmt.Errorf("building the Robot request %s %s: %w", method, path, err)
	}
	req.SetBasicAuth(c.user, c.password)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("calling Robot %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	if err != nil {
		return fmt.Errorf("reading Robot's answer to %s %s: %w", method, path, err)
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
		return fmt.Errorf("decoding Robot's answer to %s %s: %w", method, path, err)
	}
	return nil
}

func numbered(prefix string, n int64) string { return prefix + strconv.FormatInt(n, 10) }

// wait reads until done holds or ctx ends, sleeping PollInterval between reads.
func (c *Client) wait(ctx context.Context, what string, done func() (bool, error)) error {
	for {
		ok, err := done()
		if err != nil || ok {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s: %w", what, ctx.Err())
		case <-time.After(c.PollInterval):
		}
	}
}

func (c *Client) GetServer(ctx context.Context, number int64) (Server, error) {
	var answer struct {
		Server Server `json:"server"`
	}
	err := c.do(ctx, http.MethodGet, numbered("/server/", number), nil, &answer)
	return answer.Server, err
}

func (c *Client) SetServerName(ctx context.Context, number int64, name string) error {
	return c.do(ctx, http.MethodPost, numbered("/server/", number), url.Values{"server_name": {name}}, nil)
}

func (c *Client) ListKeys(ctx context.Context) ([]Key, error) {
	var answer []struct {
		Key Key `json:"key"`
	}
	err := c.do(ctx, http.MethodGet, "/key", nil, &answer)
	if ErrorCode(err) == "NOT_FOUND" {
		return nil, nil
	}
	keys := make([]Key, 0, len(answer))
	for _, a := range answer {
		keys = append(keys, a.Key)
	}
	return keys, err
}

func (c *Client) CreateKey(ctx context.Context, name, publicKey string) (Key, error) {
	var answer struct {
		Key Key `json:"key"`
	}
	err := c.do(ctx, http.MethodPost, "/key", url.Values{"name": {name}, "data": {publicKey}}, &answer)
	return answer.Key, err
}

// DeleteKey treats a key Robot no longer holds as deleted.
func (c *Client) DeleteKey(ctx context.Context, fingerprint string) error {
	err := c.do(ctx, http.MethodDelete, "/key/"+url.PathEscape(fingerprint), nil, nil)
	if ErrorCode(err) == "NOT_FOUND" {
		return nil
	}
	return err
}

func (c *Client) ActivateRescue(ctx context.Context, number int64, keyFingerprint string) (Rescue, error) {
	var answer struct {
		Rescue struct {
			Active  bool `json:"active"`
			HostKey []struct {
				Key struct {
					Fingerprint string `json:"fingerprint"`
				} `json:"key"`
			} `json:"host_key"`
		} `json:"rescue"`
	}
	form := url.Values{"os": {rescueOS}, "authorized_key[]": {keyFingerprint}}
	if err := c.do(ctx, http.MethodPost, numbered("/boot/", number)+"/rescue", form, &answer); err != nil {
		return Rescue{}, err
	}
	out := Rescue{Active: answer.Rescue.Active}
	for _, hk := range answer.Rescue.HostKey {
		if hk.Key.Fingerprint != "" {
			out.HostKeys = append(out.HostKeys, hk.Key.Fingerprint)
		}
	}
	return out, nil
}

// DeactivateRescue treats Robot's BOOT_NOT_AVAILABLE, no activation to clear, as done.
func (c *Client) DeactivateRescue(ctx context.Context, number int64) error {
	err := c.do(ctx, http.MethodDelete, numbered("/boot/", number)+"/rescue", nil, nil)
	if ErrorCode(err) == "BOOT_NOT_AVAILABLE" {
		return nil
	}
	return err
}

func (c *Client) Reset(ctx context.Context, number int64) error {
	return c.do(ctx, http.MethodPost, numbered("/reset/", number), url.Values{"type": {resetType}}, nil)
}

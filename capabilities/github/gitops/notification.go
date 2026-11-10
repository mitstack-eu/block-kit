package gitops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mitstack-eu/block-kit/helpers/bounded"
	"github.com/mitstack-eu/block-kit/helpers/events"
)

// Notification is one Argo CD notification of an Application's sync, which asks a block for a fresh verdict and fails it only alongside the objects it owns.
type Notification struct {
	App          string `json:"app"`
	Project      string `json:"project"`
	Sync         string `json:"sync"`
	Revision     string `json:"revision"`
	Health       string `json:"health"`
	Phase        string `json:"phase"`
	Message      string `json:"message"`
	ReconciledAt string `json:"reconciledAt"`
}

type Verdict string

const (
	VerdictPending   Verdict = "pending"
	VerdictSucceeded Verdict = "succeeded"
	VerdictFailed    Verdict = "failed"
)

type Notifications interface {
	// Revision "" asks for the newest notification at any revision.
	Latest(ctx context.Context, app, revision string) (Notification, bool, error)
}

const notificationsPath = "/v1/gitops-notifications"

const maxNotificationBody = 64 << 10

var notificationsClient = &http.Client{Timeout: 10 * time.Second}

// A nil HTTPClient uses a short-timeout default, and no error it returns carries Token.
type PlatformNotifications struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

// A 404 is none yet, and any status but 200 an error.
func (p PlatformNotifications) Latest(ctx context.Context, app, revision string) (Notification, bool, error) {
	query := url.Values{"app": {app}}
	if revision != "" {
		query.Set("revision", revision)
	}
	target := strings.TrimSuffix(p.BaseURL, "/") + notificationsPath + "?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Notification{}, false, fmt.Errorf("gitops: building the notification request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	client := p.HTTPClient
	if client == nil {
		client = notificationsClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return Notification{}, false, fmt.Errorf("gitops: reading the notification for %s: %w", app, err)
	}
	defer resp.Body.Close()
	body, err := bounded.Read(resp, "the notification for "+app, maxNotificationBody)
	if err != nil {
		return Notification{}, false, fmt.Errorf("gitops: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		var n Notification
		if err := json.Unmarshal(body, &n); err != nil {
			return Notification{}, false, fmt.Errorf("gitops: decoding the notification for %s: %w", app, err)
		}
		return n, true, nil
	case http.StatusNotFound:
		return Notification{}, false, nil
	default:
		return Notification{}, false, fmt.Errorf("gitops: the notification for %s answered status %d: %s",
			app, resp.StatusCode, p.scrub(strings.TrimSpace(string(body))))
	}
}

func (p PlatformNotifications) scrub(text string) string {
	if p.Token == "" {
		return text
	}
	return strings.ReplaceAll(text, p.Token, "[REDACTED]")
}

// NotificationsFromContext reads the platform the request's events callback names, with the callback's token.
func NotificationsFromContext(ctx context.Context) (PlatformNotifications, error) {
	base, token, err := events.PlatformFrom(ctx)
	if err != nil {
		return PlatformNotifications{}, fmt.Errorf("gitops: %w", err)
	}
	return PlatformNotifications{BaseURL: base, Token: token}, nil
}

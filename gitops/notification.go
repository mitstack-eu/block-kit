package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mitstack-eu/operator-kit/events"
)

// Notification is the body Argo CD's notification controller sends when an
// application's sync completes, as the platform relays it. Sync is carried
// through but never read, because the outcome is decided by Phase and Health
// alone. ReconciledAt is the RFC 3339 time Argo CD sent, kept as text.
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

// Verdict is what a Notification says about the deploy of one revision.
type Verdict string

// The three verdicts Judge returns.
const (
	// VerdictPending means the deploy has not finished, or the notification
	// is about another revision.
	VerdictPending Verdict = "pending"
	// VerdictSucceeded means the revision synced with phase Succeeded and
	// health Healthy.
	VerdictSucceeded Verdict = "succeeded"
	// VerdictFailed means the revision's sync ended in phase Error or
	// Failed, or its resources are Degraded.
	VerdictFailed Verdict = "failed"
)

// Judge returns the verdict n gives on the deploy of revision. A
// notification for any other revision, or for none, is pending whatever it
// says. For revision itself, phase Succeeded with health Healthy is a
// success, phase Error or Failed or health Degraded is a failure, and
// anything else is pending.
func Judge(n Notification, revision string) Verdict {
	if revision == "" || n.Revision != revision {
		return VerdictPending
	}
	switch {
	case n.Phase == "Succeeded" && n.Health == "Healthy":
		return VerdictSucceeded
	case n.Phase == "Error" || n.Phase == "Failed" || n.Health == "Degraded":
		return VerdictFailed
	default:
		return VerdictPending
	}
}

// Notifications is a source of Argo CD's deploy notifications.
type Notifications interface {
	// Latest returns the newest notification for app at revision, and false
	// when there is none yet. Revision "" asks for app's newest
	// notification whatever its revision.
	Latest(ctx context.Context, app, revision string) (Notification, bool, error)
}

// Outcome is a verdict together with the notification it was judged from,
// which is the zero Notification when none was read.
type Outcome struct {
	Verdict      Verdict
	Notification Notification
}

// Check reads app's notification for revision once and judges it. No
// notification yet is pending. A source error is returned with a pending
// outcome, and so is an empty revision, which no notification can match.
func Check(ctx context.Context, src Notifications, app, revision string) (Outcome, error) {
	pending := Outcome{Verdict: VerdictPending}
	if revision == "" {
		return pending, errors.New("gitops: no revision to judge")
	}
	n, ok, err := src.Latest(ctx, app, revision)
	if err != nil {
		return pending, err
	}
	if !ok {
		return pending, nil
	}
	return Outcome{Verdict: Judge(n, revision), Notification: n}, nil
}

// Wait checks app's notification for revision every poll until the verdict
// is succeeded or failed, and returns that outcome. A failed deploy is an
// outcome and not an error. When ctx ends first it returns the last pending
// outcome with ctx's error, and a source error ends the wait at once.
func Wait(ctx context.Context, src Notifications, app, revision string, poll time.Duration) (Outcome, error) {
	if poll <= 0 {
		return Outcome{Verdict: VerdictPending}, errors.New("gitops: the poll interval must be positive")
	}
	last := Outcome{Verdict: VerdictPending}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-timer.C:
		}
		out, err := Check(ctx, src, app, revision)
		if err != nil || out.Verdict != VerdictPending {
			return out, err
		}
		last = out
		timer.Reset(poll)
	}
}

// notificationsPath is the platform route that serves the notifications it
// relayed from Argo CD.
const notificationsPath = "/v1/gitops-notifications"

// operatorEventsPath is the path of the events callback URL the platform
// hands every request. The platform's base URL is that URL without it.
const operatorEventsPath = "/v1/webhooks/operator-events"

// maxNotificationBody bounds how much of a response PlatformNotifications
// reads.
const maxNotificationBody = 64 << 10

// notificationsClient is the client a PlatformNotifications with no
// HTTPClient uses. Its short timeout keeps a slow platform from holding a
// lifecycle call for long.
var notificationsClient = &http.Client{Timeout: 10 * time.Second}

// PlatformNotifications reads the notifications the platform relayed from
// Argo CD, over GET /v1/gitops-notifications with the bearer Token. A nil
// HTTPClient uses a default with a short timeout. No error it returns
// carries Token, even when the response body echoes it.
type PlatformNotifications struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

// Latest asks the platform for app's notification at revision, or for app's
// newest notification when revision is "", in which case the request
// carries no revision parameter. A 404 means there is none yet, and any
// status but 200 or 404 is an error carrying the status and the response
// body with the token scrubbed.
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxNotificationBody))
	if err != nil {
		return Notification{}, false, fmt.Errorf("gitops: reading the notification for %s: %w", app, err)
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

// scrub replaces every occurrence of the bearer token in text.
func (p PlatformNotifications) scrub(text string) string {
	if p.Token == "" {
		return text
	}
	return strings.ReplaceAll(text, p.Token, "[REDACTED]")
}

// NotificationsFromContext builds the platform reader from the events
// callback the handler stashed on ctx: its base URL is the callback URL
// without /v1/webhooks/operator-events, and its token is the callback's. It
// is an error when ctx carries no callback, or when the callback URL does
// not end in that path. No error it returns carries the token.
func NotificationsFromContext(ctx context.Context) (PlatformNotifications, error) {
	env, ok := events.EnvelopeFrom(ctx)
	if !ok || env.Callback == nil {
		return PlatformNotifications{}, errors.New("gitops: the request carries no events callback to reach the platform through")
	}
	base, found := strings.CutSuffix(env.Callback.URL, operatorEventsPath)
	if !found {
		return PlatformNotifications{}, fmt.Errorf("gitops: the events callback URL does not end in %s", operatorEventsPath)
	}
	return PlatformNotifications{BaseURL: base, Token: env.Callback.Token}, nil
}

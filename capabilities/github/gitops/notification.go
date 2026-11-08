package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mitstack-eu/block-kit/helpers/bounded"
	"github.com/mitstack-eu/block-kit/helpers/events"
)

// Only Phase and Health decide a deploy.
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

// Phase Succeeded with health Healthy succeeds, phase Error or Failed or health Degraded fails, and anything else is pending.
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

type Notifications interface {
	// Revision "" asks for the newest notification at any revision.
	Latest(ctx context.Context, app, revision string) (Notification, bool, error)
}

// Outcome is the verdict and the notification it was read from, at Revision or at a later revision holding the block's files as Revision does.
type Outcome struct {
	Verdict      Verdict
	Notification Notification
	// Revision is the one handed to Check when Argo CD reported on it, and otherwise the block's own commit at or before it.
	Revision string
	// Detail names the commit and file that keep a later revision Argo CD reports from settling the block.
	Detail string
}

// Check judges Argo CD's verdict on the block's own commit, or on a later revision in which no commit changed one of paths, and errs on an empty revision.
func Check(ctx context.Context, src Notifications, repo Repository, app, revision string, paths []string) (Outcome, error) {
	pending := Outcome{Verdict: VerdictPending, Revision: revision}
	if revision == "" {
		return pending, errors.New("gitops: no revision to judge")
	}
	if out, ok, err := checkAt(ctx, src, app, revision); ok || err != nil {
		return out, err
	}
	head, err := repo.Head(ctx)
	if err != nil {
		return pending, fmt.Errorf("gitops: reading head: %w", err)
	}
	line, err := walkLineage(ctx, repo, head, revision, paths)
	if err != nil {
		return pending, err
	}
	own := line.ownRevision(revision)
	pending.Revision = own
	if own != revision {
		if out, ok, err := checkAt(ctx, src, app, own); ok || err != nil {
			return out, err
		}
	}
	newest, ok, err := src.Latest(ctx, app, "")
	if err != nil || !ok {
		return pending, err
	}
	at := line.at(newest.Revision)
	if at < 0 {
		return pending, nil
	}
	if moved, path := line.changedAfterOwn(at); moved != "" {
		pending.Detail = fmt.Sprintf("Argo CD reports %s at %s, but commit %s after the block's own %s changed %s", app, newest.Revision, moved, own, path)
		return pending, nil
	}
	return Outcome{Verdict: Judge(newest, newest.Revision), Notification: newest, Revision: own}, nil
}

func checkAt(ctx context.Context, src Notifications, app, revision string) (Outcome, bool, error) {
	n, ok, err := src.Latest(ctx, app, revision)
	if err != nil || !ok {
		return Outcome{Verdict: VerdictPending, Revision: revision}, false, err
	}
	return Outcome{Verdict: Judge(n, revision), Notification: n, Revision: revision}, true, nil
}

// A failed deploy is an outcome, not an error.
func Wait(ctx context.Context, src Notifications, repo Repository, app, revision string, paths []string, poll time.Duration) (Outcome, error) {
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
		out, err := Check(ctx, src, repo, app, revision, paths)
		if err != nil || out.Verdict != VerdictPending {
			return out, err
		}
		last = out
		timer.Reset(poll)
	}
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

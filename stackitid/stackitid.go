// Package stackitid exchanges the platform-signed OIDC assertion for a
// short-lived STACKIT access token, under RFC 8693 OAuth 2.0 token exchange.
//
// The package is deliberately STACKIT-specific by design: it holds STACKIT's
// token-exchange shape and nothing that generalises to another cloud. It
// fails closed — the Refresher never serves a stale or already-expired
// access token, and no validation or failure path ever carries the assertion
// or the access token it protects.
package stackitid

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

	"github.com/mitstack-eu/operator-kit/credrefresh"
)

// grantType is the RFC 8693 token-exchange grant.
const grantType = "urn:ietf:params:oauth:grant-type:token-exchange"

// subjectTokenType names the assertion as a JWT, per RFC 8693.
const subjectTokenType = "urn:ietf:params:oauth:token-type:jwt"

// maxResponseBody bounds how much of the token endpoint's response is read,
// so a misbehaving or hostile endpoint cannot exhaust memory.
const maxResponseBody = 1 << 20 // 1 MiB

// Identity is what Resolve needs to exchange the platform's assertion for a
// STACKIT access token.
type Identity struct {
	// TokenURL is STACKIT's token endpoint. Required, absolute, and https —
	// there is no package default, and no plaintext exception, because the
	// assertion is a bearer credential presented in the request body.
	TokenURL string
	// Audience is the RFC 8693 audience the assertion is exchanged for.
	// Required.
	Audience string
	// Assertion is the platform-signed OIDC assertion (the RFC 8693
	// subject_token). Required. Never logged, never placed in a URL or an
	// error.
	Assertion string
	// Scope is passed through when non-empty. Optional.
	Scope string
	// Client is the injectable HTTP seam. A nil Client means
	// http.DefaultClient.
	Client *http.Client
}

// Credential is the access token resulting from the exchange.
type Credential struct {
	AccessToken string
	TokenType   string
	// Expires is when the access token stops working. A ZERO value means
	// unknown, and is treated as unusable-for-caching — never as "no expiry".
	Expires time.Time
}

// tokenResponse is the RFC 8693 JSON success body.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

// errorResponse is the RFC 6749 JSON error body.
type errorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// ErrInvalidGrant names a rejected assertion, so an operator can map it to a
// statusDetail instead of surfacing an opaque failure.
var ErrInvalidGrant = errors.New("stackitid: assertion rejected (invalid_grant)")

// Resolve exchanges id's assertion for a STACKIT access token.
//
// Validation runs before any network attempt, and no validation or failure
// message carries the assertion or the resulting access token.
func Resolve(ctx context.Context, id Identity) (Credential, error) {
	if strings.TrimSpace(id.TokenURL) == "" {
		return Credential{}, errors.New("stackitid: TokenURL is required")
	}
	if strings.TrimSpace(id.Audience) == "" {
		return Credential{}, errors.New("stackitid: Audience is required")
	}
	if strings.TrimSpace(id.Assertion) == "" {
		return Credential{}, errors.New("stackitid: Assertion is required")
	}

	parsed, err := url.Parse(id.TokenURL)
	if err != nil {
		return Credential{}, fmt.Errorf("stackitid: parsing TokenURL: %w", err)
	}
	if parsed.Scheme != "https" {
		return Credential{}, errors.New("stackitid: TokenURL must use https")
	}

	form := url.Values{
		"grant_type":         {grantType},
		"subject_token":      {id.Assertion},
		"subject_token_type": {subjectTokenType},
		"audience":           {id.Audience},
	}
	if id.Scope != "" {
		form.Set("scope", id.Scope)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, id.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Credential{}, fmt.Errorf("stackitid: building token exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := id.Client
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		// The underlying transport error's text is never surfaced: it can
		// come from arbitrary code below net/http and has no guarantee of
		// being free of request contents. ctx.Err() alone is safe to wrap,
		// so callers doing errors.Is(err, context.Canceled) still work.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Credential{}, fmt.Errorf("stackitid: token exchange request: %w", ctxErr)
		}
		return Credential{}, errors.New("stackitid: token exchange request failed")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return Credential{}, fmt.Errorf("stackitid: reading token exchange response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errBody errorResponse
		if jsonErr := json.Unmarshal(body, &errBody); jsonErr == nil && errBody.Error != "" {
			if errBody.Error == "invalid_grant" {
				return Credential{}, fmt.Errorf("%w: %s", ErrInvalidGrant, errBody.ErrorDescription)
			}
			// Any other RFC 6749 error code the endpoint names is still the
			// most actionable thing available, so it is surfaced even though
			// it has no dedicated sentinel.
			return Credential{}, fmt.Errorf("stackitid: token exchange failed: %s: %s", errBody.Error, errBody.ErrorDescription)
		}
		return Credential{}, fmt.Errorf("stackitid: token exchange failed with status %d", resp.StatusCode)
	}

	var got tokenResponse
	if err := json.Unmarshal(body, &got); err != nil {
		return Credential{}, fmt.Errorf("stackitid: parsing token exchange response: %w", err)
	}
	if got.AccessToken == "" {
		// A malformed or truncated response could otherwise be cached and
		// served as a usable credential by the Refresher for the full
		// renewal window, rather than causing every call to re-resolve.
		return Credential{}, errors.New("stackitid: token exchange returned an incomplete credential")
	}

	cred := Credential{
		AccessToken: got.AccessToken,
		TokenType:   got.TokenType,
	}
	if got.ExpiresIn > 0 {
		cred.Expires = time.Now().Add(time.Duration(got.ExpiresIn) * time.Second)
	}
	return cred, nil
}

// Refresher holds a STACKIT access token across work that outlives a single
// exchange, and fails closed.
//
// It is a thin adapter over credrefresh, which owns the renewal mechanics —
// this type only knows how to turn a Credential into the (value, expiry,
// error) shape that package generalises over.
type Refresher struct {
	// Now is injectable so the expiry behaviour is testable without
	// sleeping.
	Now func() time.Time

	inner *credrefresh.Refresher[Credential]
}

// NewRefresher returns a Refresher that mints access tokens through resolve.
func NewRefresher(resolve func(context.Context) (Credential, error)) *Refresher {
	r := &Refresher{}
	r.inner = credrefresh.New(func(ctx context.Context) (Credential, time.Time, error) {
		c, err := resolve(ctx)
		return c, c.Expires, err
	})
	// r.Now is read through this closure on every inner Get call, so setting
	// r.Now after construction — or between calls — still reaches it.
	r.inner.Now = func() time.Time {
		if r.Now != nil {
			return r.Now()
		}
		return time.Now()
	}
	return r
}

// FromIdentity returns a Refresher that re-resolves the given identity.
func FromIdentity(id Identity) *Refresher {
	return NewRefresher(func(ctx context.Context) (Credential, error) { return Resolve(ctx, id) })
}

// Get returns a usable access token, renewing when it is absent, inside the
// renewal margin, or of unknown expiry. It never returns an expired token.
func (r *Refresher) Get(ctx context.Context) (Credential, error) {
	cred, err := r.inner.Get(ctx)
	if err != nil {
		return Credential{}, fmt.Errorf("stackitid: refreshing access token (failing closed, no stale credential returned): %w", err)
	}
	return cred, nil
}

// Package handler serves the four lifecycle verbs — subscribe, upgrade,
// validate, unsubscribe — over a block author's Resource, so a block author
// never reimplements (or quietly omits) the invariants that make an operator
// safe to run: the mitstack trust boundary, idempotent replay, the settle
// loop that waits for a resource to actually converge, the absent marker,
// and the error classification a refused teardown depends on (MIT-2659).
//
// A block author supplies a Resource and, optionally, Hooks declared in the
// block's own manifest.yaml. Every other invariant — idempotency, the settle
// budget, the absent marker, the error classes — belongs to the Handler, not
// to a knob a block can override. A hook the manifest declares but that
// nothing registers is accepted by New and simply never called.
package handler

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/webhook"
)

// maxHookBodyBytes bounds how much of a POST /hook body the handler will
// read, so a misbehaving or hostile caller cannot exhaust memory before the
// trust boundary ever runs.
const maxHookBodyBytes = 1 << 20 // 1 MiB

// defaultVerbBudget is how many times the handler polls Observe, for
// subscribe/upgrade/unsubscribe, before giving up and answering pending or a
// definite absent:false.
const defaultVerbBudget = 20

// defaultPollInterval is how long the handler waits between Observe polls.
const defaultPollInterval = 500 * time.Millisecond

// Resource is everything a block author implements: converge a subscription
// to its desired state, observe what is actually there, and tear it down.
// The Handler owns every lifecycle invariant around these three calls, so a
// Resource implementation is exactly as much surface as a block needs and no
// more.
type Resource interface {
	// Ensure converges the resource identified by d.Identity to d. It may be
	// called more than once for the same subscription (a retried subscribe,
	// or an upgrade), and must converge rather than duplicate.
	Ensure(ctx context.Context, d contract.Desired) error

	// Observe reports what the resource identified by id actually is right
	// now. The Handler polls this after Ensure and after Delete to settle a
	// verb, and calls it once, unpolled, for validate.
	Observe(ctx context.Context, id contract.Identity) (contract.Observation, error)

	// Delete tears the resource down. Absence is success: Delete must not
	// error when nothing was ever provisioned.
	Delete(ctx context.Context, id contract.Identity) error
}

// Hook is a declared extension point a block registers at one of the four
// HookXxx points. One signature serves all four: an ensure-side hook and a
// delete-side hook both receive a Desired, the delete-side value carrying
// only its Identity, since tearing a subscription down needs no desired
// params.
type Hook func(ctx context.Context, d contract.Desired) error

// The closed set of hook points the Handler calls. A hook registered under
// any other name is refused by New — a block author extends the lifecycle
// only at these four points, never invents a fifth.
const (
	// HookBeforeEnsure runs immediately before Ensure, for subscribe and
	// upgrade alike.
	HookBeforeEnsure = "beforeEnsure"
	// HookAfterEnsure runs immediately after Ensure succeeds, before the
	// settle loop.
	HookAfterEnsure = "afterEnsure"
	// HookBeforeDelete runs immediately before Delete, for unsubscribe.
	HookBeforeDelete = "beforeDelete"
	// HookAfterDelete runs immediately after Delete succeeds, before the
	// settle loop.
	HookAfterDelete = "afterDelete"
)

// Config wires a Handler. Resource and JWKSURL are the only fields a block
// author must always set; everything else defaults to a safe, hermetic
// choice.
type Config struct {
	// Resource is the block's own implementation of the three lifecycle
	// calls. Required.
	Resource Resource

	// Manifest is the block's manifest.yaml. Required when Hooks is
	// non-empty, since a hook may only be registered if the manifest
	// declares it under spec.lifecycle.hooks.
	Manifest []byte

	// Hooks registers a block's extension points by HookXxx name. Optional;
	// a hook point with no registered Hook is simply never called.
	Hooks map[string]Hook

	// JWKSURL is mitstack's JWKS endpoint the trust boundary resolves the
	// platform signing key from. Required.
	JWKSURL string

	// PublicKey resolves a signing key by kid, over the keys JWKSURL serves.
	// Optional; defaults to a kit KeyCache over JWKSURL. It is not an
	// alternative to JWKSURL: webhook.AuthenticateHook rejects every request
	// when JWKSURL is empty before it ever consults PublicKey.
	PublicKey func(kid string) (ed25519.PublicKey, error)

	// Replay is the nonce/skew guard for POST /hook. Optional; defaults to
	// webhook.NewReplay().
	Replay *webhook.Replay

	// Logs serves GET /logs when set. When nil, GET /logs answers 404.
	Logs *webhook.LogBuffer

	// VerbBudget caps how many times the Handler calls Observe while
	// settling subscribe, upgrade or unsubscribe. Optional; defaults to 20.
	VerbBudget int

	// PollInterval is how long the Handler waits between Observe attempts
	// while settling. Optional; defaults to 500ms.
	PollInterval time.Duration

	// Sleep is called between settle attempts. Optional; a test supplies a
	// no-op so it never waits on wall-clock time.
	Sleep func(ctx context.Context, d time.Duration)

	// Now supplies the current time. Optional; defaults to time.Now.
	Now func() time.Time
}

// Handler serves the four lifecycle verbs over a Resource. Every field is
// unexported: the invariants a Handler enforces are not something a caller
// reaches in and adjusts after New returns.
type Handler struct {
	resource Resource
	hooks    map[string]Hook

	jwksURL   string
	publicKey func(kid string) (ed25519.PublicKey, error)
	replay    *webhook.Replay
	logs      *webhook.LogBuffer

	verbBudget   int
	pollInterval time.Duration
	sleep        func(ctx context.Context, d time.Duration)
	now          func() time.Time

	idempotency *idempotencyCache
}

// New builds a Handler from cfg, applying every default the CONTRACT pins
// and refusing a configuration that would let a block override an
// invariant: a nil Resource, an empty JWKSURL (webhook.AuthenticateHook
// rejects every request on that before it ever consults PublicKey, so a
// resolver configured without a JWKSURL would build and then answer 401 to
// everything), a hook registered under an unknown name, a hook not declared
// in the manifest, or Hooks set with no Manifest to check it against.
func New(cfg Config) (*Handler, error) {
	if cfg.Resource == nil {
		return nil, errNilResource
	}
	if cfg.JWKSURL == "" {
		return nil, errEmptyJWKSURL
	}
	if err := validateHooks(cfg); err != nil {
		return nil, err
	}

	hooks := cfg.Hooks
	if hooks == nil {
		hooks = map[string]Hook{}
	}

	h := &Handler{
		resource:     cfg.Resource,
		hooks:        hooks,
		jwksURL:      cfg.JWKSURL,
		publicKey:    cfg.PublicKey,
		replay:       cfg.Replay,
		logs:         cfg.Logs,
		verbBudget:   cfg.VerbBudget,
		pollInterval: cfg.PollInterval,
		sleep:        cfg.Sleep,
		now:          cfg.Now,
		idempotency:  newIdempotencyCache(),
	}

	if h.verbBudget <= 0 {
		h.verbBudget = defaultVerbBudget
	}
	if h.pollInterval <= 0 {
		h.pollInterval = defaultPollInterval
	}
	if h.sleep == nil {
		h.sleep = defaultSleep
	}
	if h.now == nil {
		h.now = time.Now
	}
	if h.replay == nil {
		h.replay = webhook.NewReplay()
	}
	if h.publicKey == nil {
		cache := webhook.NewKeyCache()
		jwksURL := h.jwksURL
		h.publicKey = func(kid string) (ed25519.PublicKey, error) {
			return cache.PublicKey(jwksURL, nil, kid)
		}
	}

	return h, nil
}

// defaultSleep waits d, or until ctx is done, whichever comes first — the
// real-clock Sleep every Config gets unless a test overrides it.
func defaultSleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// Serve routes POST /hook and GET /logs. Any other path is 404; a known path
// with the wrong method is 405.
func (h *Handler) Serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/hook":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.serveHook(w, r)
	case "/logs":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.serveLogs(w, r)
	default:
		http.NotFound(w, r)
	}
}

// ServeHTTP makes Handler an http.Handler. It is not a second
// implementation: it calls Serve.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.Serve(w, r) }

// webhookDeps builds the webhook.Deps the trust-boundary functions need,
// from this Handler's own configuration.
func (h *Handler) webhookDeps() webhook.Deps {
	return webhook.Deps{
		JWKSURL:   h.jwksURL,
		PublicKey: h.publicKey,
		Replay:    h.replay,
		Now:       h.now,
	}
}

// serveLogs answers GET /logs: 404 when Config.Logs was never set, otherwise
// delegates to webhook.ServeLogs so the reviewed trust boundary — not a
// second copy of it — guards a block's log output.
func (h *Handler) serveLogs(w http.ResponseWriter, r *http.Request) {
	if h.logs == nil {
		http.NotFound(w, r)
		return
	}
	webhook.ServeLogs(h.webhookDeps(), h.logs, w, r)
}

// serveHook is the POST /hook pipeline: read the bounded body, authenticate
// it, decode the envelope, verify an OBO token when present, then dispatch
// the verb — in that order, so a request that fails any earlier step never
// reaches the Resource.
func (h *Handler) serveHook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHookBodyBytes+1))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "cannot read request body: "+err.Error())
		return
	}
	if len(body) > maxHookBodyBytes {
		h.writeError(w, http.StatusBadRequest, "request body exceeds the maximum allowed size")
		return
	}

	pub, ok := webhook.AuthenticateHook(h.webhookDeps(), w, r, body)
	if !ok {
		return // AuthenticateHook already wrote the 401.
	}

	req, err := contract.DecodeRequest(body)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.OBO != "" {
		if _, err := webhook.VerifyOBO(pub, req.OBO, h.now()); err != nil {
			h.writeError(w, http.StatusUnauthorized, "invalid OBO token: "+err.Error())
			return
		}
	}

	h.dispatchRequest(w, r, req)
}

// writeError writes a minimal contract.Response carrying msg as the error,
// for a failure the trust boundary or the envelope decoder produced before
// any verb ever ran.
func (h *Handler) writeError(w http.ResponseWriter, status int, msg string) {
	writeResponse(w, status, contract.Response{Error: msg})
}

// writeResponse marshals resp and writes it with status, the one place a
// response body leaves this package on the success path.
func writeResponse(w http.ResponseWriter, status int, resp contract.Response) {
	raw, err := json.Marshal(resp)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"handler: encoding response failed"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

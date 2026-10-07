// Package handler serves the four lifecycle verbs over a block author's Resource, owning every invariant a block must not override.
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

// maxHookBodyBytes bounds the read so a hostile caller cannot exhaust memory before the trust boundary runs.
const maxHookBodyBytes = 1 << 20 // 1 MiB

// defaultVerbBudget is how many times Observe is polled before answering pending or a definite absent:false.
const defaultVerbBudget = 20

const defaultPollInterval = 500 * time.Millisecond

// Resource is everything a block author implements, around which the Handler owns every lifecycle invariant.
type Resource interface {
	// Ensure may be called more than once for the same subscription and must converge rather than duplicate.
	Ensure(ctx context.Context, d contract.Desired) error

	// Observe is polled after Ensure and Delete and called once for validate; id carries the instance config so a region-scoped block addresses the resource where it lives.
	Observe(ctx context.Context, id contract.Identity) (contract.Observation, error)

	// Delete must not error when nothing was ever provisioned.
	Delete(ctx context.Context, id contract.Identity) error
}

// Observer reports what one declared resource actually is, from the provider and never from what an ensure remembered.
type Observer interface {
	ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error)
}

// ResourceReporter, when a block-level Resource implements it, replaces Ensure with EnsureResources, and a reported resource the manifest does not declare is refused with a 400.
type ResourceReporter interface {
	EnsureResources(ctx context.Context, d contract.Desired) ([]contract.CreatedResource, error)
}

// ReplacementPlanner names, on a validate, the declared resources an upgrade would replace, and a name the manifest does not declare is refused with a 400.
type ReplacementPlanner interface {
	PlanReplacements(ctx context.Context, d contract.Desired) ([]string, error)
}

// DeclaredResource is what a block implements once for each resource its manifest declares, ensured in creation order and deleted in reverse.
//
// Deprecated: a block serves its declared resources through its block-level
// Resource and an Observer for each, under Config.Observers; the next major
// release removes DeclaredResource and Config.Resources.
type DeclaredResource interface {
	Ensure(ctx context.Context, d contract.Desired) error

	Observe(ctx context.Context, id contract.Identity) (contract.Observation, error)

	// Delete must not error when the resource was never created.
	Delete(ctx context.Context, id contract.Identity) error
}

// ReportingResource replaces Ensure with EnsureWithReport, and an identity or output value equal to a request secret is refused as secret-in-output.
//
// Deprecated: it serves a DeclaredResource alone; a block-level Resource
// reports what it created through ResourceReporter. The next major release
// removes it.
type ReportingResource interface {
	EnsureWithReport(ctx context.Context, d contract.Desired) (contract.Ensured, error)
}

// RequestDeleter replaces Delete with a call carrying the verb's Desired, for a resource reachable only through recorded inputs and identity.
//
// A declared resource asserts var _ handler.RequestDeleter = (*T)(nil) because HandoverResource declares a DeleteWithRequest with a different signature, which would otherwise be silently deleted through Delete.
type RequestDeleter interface {
	// DeleteWithRequest must not error when the resource was never created.
	DeleteWithRequest(ctx context.Context, d contract.Desired) error
}

// Hook is an extension point registered at one of the four HookXxx points; the delete-side Desired carries only its Identity, whose instance config lets a hook reach a region-scoped API.
type Hook func(ctx context.Context, d contract.Desired) error

// The closed set of hook points the Handler calls; New refuses a hook registered under any other name.
const (
	HookBeforeEnsure = "beforeEnsure"
	// HookAfterEnsure runs after Ensure succeeds, before the settle loop.
	HookAfterEnsure  = "afterEnsure"
	HookBeforeDelete = "beforeDelete"
	// HookAfterDelete runs after Delete succeeds, before the settle loop.
	HookAfterDelete = "afterDelete"
)

// Config wires a Handler; only JWKSURL and exactly one of Resource and Resources are required.
type Config struct {
	// Resource is set instead of Resources, never both.
	Resource Resource

	// Observers needs one Observer for each declared resource, and is refused with Resources or without a mitstack/v1beta manifest.
	Observers map[string]Observer

	// Resources needs Manifest, refuses Hooks, and is set instead of Resource, never both.
	//
	// Deprecated: serve the block over Resource with an Observer for each
	// declared resource under Observers; the next major release removes
	// Resources, and with it a request running ensure or delete on one
	// named resource.
	Resources map[string]DeclaredResource

	// Manifest is required when Hooks or Resources is non-empty, since a hook or declared resource is registered only if the manifest declares it.
	Manifest []byte

	Hooks map[string]Hook

	JWKSURL string

	// PublicKey defaults to a kit KeyCache over JWKSURL and is no alternative to it, since webhook.AuthenticateHook rejects every request when JWKSURL is empty.
	PublicKey func(kid string) (ed25519.PublicKey, error)

	// Replay defaults to webhook.NewReplay().
	Replay *webhook.Replay

	// Logs serves GET /logs when set, and GET /logs answers 404 otherwise.
	Logs *webhook.LogBuffer

	// VerbBudget defaults to 20.
	VerbBudget int

	// PollInterval defaults to 500ms.
	PollInterval time.Duration

	// Sleep lets a test supply a no-op so it never waits on wall-clock time.
	Sleep func(ctx context.Context, d time.Duration)

	// Now defaults to time.Now.
	Now func() time.Time
}

// Handler serves the four lifecycle verbs over a Resource, with every field unexported so a caller cannot adjust its invariants after New.
type Handler struct {
	resource Resource
	hooks    map[string]Hook

	// declared is nil for a block served over a single Resource.
	declared   []namedResource
	blockLevel bool
	// provided names the resources spec.provides offers, whose outputs a block-level answer carries.
	provided map[string]bool
	// secretParams are scrubbed like any request secret when a resource input binds one.
	secretParams map[string]bool

	retainsOnUnsubscribe bool

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

// New builds a Handler from cfg and refuses a configuration that would let a block override an invariant; see declaredResources and observedResources.
func New(cfg Config) (*Handler, error) {
	if cfg.Resource != nil && len(cfg.Resources) > 0 {
		return nil, errResourceAndResources
	}
	if cfg.Resource == nil && len(cfg.Resources) == 0 {
		return nil, errNilResource
	}
	if cfg.JWKSURL == "" {
		return nil, errEmptyJWKSURL
	}
	declared, err := readManifestDeclarations(cfg.Manifest)
	if err != nil {
		return nil, err
	}
	var resources []namedResource
	blockLevel := false
	switch {
	case len(cfg.Resources) > 0 && len(cfg.Observers) > 0:
		err = errObserversAndResources
	case len(cfg.Resources) > 0:
		resources, err = declaredResources(cfg, declared)
	case declared.apiVersion == contract.ManifestAPIVersion:
		blockLevel = true
		resources, err = observedResources(cfg, declared)
	case len(cfg.Observers) > 0:
		err = errObserversWithoutManifest
	default:
		err = validateHooks(cfg, declared.hooks)
	}
	if err != nil {
		return nil, err
	}

	hooks := cfg.Hooks
	if hooks == nil {
		hooks = map[string]Hook{}
	}

	h := &Handler{
		resource:             cfg.Resource,
		hooks:                hooks,
		declared:             resources,
		blockLevel:           blockLevel,
		provided:             declared.provided,
		secretParams:         declared.secretParams,
		retainsOnUnsubscribe: declared.retainsOnUnsubscribe,
		jwksURL:              cfg.JWKSURL,
		publicKey:            cfg.PublicKey,
		replay:               cfg.Replay,
		logs:                 cfg.Logs,
		verbBudget:           cfg.VerbBudget,
		pollInterval:         cfg.PollInterval,
		sleep:                cfg.Sleep,
		now:                  cfg.Now,
		idempotency:          newIdempotencyCache(),
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

func defaultSleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// Serve routes POST /hook and GET /logs, answering 404 for any other path and 405 for the wrong method.
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

// ServeHTTP makes Handler an http.Handler by calling Serve.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.Serve(w, r) }

func (h *Handler) webhookDeps() webhook.Deps {
	return webhook.Deps{
		JWKSURL:   h.jwksURL,
		PublicKey: h.publicKey,
		Replay:    h.replay,
		Now:       h.now,
	}
}

// serveLogs delegates to webhook.ServeLogs so the reviewed trust boundary, not a second copy, guards the log output.
func (h *Handler) serveLogs(w http.ResponseWriter, r *http.Request) {
	if h.logs == nil {
		http.NotFound(w, r)
		return
	}
	webhook.ServeLogs(h.webhookDeps(), h.logs, w, r)
}

// serveHook runs its checks in order so a request failing any earlier one never reaches the Resource.
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
		return
	}

	req, err := contract.DecodeRequest(body)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.OBO == "" {
		h.writeError(w, http.StatusUnauthorized, "missing OBO token")
		return
	}

	if _, err := webhook.VerifyOBO(pub, req.OBO, h.now()); err != nil {
		h.writeError(w, http.StatusUnauthorized, "invalid OBO token: "+err.Error())
		return
	}

	h.dispatchRequest(w, r, req)
}

func (h *Handler) writeError(w http.ResponseWriter, status int, msg string) {
	writeResponse(w, status, contract.Response{Error: msg})
}

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

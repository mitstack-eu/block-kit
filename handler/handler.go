// Package handler, part of the core every block needs, serves the four lifecycle verbs over a block author's Resource, owning every invariant a block must not override.
package handler

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/observe"
	"github.com/mitstack-eu/block-kit/webhook"
)

// maxHookBodyBytes bounds the read so a hostile caller cannot exhaust memory before the trust boundary runs.
const maxHookBodyBytes = 1 << 20 // 1 MiB

type Resource interface {
	// Ensure may be called more than once for the same subscription and must converge rather than duplicate.
	Ensure(ctx context.Context, d contract.Desired) error

	// Delete must not error when nothing was ever provisioned and must be idempotent, since a settle timeout's redrive calls it again, and its outputs reach the unsubscribe answer unscrubbed, so they must never carry secret material.
	Delete(ctx context.Context, d contract.Desired) (outputs map[string]string, err error)
}

// Observer reports what one declared resource actually is, from the provider and never from what an ensure remembered.
type Observer interface {
	ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error)
}

// ResourceReporter, when a block-level Resource implements it, replaces Ensure with EnsureResources, which reports each resource once and each member of an entry marked many as <entry>/<key>, and any other name is refused with a 400.
type ResourceReporter interface {
	EnsureResources(ctx context.Context, d contract.Desired) ([]contract.CreatedResource, error)
}

// KeptReporter, when a block-level Resource implements it, replaces Delete with DeleteKeeping, whose kept files, keyed by the declared resource that landed them, answer on that resource's delete outcome; a name the manifest does not declare is refused with a 400.
type KeptReporter interface {
	DeleteKeeping(ctx context.Context, d contract.Desired) (outputs map[string]string, kept map[string][]contract.Kept, err error)
}

// TeardownReporter, when a block-level Resource implements it, is asked once an unsubscribe or a validate proves every resource absent, and a detail it returns answers as that absent answer's statusDetail, so a block can name what its teardown left alone.
type TeardownReporter interface {
	TeardownDetail(ctx context.Context, d contract.Desired) (string, error)
}

// ReplacementPlanner names, on a validate, the declared resources an upgrade would replace, and a name the manifest does not declare is refused with a 400.
type ReplacementPlanner interface {
	PlanReplacements(ctx context.Context, d contract.Desired) ([]string, error)
}

// FullReadChecker, when a block-level Resource implements it, is handed each full read in which the kit observer found a resource to be the subscription's, and a non-empty problem answers that resource unhealthy.
type FullReadChecker interface {
	CheckFullRead(ctx context.Context, r contract.ResourceRef, read observe.FullRead) (problem string, err error)
}

// Converger, when a block-level Resource implements it, is asked on a validate about work its EnsureResources left running: a non-empty detail answers every resource unknown with it, and finished answers redrive, so the engine sends the subscribe that reports what the work made; either way no observer runs.
type Converger interface {
	Converging(ctx context.Context, d contract.Desired) (detail string, finished bool, err error)
}

// CredentialIssuer, when a capability's block-level Resource implements it, answers an issue with the named app's credential and when it expires.
type CredentialIssuer interface {
	IssueCredential(ctx context.Context, d contract.Desired, appID string) (contract.Credential, error)
}

// Leaser, which a block-level Resource implements to serve a manifest that lists lease, mints a short-lived credential of one resource the block provides for the lease's caller and deletes the credentials a request lists in Retire.
type Leaser interface {
	Lease(ctx context.Context, d contract.Desired, l contract.LeaseRequest) (contract.Lease, error)
}

// Config wires a Handler; Resource, Manifest and JWKSURL are required, a nil Logs answers GET /logs with 404, and SubscriptionLogs, required exactly when the manifest lists the operation logs, answers a signed POST /logs with the lines of the subscription its request names.
type Config struct {
	Resource Resource

	// Observers needs one Observer for each resource the manifest declares, except one whose every type carries an observe spec, which KitObserver reads instead.
	Observers   map[string]Observer
	KitObserver observe.KitObserver

	// TypeObservers is keyed by resource-type id and serves a validate naming a resource the manifest no longer declares.
	TypeObservers map[string]Observer

	// Manifest is the block's mitstack/v1beta manifest, whose metadata.id every request's OBO token must name.
	Manifest []byte

	// Types is the type snapshot the manifest names its types from, and a declared resource of a type it marks EndsWithSubscription is counted absent once the unsubscribe's delete succeeds, unobserved.
	Types contract.TypeSnapshot

	JWKSURL string

	// PublicKey defaults to a kit KeyCache over JWKSURL, read at Now, and is no alternative to it, since webhook.AuthenticateHook rejects every request when JWKSURL is empty; an error wrapping webhook.ErrKeysUnavailable answers 503.
	PublicKey func(kid string) (ed25519.PublicKey, error)

	Replay *webhook.Replay

	Logs *webhook.LogBuffer

	SubscriptionLogs func(ctx context.Context, d contract.Desired) ([]string, error)

	// Verifier answers a verify-connection, and is required exactly when the manifest lists that operation, which a capability whose only operation it is serves with no Resource.
	Verifier ConnectionVerifier

	Now func() time.Time
}

type Handler struct {
	resource Resource

	declared      []namedResource
	typeObservers map[string]Observer
	provided      map[string]bool

	types      contract.TypeSnapshot
	capability bool

	kit             observe.KitObserver
	fullReads       *fullReadClock
	fullReadChecker FullReadChecker
	kitReadAmender  KitReadAmender

	leaser Leaser

	audience  string
	jwksURL   string
	publicKey func(kid string) (ed25519.PublicKey, error)
	replay    *webhook.Replay
	logs      *webhook.LogBuffer

	subscriptionLogs func(ctx context.Context, d contract.Desired) ([]string, error)

	verifier ConnectionVerifier

	now func() time.Time

	idempotency *idempotencyCache
}

// New refuses a configuration that would let a block override an invariant.
func New(cfg Config) (*Handler, error) {
	if len(cfg.Manifest) == 0 {
		return nil, errNoManifest
	}
	if cfg.JWKSURL == "" {
		return nil, errEmptyJWKSURL
	}
	declared, err := readManifestDeclarations(cfg.Manifest)
	if err != nil {
		return nil, err
	}
	if cfg.Resource == nil && !declared.verifyOnly {
		return nil, errNilResource
	}
	if err := checkVerifier(cfg, declared); err != nil {
		return nil, err
	}
	resources, err := observedResources(cfg, declared)
	if err != nil {
		return nil, err
	}
	if err := validateTypeObservers(cfg.TypeObservers); err != nil {
		return nil, err
	}
	if err := checkSubscriptionLogs(cfg, declared); err != nil {
		return nil, err
	}
	leaser, err := checkLeaser(cfg, declared)
	if err != nil {
		return nil, err
	}

	h := &Handler{
		leaser:           leaser,
		resource:         cfg.Resource,
		declared:         resources,
		typeObservers:    cfg.TypeObservers,
		provided:         declared.provided,
		types:            cfg.Types,
		capability:       declared.capability,
		audience:         declared.id,
		jwksURL:          cfg.JWKSURL,
		publicKey:        cfg.PublicKey,
		replay:           cfg.Replay,
		logs:             cfg.Logs,
		subscriptionLogs: cfg.SubscriptionLogs,
		verifier:         cfg.Verifier,
		now:              cfg.Now,
		idempotency:      newIdempotencyCache(),
		kit:              cfg.KitObserver,
		fullReads:        newFullReadClock(),
	}
	h.fullReadChecker, _ = cfg.Resource.(FullReadChecker)
	h.kitReadAmender, _ = cfg.Resource.(KitReadAmender)

	if h.now == nil {
		h.now = time.Now
	}
	if h.replay == nil {
		h.replay = webhook.NewReplay()
	}
	if h.publicKey == nil {
		cache := webhook.NewKeyCache()
		h.publicKey = func(kid string) (ed25519.PublicKey, error) {
			return cache.PublicKey(h.jwksURL, nil, kid, h.now())
		}
	}

	return h, nil
}

func (h *Handler) Serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/hook":
		if r.Method != http.MethodPost {
			h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.serveHook(w, r)
	case "/logs":
		switch {
		case r.Method == http.MethodGet:
			h.serveLogs(w, r)
		case r.Method == http.MethodPost && h.subscriptionLogs != nil:
			h.serveSubscriptionLogs(w, r)
		default:
			h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	default:
		h.writeError(w, http.StatusNotFound, "no such path")
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.Serve(w, r) }

func (h *Handler) webhookDeps() webhook.Deps {
	return webhook.Deps{
		JWKSURL:    h.jwksURL,
		PublicKey:  h.publicKey,
		Replay:     h.replay,
		WriteError: h.writeError,
		Now:        h.now,
	}
}

func (h *Handler) serveLogs(w http.ResponseWriter, r *http.Request) {
	if h.logs == nil {
		h.writeError(w, http.StatusNotFound, "this block keeps no logs")
		return
	}
	webhook.ServeLogs(h.webhookDeps(), h.logs, w, r)
}

func (h *Handler) serveHook(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authenticatedRequest(w, r)
	if !ok {
		return
	}
	if req.Operation == contract.OperationLogs {
		h.writeError(w, http.StatusBadRequest, "a logs request is served on /logs, not /hook")
		return
	}
	h.dispatchRequest(w, r, req)
}

// authenticatedRequest reads, verifies and decodes a signed request body and checks that its OBO token names this block and the request's subscription, returning false once a refusal has been written.
func (h *Handler) authenticatedRequest(w http.ResponseWriter, r *http.Request) (contract.Request, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHookBodyBytes+1))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "cannot read request body: "+err.Error())
		return contract.Request{}, false
	}
	if len(body) > maxHookBodyBytes {
		h.writeError(w, http.StatusBadRequest, "request body exceeds the maximum allowed size")
		return contract.Request{}, false
	}

	pub, ok := webhook.AuthenticateHook(h.webhookDeps(), w, r, body)
	if !ok {
		return contract.Request{}, false
	}

	req, err := contract.DecodeRequest(body)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return contract.Request{}, false
	}

	if req.Operation == contract.OperationVerifyConnection {
		return req, true
	}
	if req.OBO == "" {
		h.writeError(w, http.StatusUnauthorized, "missing OBO token")
		return contract.Request{}, false
	}

	claims, err := webhook.VerifyOBO(pub, req.OBO, h.now())
	if err != nil {
		h.writeError(w, http.StatusUnauthorized, "invalid OBO token: "+err.Error())
		return contract.Request{}, false
	}
	if claims != (webhook.OBOClaims{Audience: h.audience, OrgID: req.OrgID, AppID: req.AppID, SubscriptionID: req.SubscriptionID}) {
		h.writeError(w, http.StatusUnauthorized, "the OBO token was minted for another block, org, app or subscription")
		return contract.Request{}, false
	}
	return req, true
}

// requestRefusals classes each 4xx the handler answers before a block is consulted, so the engine reads every 4xx as a refusal.
var requestRefusals = map[int]string{
	http.StatusBadRequest:       "malformed-request",
	http.StatusUnauthorized:     "untrusted-request",
	http.StatusNotFound:         "unknown-path",
	http.StatusMethodNotAllowed: "method-not-allowed",
}

func (h *Handler) writeError(w http.ResponseWriter, status int, msg string) {
	if class, ok := requestRefusals[status]; ok {
		msg = (&Refused{Class: class, Detail: msg}).Error()
		writeResponse(w, status, contract.Response{Error: msg, StatusDetail: msg})
		return
	}
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

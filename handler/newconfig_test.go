package handler_test

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/kittest"
)

// manifestDeclaring builds a minimal block manifest declaring exactly the
// given hook names under spec.lifecycle.hooks, in the nested block-sequence
// shape the CONTRACT pins ("Reading the manifest").
func manifestDeclaring(hooks ...string) []byte {
	var b strings.Builder
	b.WriteString("apiVersion: mitstack/v1alpha\n")
	b.WriteString("kind: Block\n")
	b.WriteString("metadata:\n  id: test-block\n  name: Test Block\n")
	b.WriteString("spec:\n  layer: infra\n  lifecycle:\n")
	if len(hooks) == 0 {
		b.WriteString("    hooks: []\n")
	} else {
		b.WriteString("    hooks:\n")
		for _, h := range hooks {
			b.WriteString("      - " + h + "\n")
		}
	}
	return []byte(b.String())
}

const manifestWithNoHooksKey = `apiVersion: mitstack/v1alpha
kind: Block
metadata:
  id: test-block
  name: Test Block
spec:
  layer: infra
`

func noopHook(context.Context, contract.Desired) error { return nil }

// --- Start-up refusals from New ---------------------------------------------

func TestNew_ErrorsWhenResourceIsNil(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	cfg := baseConfig(t, nil, s)

	if _, err := handler.New(cfg); err == nil {
		t.Fatal("expected an error for a nil Resource, got none")
	}
}

func TestNew_ErrorsOnUnknownHookPoint(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	cfg := baseConfig(t, f, s)
	cfg.Manifest = manifestDeclaring("beforeEnsure")
	cfg.Hooks = map[string]handler.Hook{
		"onFrobnicate": noopHook, // not one of the four HookXxx constants
	}

	_, err := handler.New(cfg)
	if err == nil {
		t.Fatal("expected an error for an unknown hook point, got none")
	}
	if !strings.Contains(err.Error(), "onFrobnicate") {
		t.Errorf("error %q does not name the offending hook %q", err.Error(), "onFrobnicate")
	}
	for _, known := range []string{handler.HookBeforeEnsure, handler.HookAfterEnsure, handler.HookBeforeDelete, handler.HookAfterDelete} {
		if !strings.Contains(err.Error(), known) {
			t.Errorf("error %q does not list known hook point %q", err.Error(), known)
		}
	}
}

func TestNew_ErrorsOnHookNotDeclaredInManifest(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	cfg := baseConfig(t, f, s)
	// The manifest declares afterDelete only; registering beforeEnsure must
	// be refused.
	cfg.Manifest = manifestDeclaring("afterDelete")
	cfg.Hooks = map[string]handler.Hook{
		handler.HookBeforeEnsure: noopHook,
	}

	_, err := handler.New(cfg)
	if err == nil {
		t.Fatal("expected an error for a hook not declared in the manifest, got none")
	}
	if !strings.Contains(err.Error(), handler.HookBeforeEnsure) {
		t.Errorf("error %q does not name the offending hook %q", err.Error(), handler.HookBeforeEnsure)
	}
	if !strings.Contains(err.Error(), "afterDelete") {
		t.Errorf("error %q does not name what the manifest actually declares (%q)", err.Error(), "afterDelete")
	}
}

func TestNew_ErrorsWhenHooksNonEmptyAndManifestEmpty(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	cfg := baseConfig(t, f, s)
	cfg.Manifest = nil
	cfg.Hooks = map[string]handler.Hook{
		handler.HookBeforeEnsure: noopHook,
	}

	if _, err := handler.New(cfg); err == nil {
		t.Fatal("expected an error when Hooks is set but Manifest is empty, got none")
	}
}

// TestNew_AcceptsADeclaredAndRegisteredHookAndCallsItAtItsPoint proves the
// positive: a hook declared in the manifest and registered under the same
// name is accepted by New, and actually runs at its declared point.
func TestNew_AcceptsADeclaredAndRegisteredHookAndCallsItAtItsPoint(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}

	var called bool
	h := newHandler(t, f, s, func(c *handler.Config) {
		c.Manifest = manifestDeclaring("beforeEnsure")
		c.Hooks = map[string]handler.Hook{
			handler.HookBeforeEnsure: func(ctx context.Context, d contract.Desired) error {
				called = true
				if f.EnsureCalls != 0 {
					t.Error("beforeEnsure ran after Ensure, not before it")
				}
				return nil
			},
		}
	})

	status, raw := postHook(t, h, s, envelope(t, s, "nonce-hook-pos", nil))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}
	if !called {
		t.Error("the declared-and-registered beforeEnsure hook was never called")
	}
}

// TestNew_ManifestDeclaringAHookNobodyRegistersIsAccepted pins that a hook
// the manifest declares but nothing registers is simply not called — it is
// not a startup refusal, and the verb still completes normally.
func TestNew_ManifestDeclaringAHookNobodyRegistersIsAccepted(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	h := newHandler(t, f, s, func(c *handler.Config) {
		c.Manifest = manifestDeclaring("beforeEnsure", "afterEnsure")
		// c.Hooks left nil: declared, but nobody registers.
	})

	status, raw := postHook(t, h, s, envelope(t, s, "nonce-hook-unused", nil))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}
	if f.EnsureCalls != 1 {
		t.Errorf("EnsureCalls = %d, want 1 — an unregistered declared hook must not change verb dispatch", f.EnsureCalls)
	}
}

// TestNew_EmptyManifestHooksListDeclaresNone pins that "hooks: []" declares
// no hook, so registering any hook against it is refused exactly like a
// manifest with no hooks: key at all.
func TestNew_EmptyManifestHooksListDeclaresNone(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	cfg := baseConfig(t, f, s)
	cfg.Manifest = manifestDeclaring() // "hooks: []"
	cfg.Hooks = map[string]handler.Hook{
		handler.HookBeforeEnsure: noopHook,
	}

	if _, err := handler.New(cfg); err == nil {
		t.Fatal("expected an error: \"hooks: []\" declares no hook, so beforeEnsure is undeclared")
	}
}

// TestNew_ManifestWithNoHooksKeyDeclaresNone mirrors the above for a
// manifest that omits spec.lifecycle.hooks entirely.
func TestNew_ManifestWithNoHooksKeyDeclaresNone(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	cfg := baseConfig(t, f, s)
	cfg.Manifest = []byte(manifestWithNoHooksKey)
	cfg.Hooks = map[string]handler.Hook{
		handler.HookAfterDelete: noopHook,
	}

	if _, err := handler.New(cfg); err == nil {
		t.Fatal("expected an error: a manifest with no hooks: key declares no hook")
	}
}

// --- New refuses an empty JWKSURL (MIT-3070 review) --------------------------
//
// webhook.AuthenticateHook rejects a request when JWKSURL is empty, before
// it ever consults PublicKey (webhook/authenticate.go:63-66). A handler
// configured with a key resolver and no JWKS URL would build today and then
// answer 401 to everything it ever receives. New refuses that configuration
// instead of letting it build silently broken.

func TestNew_ErrorsWhenJWKSURLIsEmpty(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	cfg := baseConfig(t, f, s)
	cfg.JWKSURL = ""

	_, err := handler.New(cfg)
	if err == nil {
		t.Fatal("expected an error for an empty JWKSURL, got none")
	}
	if !strings.Contains(err.Error(), "JWKSURL") {
		t.Errorf("error %q does not name JWKSURL", err.Error())
	}
}

// TestNew_ErrorsWhenJWKSURLIsEmptyEvenWithPublicKeyOverride pins the exact
// failure mode the review found: Config.PublicKey is not an alternative to
// JWKSURL, so supplying a key resolver does not excuse leaving JWKSURL
// empty.
func TestNew_ErrorsWhenJWKSURLIsEmptyEvenWithPublicKeyOverride(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	cfg := baseConfig(t, f, s)
	cfg.JWKSURL = ""
	cfg.PublicKey = func(kid string) (ed25519.PublicKey, error) { return s.Public, nil }

	_, err := handler.New(cfg)
	if err == nil {
		t.Fatal("expected an error for an empty JWKSURL even with Config.PublicKey set, got none")
	}
	if !strings.Contains(err.Error(), "JWKSURL") {
		t.Errorf("error %q does not name JWKSURL", err.Error())
	}
}

// --- Only spec.lifecycle.hooks declares a hook ----------------------------
//
// New reads the manifest through contract.ParseManifest, so a hooks: key
// anywhere but spec.lifecycle is a key no manifest field names, and the
// parser refuses it, naming its path.

// manifestWithDecoyTopLevelHooks carries a top-level "hooks:" key ahead of
// spec.lifecycle.hooks in the document.
const manifestWithDecoyTopLevelHooks = `apiVersion: mitstack/v1alpha
kind: Block
metadata:
  id: test-block
  name: Test Block
hooks:
  - decoyHook
spec:
  layer: infra
  lifecycle:
    hooks:
      - beforeEnsure
`

func TestNew_ManifestAnchoring_DecoyHooksKeyAboveSpecLifecycleIsRefusedAsUnknown(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	cfg := baseConfig(t, &kittest.FakeResource{}, s)
	cfg.Manifest = []byte(manifestWithDecoyTopLevelHooks)
	cfg.Hooks = map[string]handler.Hook{handler.HookBeforeEnsure: noopHook}

	requireNewRefusesWithTheParsersMessage(t, cfg, "hooks")
}

// manifestWithDecoyTooDeepHooks carries a second "hooks:" key nested one
// level deeper than spec.lifecycle.hooks, under spec.lifecycle.extra, ahead
// of the real one in the document.
const manifestWithDecoyTooDeepHooks = `apiVersion: mitstack/v1alpha
kind: Block
metadata:
  id: test-block
  name: Test Block
spec:
  layer: infra
  lifecycle:
    extra:
      hooks:
        - afterEnsure
    hooks:
      - beforeEnsure
`

func TestNew_ManifestAnchoring_DecoyHooksKeyOneLevelTooDeepIsRefusedAsUnknown(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	cfg := baseConfig(t, &kittest.FakeResource{}, s)
	cfg.Manifest = []byte(manifestWithDecoyTooDeepHooks)
	cfg.Hooks = map[string]handler.Hook{handler.HookAfterEnsure: noopHook}

	requireNewRefusesWithTheParsersMessage(t, cfg, "spec.lifecycle.extra")
}

// --- An unreadable hooks declaration is refused, not read as none ----------
//
// The flow form "hooks: [beforeEnsure, afterDelete]" declares its hooks as
// the block sequence does, and a hooks: value that is not a list is refused
// rather than read as declaring nothing.

func manifestWithLifecycleHooksLine(hooksLine string) []byte {
	return []byte("apiVersion: mitstack/v1alpha\n" +
		"kind: Block\n" +
		"metadata:\n  id: test-block\n  name: Test Block\n" +
		"spec:\n  layer: infra\n  lifecycle:\n" +
		"    " + hooksLine + "\n")
}

// TestNew_ManifestFlowFormHooksListIsRead pins that "hooks: [beforeEnsure]"
// — the flow-sequence form — is read exactly like the block-sequence form.
func TestNew_ManifestFlowFormHooksListIsRead(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	var called bool
	h := newHandler(t, &kittest.FakeResource{}, s, func(c *handler.Config) {
		c.Manifest = manifestWithLifecycleHooksLine("hooks: [beforeEnsure]")
		c.Hooks = map[string]handler.Hook{
			handler.HookBeforeEnsure: func(context.Context, contract.Desired) error {
				called = true
				return nil
			},
		}
	})

	status, raw := postHook(t, h, s, envelope(t, s, "nonce-manifest-flow-form", nil))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}
	if !called {
		t.Error("beforeEnsure was never called — New must read the flow form \"hooks: [beforeEnsure]\"")
	}
}

// TestNew_ManifestUnreadableHooksValueIsRefused pins the negative: a
// hooks: value under spec.lifecycle that fits none of the supported forms
// (not "[]", not a "[a, b]" flow list, not an indented "- name" sequence)
// must make New return an error naming the manifest, rather than silently
// treating the declaration as empty.
//
// No hook is registered here on purpose. Registering one against an
// undeclared-looking manifest already errors today for an unrelated reason
// (the ordinary "hook not declared" refusal), which would mask the actual
// defect the fix document names: a value nobody can read must not be
// mistaken for a declaration of nothing. Leaving Config.Hooks empty isolates
// that: with nothing registered, today's code has no reason to look at the
// manifest at all and New succeeds silently, even though the manifest's own
// hooks: stanza is nonsense.
func TestNew_ManifestUnreadableHooksValueIsRefused(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	cfg := baseConfig(t, f, s)
	cfg.Manifest = manifestWithLifecycleHooksLine("hooks: beforeEnsure")

	_, err := handler.New(cfg)
	if err == nil {
		t.Fatal("expected an error for a hooks: value that fits no supported form, got none — " +
			"an unreadable declaration must not be silently treated as declaring nothing")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "manifest") {
		t.Errorf("error %q does not name the manifest", err.Error())
	}
	requireNewRefusesWithTheParsersMessage(t, cfg, contract.LifecycleHooksField)
}

// --- A comment inside the hooks: block sequence does not shorten it --------

// manifestWithHooksBlockCommentBetweenItems declares beforeEnsure and
// afterDelete under spec.lifecycle.hooks with a comment line sitting between
// them. A comment is valid inside a YAML block sequence, so both hooks are
// declared.
const manifestWithHooksBlockCommentBetweenItems = `apiVersion: mitstack/v1alpha
kind: Block
metadata:
  id: test-block
  name: Test Block
spec:
  layer: infra
  lifecycle:
    hooks:
      - beforeEnsure
      # a comment between two items
      - afterDelete
`

// TestNew_ManifestCommentBetweenHookItemsDeclaresBoth pins that both hooks
// either side of the comment are accepted — registering either one must not
// be refused as undeclared.
func TestNew_ManifestCommentBetweenHookItemsDeclaresBoth(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	cfg := baseConfig(t, f, s)
	cfg.Manifest = []byte(manifestWithHooksBlockCommentBetweenItems)
	cfg.Hooks = map[string]handler.Hook{
		handler.HookBeforeEnsure: noopHook,
		handler.HookAfterDelete:  noopHook,
	}

	if _, err := handler.New(cfg); err != nil {
		t.Fatalf("handler.New: unexpected error — both beforeEnsure and afterDelete are declared either side of the comment: %v", err)
	}
}

// TestNew_ManifestCommentBetweenHookItemsErrorListsBothDeclaredHooks proves
// the error for an actually-undeclared hook lists every hook the manifest
// declares, including the one after the comment — the "declares:" list must
// never omit a hook the manifest does declare.
func TestNew_ManifestCommentBetweenHookItemsErrorListsBothDeclaredHooks(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	cfg := baseConfig(t, f, s)
	cfg.Manifest = []byte(manifestWithHooksBlockCommentBetweenItems)
	cfg.Hooks = map[string]handler.Hook{
		handler.HookBeforeDelete: noopHook, // declared by neither item
	}

	_, err := handler.New(cfg)
	if err == nil {
		t.Fatal("expected an error: beforeDelete is declared by neither item, got none")
	}
	if !strings.Contains(err.Error(), "beforeEnsure") {
		t.Errorf("error %q does not list beforeEnsure among what the manifest declares", err.Error())
	}
	if !strings.Contains(err.Error(), "afterDelete") {
		t.Errorf("error %q does not list afterDelete among what the manifest declares — "+
			"it must never claim the manifest fails to declare a hook it does declare", err.Error())
	}
}

// --- A tab-indented hooks: sequence fails on the reader's own error path ---

// manifestWithTabIndentedHooksSequence indents the same two items with a tab
// rather than spaces. A tab is not valid YAML indentation, so the reader
// must refuse it rather than read it as an empty declaration.
const manifestWithTabIndentedHooksSequence = "apiVersion: mitstack/v1alpha\n" +
	"kind: Block\n" +
	"metadata:\n  id: test-block\n  name: Test Block\n" +
	"spec:\n  layer: infra\n  lifecycle:\n" +
	"    hooks:\n" +
	"\t- beforeEnsure\n" +
	"\t- afterDelete\n"

// TestNew_ManifestTabIndentedHooksSequenceFailsNamingManifest pins that a
// tab-indented sequence fails New outright, naming the manifest, on its own
// account — with no hook registered at all, so the only possible source of
// an error is the manifest read itself, not a "hook not declared" refusal
// riding on top of a wrongly-empty declaration.
func TestNew_ManifestTabIndentedHooksSequenceFailsNamingManifest(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	cfg := baseConfig(t, f, s)
	cfg.Manifest = []byte(manifestWithTabIndentedHooksSequence)
	// cfg.Hooks is left nil on purpose: with nothing registered, New has no
	// "hook not declared" refusal available to fall back on, so an error
	// here can only come from the manifest read failing on its own account.
	// Today it reads the tab-indented sequence as zero items and no error,
	// so New builds successfully — silently treating an unreadable
	// declaration as an empty one.

	_, err := handler.New(cfg)
	if err == nil {
		t.Fatal("expected an error for a tab-indented hooks sequence, got none — " +
			"a tab is not valid indentation and must not be read as an empty declaration")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "manifest") {
		t.Errorf("error %q does not name the manifest", err.Error())
	}
	if strings.Contains(err.Error(), "does not declare it") {
		t.Errorf("error %q comes from the \"hook not declared\" refusal, not the reader's own error path — "+
			"got here with no hook registered, so declaredSet must never have been consulted", err.Error())
	}
	if f.EnsureCalls != 0 || f.ObserveCalls != 0 || f.DeleteCalls != 0 {
		t.Errorf("Resource was called before New even returned: Ensure=%d Observe=%d Delete=%d",
			f.EnsureCalls, f.ObserveCalls, f.DeleteCalls)
	}
}

// --- A sibling stanza, a commented-out hooks: key, and CRLF endings --------

// manifestWithSiblingSpecOtherHooksStanza declares beforeEnsure under
// spec.lifecycle.hooks and afterDelete under the sibling spec.other.hooks,
// a key no manifest field names.
const manifestWithSiblingSpecOtherHooksStanza = `apiVersion: mitstack/v1alpha
kind: Block
metadata:
  id: test-block
  name: Test Block
spec:
  layer: infra
  lifecycle:
    hooks:
      - beforeEnsure
  other:
    hooks:
      - afterDelete
`

func TestNew_ManifestSiblingSpecOtherHooksStanzaIsRefusedAsUnknown(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	cfg := baseConfig(t, &kittest.FakeResource{}, s)
	cfg.Manifest = []byte(manifestWithSiblingSpecOtherHooksStanza)
	cfg.Hooks = map[string]handler.Hook{handler.HookBeforeEnsure: noopHook}

	requireNewRefusesWithTheParsersMessage(t, cfg, "spec.other")
}

// manifestWithCommentedOutHooksKey comments out the entire hooks: stanza
// under spec.lifecycle. A commented-out key is not a declaration, so this
// must read exactly like a manifest with no hooks: key at all.
const manifestWithCommentedOutHooksKey = `apiVersion: mitstack/v1alpha
kind: Block
metadata:
  id: test-block
  name: Test Block
spec:
  layer: infra
  lifecycle:
    # hooks:
    #   - beforeEnsure
`

func TestNew_ManifestCommentedOutHooksKeyDeclaresNone(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	cfg := baseConfig(t, f, s)
	cfg.Manifest = []byte(manifestWithCommentedOutHooksKey)
	cfg.Hooks = map[string]handler.Hook{
		handler.HookBeforeEnsure: noopHook,
	}

	if _, err := handler.New(cfg); err == nil {
		t.Fatal("expected an error: a commented-out hooks: line declares no hook, same as no hooks: key at all")
	}
}

// TestNew_ManifestCRLFLineEndingsHooksSequenceIsRead pins that a hooks:
// block sequence terminated with CRLF line endings is read the same as one
// terminated with bare LF.
func TestNew_ManifestCRLFLineEndingsHooksSequenceIsRead(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	cfg := baseConfig(t, f, s)
	cfg.Manifest = []byte(strings.ReplaceAll(string(manifestDeclaring("beforeEnsure", "afterDelete")), "\n", "\r\n"))
	cfg.Hooks = map[string]handler.Hook{
		handler.HookBeforeEnsure: noopHook,
		handler.HookAfterDelete:  noopHook,
	}

	if _, err := handler.New(cfg); err != nil {
		t.Fatalf("handler.New: unexpected error for a CRLF-terminated manifest: %v", err)
	}
}

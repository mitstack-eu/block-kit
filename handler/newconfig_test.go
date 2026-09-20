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

	status, raw := postHook(t, h, s, envelope("nonce-hook-pos", nil))
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

	status, raw := postHook(t, h, s, envelope("nonce-hook-unused", nil))
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

// --- The hook scan is anchored under spec.lifecycle (MIT-3070 review) -------
//
// manifestHooks used to take the first line anywhere in the file whose
// trimmed text was "hooks:", so a manifest carrying any other hooks: key
// earlier in the document made New read the wrong list. It is now anchored
// by indentation: spec: at the document's top level, lifecycle: nested under
// it, hooks: nested under that — and only that one counts.

// manifestWithDecoyTopLevelHooks carries a top-level "hooks:" key — a
// plausible unrelated schema field — ahead of spec.lifecycle.hooks in the
// document. The unanchored reader picks up "decoyHook" and never reaches the
// real list at all.
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

func TestNew_ManifestAnchoring_DecoyHooksKeyAboveSpecLifecycleIsIgnored(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	var called bool
	h := newHandler(t, &kittest.FakeResource{}, s, func(c *handler.Config) {
		c.Manifest = []byte(manifestWithDecoyTopLevelHooks)
		c.Hooks = map[string]handler.Hook{
			handler.HookBeforeEnsure: func(context.Context, contract.Desired) error {
				called = true
				return nil
			},
		}
	})

	status, raw := postHook(t, h, s, envelope("nonce-manifest-anchor-decoy-above", nil))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", status, raw)
	}
	if !called {
		t.Error("beforeEnsure was never called — New must read spec.lifecycle.hooks, not the decoy top-level hooks: key")
	}
}

// manifestWithDecoyTooDeepHooks carries a second "hooks:" key nested one
// level deeper than spec.lifecycle.hooks (under spec.lifecycle.extra), ahead
// of the real one in the document. The unanchored reader picks up
// "afterEnsure" from the too-deep decoy; the real spec.lifecycle.hooks list
// declares only "beforeEnsure".
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

func TestNew_ManifestAnchoring_DecoyHooksKeyOneLevelTooDeepIsIgnored(t *testing.T) {
	s := kittest.NewSigner(t, "kid-1", 7)
	f := &kittest.FakeResource{}
	cfg := baseConfig(t, f, s)
	cfg.Manifest = []byte(manifestWithDecoyTooDeepHooks)
	cfg.Hooks = map[string]handler.Hook{
		handler.HookAfterEnsure: noopHook,
	}

	_, err := handler.New(cfg)
	if err == nil {
		t.Fatal("expected an error: afterEnsure is declared only by the too-deep decoy under spec.lifecycle.extra.hooks, " +
			"not by spec.lifecycle.hooks itself, so registering it must be refused")
	}
	if !strings.Contains(err.Error(), handler.HookAfterEnsure) {
		t.Errorf("error %q does not name the offending hook %q", err.Error(), handler.HookAfterEnsure)
	}
}

// --- An unreadable hooks declaration is refused, not read as none ----------
//
// "hooks: [beforeEnsure, afterDelete]" is valid YAML and matched neither the
// "hooks: []" branch nor the block-sequence branch, so it declared nothing
// and New then refused a hook the manifest does declare. The fix reads the
// flow form too, and refuses a hooks: value that fits none of the supported
// forms instead of silently treating it as declaring nothing.

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

	status, raw := postHook(t, h, s, envelope("nonce-manifest-flow-form", nil))
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
}

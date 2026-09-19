package handler_test

import (
	"context"
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

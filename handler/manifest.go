package handler

import (
	"errors"
	"fmt"
	"strings"

	"github.com/mitstack-eu/operator-kit/contract"
)

// errNilResource is returned by New when Config.Resource is nil.
var errNilResource = errors.New("handler: Config.Resource is required")

// errEmptyJWKSURL is returned by New when Config.JWKSURL is empty.
// webhook.AuthenticateHook rejects every request on an empty JWKSURL before
// it ever consults Config.PublicKey, so PublicKey is never a substitute for
// it.
var errEmptyJWKSURL = errors.New("handler: Config.JWKSURL is required")

// knownHooks is the closed set of hook points New will ever accept a
// registration for. It comes from the contract so the handler and the
// platform's manifest validation share one set.
var knownHooks = contract.LifecycleHookPoints()

// manifestDeclarations is what New reads of a block's manifest: the hook
// points spec.lifecycle.hooks declares, and
// spec.lifecycle.retainsOnUnsubscribe. A mitstack/v1beta lifecycle has
// neither, so a v1beta manifest declares no hook and does not retain its
// resource.
type manifestDeclarations struct {
	hooks                []string
	retainsOnUnsubscribe bool
}

// readManifestDeclarations reads Config.Manifest through
// contract.ParseManifest. An empty manifest declares nothing. A manifest
// the parser refuses is an error carrying the parser's message, since a
// declaration nobody can read must not be mistaken for an absent one.
func readManifestDeclarations(manifest []byte) (manifestDeclarations, error) {
	if len(manifest) == 0 {
		return manifestDeclarations{}, nil
	}
	m, err := contract.ParseManifest(manifest)
	if err != nil {
		return manifestDeclarations{}, fmt.Errorf("handler: Config.Manifest is not a manifest the kit's parser accepts: %w", err)
	}
	if m.V1Alpha == nil || m.V1Alpha.Spec.Lifecycle == nil {
		return manifestDeclarations{}, nil
	}
	l := m.V1Alpha.Spec.Lifecycle
	return manifestDeclarations{hooks: l.Hooks, retainsOnUnsubscribe: l.RetainsOnUnsubscribe}, nil
}

// validateHooks refuses a Config that would let a block override an
// invariant through Hooks: a registration under a name that is not one of
// the four HookXxx constants, a registration for a hook declared does not
// name, or a non-empty Hooks with no Manifest to check it against. declared
// is the manifest's spec.lifecycle.hooks, which is refused when it names a
// hook outside knownHooks or names one hook twice, even with no Hooks
// registered at all, so that New refuses exactly the declarations the
// platform's manifest validation refuses.
func validateHooks(cfg Config, declared []string) error {
	if len(cfg.Hooks) > 0 && len(cfg.Manifest) == 0 {
		return errors.New("handler: Config.Hooks is set but Config.Manifest is empty — a hook can only be registered if the manifest declares it")
	}

	knownSet := make(map[string]bool, len(knownHooks))
	for _, h := range knownHooks {
		knownSet[h] = true
	}
	declaredSet := make(map[string]bool, len(declared))
	for _, h := range declared {
		if !knownSet[h] {
			return fmt.Errorf("handler: manifest's spec.lifecycle.hooks declares unknown hook point %q; the known hook points are %s",
				h, strings.Join(knownHooks, ", "))
		}
		if declaredSet[h] {
			return fmt.Errorf("handler: manifest's spec.lifecycle.hooks declares hook %q more than once", h)
		}
		declaredSet[h] = true
	}

	for name := range cfg.Hooks {
		if !knownSet[name] {
			return fmt.Errorf("handler: Config.Hooks registers unknown hook point %q; the known hook points are %s",
				name, strings.Join(knownHooks, ", "))
		}
		if !declaredSet[name] {
			return fmt.Errorf("handler: Config.Hooks registers hook %q, but the manifest's spec.lifecycle.hooks does not declare it (declares: %s)",
				name, strings.Join(declared, ", "))
		}
	}
	return nil
}

// ManifestAppNameNaming reads a block manifest's spec.naming.appName
// declaration (contract.AppNameNamingField) through contract.ParseManifest,
// which requires its maxLength and each derived name's template and limit.
// A manifest that declares no spec.naming.appName returns nil and no error;
// a mitstack/v1beta manifest has no spec.naming, so it declares none. A
// manifest the parser refuses, an empty one included, is an error carrying
// the parser's message, since a declaration nobody can read must not be
// mistaken for an absent one.
func ManifestAppNameNaming(manifest []byte) (*contract.AppNameNaming, error) {
	m, err := contract.ParseManifest(manifest)
	if err != nil {
		return nil, fmt.Errorf("handler: the manifest's %s is unknown, because the kit's parser refuses the manifest: %w",
			contract.AppNameNamingField, err)
	}
	if m.V1Alpha == nil || m.V1Alpha.Spec.Naming == nil {
		return nil, nil
	}
	return m.V1Alpha.Spec.Naming.AppName, nil
}

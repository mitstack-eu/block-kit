package handler

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/mitstack-eu/block-kit/contract"
)

var errNilResource = errors.New("handler: Config.Resource is required unless the manifest's only operation is verify-connection")

var errNoManifest = fmt.Errorf("handler: Config.Manifest is required: pass the bytes of the block's %s manifest", contract.ManifestAPIVersion)

// errEmptyJWKSURL stands because webhook.AuthenticateHook rejects every request on an empty JWKSURL before consulting PublicKey.
var errEmptyJWKSURL = errors.New("handler: Config.JWKSURL is required")

type manifestDeclarations struct {
	id         string
	resources  []contract.ManifestResource
	provided   map[string]bool
	capability bool
	logs       bool
	verifies   bool
	// verifyOnly is a manifest whose one operation is verify-connection, which needs no Resource.
	verifyOnly bool
}

func readManifestDeclarations(manifest []byte) (manifestDeclarations, error) {
	m, err := contract.ParseManifest(manifest)
	if err != nil {
		return manifestDeclarations{}, fmt.Errorf("handler: Config.Manifest is not a manifest the kit's parser accepts: %w", err)
	}
	provided := make(map[string]bool, len(m.Block.Spec.Provides))
	for _, name := range m.Block.Spec.Provides {
		provided[name] = true
	}
	return manifestDeclarations{
		id:         m.ID(),
		resources:  m.Block.Spec.Resources,
		provided:   provided,
		capability: m.Block.Type == contract.ManifestTypeCapability,
		logs:       slices.Contains(m.Block.Spec.Lifecycle.Operations, contract.OperationLogs),
		verifies:   slices.Contains(m.Block.Spec.Lifecycle.Operations, contract.OperationVerifyConnection),
		verifyOnly: slices.Equal(m.Block.Spec.Lifecycle.Operations, []string{contract.OperationVerifyConnection}),
	}, nil
}

// checkVerifier refuses a Verifier the manifest does not list the operation for, and the operation listed with no Verifier to answer it.
func checkVerifier(cfg Config, declared manifestDeclarations) error {
	switch {
	case declared.verifies && cfg.Verifier == nil:
		return fmt.Errorf("handler: the manifest lists %s, but Config.Verifier is nil, so nothing could answer it", contract.OperationVerifyConnection)
	case !declared.verifies && cfg.Verifier != nil:
		return fmt.Errorf("handler: Config.Verifier is set, but the manifest does not list %s, so it would never be asked", contract.OperationVerifyConnection)
	case declared.verifies && !declared.capability:
		return fmt.Errorf("handler: the manifest lists %s, which only a capability manifest may", contract.OperationVerifyConnection)
	}
	return nil
}

type namedResource struct {
	name        string
	observer    Observer
	declaration contract.ManifestResource
}

// named is nr under name, which is nr's own name or one of its members'.
func (nr namedResource) named(name string) namedResource {
	nr.name = name
	return nr
}

// units lists what a block-level verb observes, in manifest order, putting in each entry's place the names contract.ManifestResource.Names gives it among held.
func units[V any](declared []namedResource, held map[string]V) []namedResource {
	names := slices.Collect(maps.Keys(held))
	out := make([]namedResource, 0, len(declared))
	for _, nr := range declared {
		for _, name := range nr.declaration.Names(names) {
			out = append(out, nr.named(name))
		}
	}
	return out
}

// observedResources refuses a declared resource with no observer, an observer under an undeclared name, and an entry marked many whose members the block cannot report.
func observedResources(cfg Config, declared manifestDeclarations) ([]namedResource, error) {
	declaredSet := make(map[string]bool, len(declared.resources))
	out := make([]namedResource, 0, len(declared.resources))
	_, reports := cfg.Resource.(ResourceReporter)
	_, owning := cfg.Resource.(OwnSecretsReporter)
	for _, decl := range declared.resources {
		if decl.Many && !reports && !owning {
			return nil, fmt.Errorf("handler: the manifest marks resource %q many, but Config.Resource is neither a handler.ResourceReporter nor a handler.OwnSecretsReporter, so it cannot report the entry's members", decl.Name)
		}
		declaredSet[decl.Name] = true
		o := cfg.Observers[decl.Name]
		kitObserved, err := kitObservedDeclaration(cfg, decl)
		if err != nil {
			return nil, err
		}
		if o == nil && !kitObserved {
			return nil, fmt.Errorf("handler: the manifest declares resource %q, but Config.Observers has no observe for it; every declared resource is observed through its own Observer", decl.Name)
		}
		out = append(out, namedResource{name: decl.Name, observer: o, declaration: decl})
	}
	if undeclared := undeclaredKeys(cfg.Observers, declaredSet); undeclared != "" {
		return nil, fmt.Errorf("handler: Config.Observers has an observer for %s, which the manifest's spec.resources does not declare", undeclared)
	}
	return out, nil
}

func validateTypeObservers(observers map[string]Observer) error {
	keys := make([]string, 0, len(observers))
	for typ := range observers {
		keys = append(keys, typ)
	}
	sort.Strings(keys)
	for _, typ := range keys {
		if _, _, _, err := contract.ParseResourceTypeID(typ); err != nil {
			return fmt.Errorf("handler: Config.TypeObservers has an observer under %q, which is not a resource type id: %w", typ, err)
		}
		if observers[typ] == nil {
			return fmt.Errorf("handler: Config.TypeObservers has a nil observer under %q, so a resource of that type could never be observed", typ)
		}
	}
	return nil
}

func undeclaredKeys[V any](m map[string]V, declared map[string]bool) string {
	var undeclared []string
	for name := range m {
		if !declared[name] {
			undeclared = append(undeclared, strconv.Quote(name))
		}
	}
	sort.Strings(undeclared)
	return strings.Join(undeclared, ", ")
}

package contract

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// checkBindings judges every resources.<name> and requires.<name> binding in
// each resource's inputs, nested mappings included, walking resources in
// manifest order and inputs in name order. A binding must name a resource or
// requirement of the block, a requirement being named by its name when set
// and by its kind otherwise, and through .outputs.<output> an output the
// resource's type publishes or the requirement's kind names. A top-level
// input the resource's type declares as accepting a resource or an output of
// one kind must be bound to that kind. A type or kind the catalogue does not
// hold leaves what depends on it unjudged. The error names the resource and
// the input.
func checkBindings(spec ManifestSpec) error {
	b := blockBindings{
		resources: make(map[string]ManifestResource, len(spec.Resources)),
		requires:  make(map[string]ManifestRequirement, len(spec.Requires)),
	}
	for _, r := range spec.Resources {
		b.resources[r.Name] = r
	}
	for _, q := range spec.Requires {
		name := q.Name
		if name == "" {
			name = q.Kind
		}
		b.requires[name] = q
	}
	for i, r := range spec.Resources {
		// A type outside the catalogue declares no input, so none is judged
		// by kind.
		t, _ := LookupResourceType(r.Type)
		accepts := make(map[string]string, len(t.Inputs))
		for _, in := range t.Inputs {
			accepts[in.Name] = in.Accepts
		}
		err := walkInputs(r.Inputs, "", func(path, binding string, top bool) error {
			kind, err := b.resolve(binding)
			if err != nil || kind == "" || !top {
				return err
			}
			return checkAcceptedKind(t.ID, accepts[path], binding, kind)
		})
		if err != nil {
			return fmt.Errorf("spec.resources[%d] %q: %w", i, r.Name, err)
		}
	}
	return nil
}

// blockBindings holds what a binding can name: the block's resources by
// name, and its requirements by the name a binding uses for them.
type blockBindings struct {
	resources map[string]ManifestResource
	requires  map[string]ManifestRequirement
}

// walkInputs calls judge for each scalar input under inputs, in name order,
// with its path (its name at the top level, prefix.name in a nested mapping)
// and whether it is a top-level input. A refusal names the input's path.
func walkInputs(inputs map[string]InputValue, prefix string, judge func(path, binding string, top bool) error) error {
	for _, name := range slices.Sorted(maps.Keys(inputs)) {
		path := prefix + name
		v := inputs[name]
		if v.Map != nil {
			if err := walkInputs(v.Map, path+".", judge); err != nil {
				return err
			}
			continue
		}
		if err := judge(path, v.Scalar, prefix == ""); err != nil {
			return fmt.Errorf("input %q: %w", path, err)
		}
	}
	return nil
}

// resolve judges one binding and returns the kind of the resource or
// requirement it is bound to. It returns "" when the binding names neither,
// takes a part other than the whole or an output, or is bound to a type or
// kind the catalogue does not hold.
func (b blockBindings) resolve(binding string) (string, error) {
	source, rest, ok := strings.Cut(binding, ".")
	if !ok || (source != "resources" && source != "requires") {
		return "", nil
	}
	name, part, _ := strings.Cut(rest, ".")
	// An output name may hold a dot, as cloud.region does, so it is all
	// that follows outputs.
	output, isOutput := strings.CutPrefix(part, "outputs.")
	takesKind := part == "" || isOutput
	if source == "resources" {
		r, ok := b.resources[name]
		if !ok {
			return "", fmt.Errorf("%s binds %q, which names no resource in spec.resources", binding, name)
		}
		t, ok := LookupResourceType(r.Type)
		if !ok || !takesKind {
			return "", nil
		}
		if isOutput && !slices.Contains(t.Outputs, output) {
			return "", fmt.Errorf("%s binds output %q, which type %s does not publish", binding, output, t.ID)
		}
		return t.Kind, nil
	}
	q, ok := b.requires[name]
	if !ok {
		return "", fmt.Errorf("%s binds %q, which names no requirement in spec.requires, where a requirement is named by its name when set and by its kind otherwise", binding, name)
	}
	k, ok := LookupKind(q.Kind)
	if !ok || !takesKind {
		return "", nil
	}
	if isOutput && !k.names(output) {
		return "", fmt.Errorf("%s binds output %q, which kind %s does not name", binding, output, k.ID)
	}
	return k.ID, nil
}

// checkAcceptedKind refuses a binding to kind where the input's accepts form,
// "resource:<kind>" or "output:<kind>/<output>", names another kind. Type
// typeID declares the input. "value", or "" for an input the type does not
// declare, accepts any kind.
func checkAcceptedKind(typeID, accepts, binding, kind string) error {
	if want, ok := strings.CutPrefix(accepts, "resource:"); ok && want != kind {
		return fmt.Errorf("type %s takes a resource of kind %s here, but %s is of kind %s", typeID, want, binding, kind)
	}
	if rest, ok := strings.CutPrefix(accepts, "output:"); ok {
		want, output, _ := strings.Cut(rest, "/")
		if want != kind {
			return fmt.Errorf("type %s takes output %s of a %s here, but %s is bound to a %s", typeID, output, want, binding, kind)
		}
	}
	return nil
}

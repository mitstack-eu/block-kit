package main

import (
	"fmt"
	"strings"

	"github.com/mitstack-eu/operator-kit/contract"
)

// steppedManifest is what the manifest suite reads of a manifest that
// declares spec.steps: the declaration contract.ValidateBlockSteps judges,
// and the entries ADR 0079 refuses in such a manifest.
type steppedManifest struct {
	declares     contract.BlockSteps
	capabilities []string
	extensions   []string
	dependsOn    bool
	// unnamed holds the spec.requires position of each entry that names no
	// resource, capability or extension.
	unnamed []int
}

// hasSteps reports whether spec declares a spec.steps key.
func hasSteps(spec *yamlNode) bool {
	return spec.get("steps") != nil
}

// steppedFields reads the stepped declaration from spec, which declares
// spec.steps.
func steppedFields(spec *yamlNode) (steppedManifest, error) {
	var m steppedManifest
	m.dependsOn = spec.get("dependsOn") != nil

	params, err := mappingItems(spec.get("params"), "spec.params")
	if err != nil {
		return m, err
	}
	for _, p := range params {
		name, err := scalarField(p, "spec.params[].name", "name")
		if err != nil {
			return m, err
		}
		m.declares.Params = append(m.declares.Params, name)
	}

	requires, err := mappingItems(spec.get("requires"), "spec.requires")
	if err != nil {
		return m, err
	}
	for i, item := range requires {
		if c := item.get("capability"); c != nil {
			v, err := scalarField(item, "spec.requires[].capability", "capability")
			if err != nil {
				return m, err
			}
			m.capabilities = append(m.capabilities, v)
			continue
		}
		if e := item.get("extension"); e != nil {
			v, err := scalarField(item, "spec.requires[].extension", "extension")
			if err != nil {
				return m, err
			}
			m.extensions = append(m.extensions, v)
			continue
		}
		if item.get("resource") == nil {
			m.unnamed = append(m.unnamed, i)
			continue
		}
		r, err := resourceRequirement(item)
		if err != nil {
			return m, err
		}
		m.declares.Requires = append(m.declares.Requires, r)
	}

	steps, err := mappingItems(spec.get("steps"), "spec.steps")
	if err != nil {
		return m, err
	}
	for _, item := range steps {
		s, err := step(item)
		if err != nil {
			return m, err
		}
		m.declares.Steps = append(m.declares.Steps, s)
	}

	provides := spec.get("provides")
	switch {
	case provides == nil || provides.kind == yamlNull:
	case provides.kind != yamlSeq:
		return m, unfollowable("spec.provides")
	default:
		for _, item := range provides.items {
			switch {
			case item.kind == yamlScalar || item.kind == yamlNull:
				// A provided capability slug, which a block running one still
				// lists, and which this suite does not judge.
			case item.followableMap():
				id, err := scalarField(item, "spec.provides[].step", "step")
				if err != nil {
					return m, err
				}
				m.declares.Offers = append(m.declares.Offers, contract.Offer{Step: id})
			default:
				return m, unfollowable("an item of spec.provides")
			}
		}
	}
	return m, nil
}

// mappingItems reads n as a block sequence of block mappings: absent and
// empty give none.
func mappingItems(n *yamlNode, field string) ([]*yamlNode, error) {
	if n == nil || n.kind == yamlNull {
		return nil, nil
	}
	if n.kind != yamlSeq {
		return nil, unfollowable(field)
	}
	for _, item := range n.items {
		if !item.followableMap() {
			return nil, unfollowable("an item of " + field)
		}
	}
	return n.items, nil
}

// resourceRequirement reads one resource entry of spec.requires.
func resourceRequirement(item *yamlNode) (contract.ResourceRequirement, error) {
	var r contract.ResourceRequirement
	var err error
	if r.Resource, err = scalarField(item, "spec.requires[].resource", "resource"); err != nil {
		return r, err
	}
	field := fmt.Sprintf("spec.requires[%s]", r.Resource)
	if r.Kind, err = scalarField(item, field+".kind", "kind"); err != nil {
		return r, err
	}
	if r.Type, err = scalarField(item, field+".type", "type"); err != nil {
		return r, err
	}
	if r.Reads, err = nameList(item.get("reads"), field+".reads"); err != nil {
		return r, err
	}
	many, err := scalarField(item, field+".many", "many")
	if err != nil {
		return r, err
	}
	switch many {
	case "", "false":
	case "true":
		r.Many = true
	default:
		return r, fmt.Errorf("%s.many is %q, want true or false", field, many)
	}
	return r, nil
}

// step reads one item of spec.steps.
func step(item *yamlNode) (contract.Step, error) {
	var s contract.Step
	var err error
	if s.ID, err = scalarField(item, "spec.steps[].id", "id"); err != nil {
		return s, err
	}
	field := fmt.Sprintf("spec.steps[%s]", s.ID)
	if s.Type, err = scalarField(item, field+".type", "type"); err != nil {
		return s, err
	}
	ownership, err := scalarField(item, field+".ownership", "ownership")
	if err != nil {
		return s, err
	}
	s.Ownership = contract.Ownership(ownership)
	inputs := item.get("inputs")
	switch {
	case inputs == nil || inputs.kind == yamlNull:
		return s, nil
	case !inputs.followableMap():
		return s, unfollowable(field + ".inputs")
	}
	s.Inputs = make(map[string]string, len(inputs.keys))
	for _, k := range inputs.keys {
		v, err := scalarField(inputs, field+".inputs."+k, k)
		if err != nil {
			return s, err
		}
		s.Inputs[k] = v
	}
	return s, nil
}

// steppedProblems returns one message per defect in a stepped manifest:
// each one contract.ValidateBlockSteps reports, and each capability entry,
// extension entry or dependsOn key ADR 0079 refuses beside spec.steps.
func steppedProblems(m steppedManifest) []string {
	var problems []string
	for _, c := range m.capabilities {
		problems = append(problems, fmt.Sprintf(
			"spec.requires: capability %q is refused in a manifest that declares spec.steps, which requires only resources and derives its capabilities", c))
	}
	for _, e := range m.extensions {
		problems = append(problems, fmt.Sprintf(
			"spec.requires: extension %q is refused in a manifest that declares spec.steps; a fan-in is a resource requirement with many: true", e))
	}
	for _, i := range m.unnamed {
		problems = append(problems, fmt.Sprintf(
			"spec.requires[%d]: names no resource; a manifest that declares spec.steps requires only resources", i))
	}
	if m.dependsOn {
		problems = append(problems,
			"spec.dependsOn is refused in a manifest that declares spec.steps; resource requirements order the stack")
	}
	if err := contract.ValidateBlockSteps(m.declares); err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			problems = append(problems, "spec.steps: "+line)
		}
	}
	return problems
}

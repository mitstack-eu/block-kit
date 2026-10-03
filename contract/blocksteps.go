package contract

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// A block lists the resources it creates as ordered steps, requires only
// resources, and offers a resource to the rest of its stack by naming the
// step that creates it. ADR 0079 fixes the shape; these are its one
// definition, which the platform's manifest parser and blockverify both read.

// ResourceRequirement is one entry of a block's spec.requires in a manifest
// that declares spec.steps: a resource that must exist before the block runs,
// which the block reads or uses but does not create.
type ResourceRequirement struct {
	// Resource names the requirement within the block. A step input binds
	// to its outputs as requires.<Resource>.outputs.<name>.
	Resource string `json:"resource"`
	// Kind matches any catalogued type of this kind that outputs every name
	// in Reads. Exactly one of Kind and Type is set.
	Kind string `json:"kind,omitempty"`
	// Type narrows the requirement to the one catalogued type with this ID.
	Type string `json:"type,omitempty"`
	// Reads names the outputs the block reads from the resource.
	Reads []string `json:"reads,omitempty"`
	// Many marks a requirement that collects every offer in the stack. A
	// requirement without it is met by exactly one source.
	Many bool `json:"many,omitempty"`
}

// Matches reports whether a resource of type t meets r: a narrowed
// requirement matches only the type it names, and a kind requirement matches
// a type of that kind that outputs every name r reads.
func (r ResourceRequirement) Matches(t ResourceType) bool {
	if r.Type != "" {
		return t.ID == r.Type
	}
	if r.Kind == "" || t.Kind != r.Kind {
		return false
	}
	for _, read := range r.Reads {
		if !slices.Contains(t.Outputs, read) {
			return false
		}
	}
	return true
}

// Offer is one `- step: <id>` entry of a block's spec.provides: the block
// offers the resource that step creates to the rest of its stack. A resource
// a block creates without offering it matches no requirement.
type Offer struct {
	// Step is the ID of the step whose resource is offered.
	Step string `json:"step"`
}

// BlockSteps is what a stepped manifest declares that ValidateBlockSteps
// judges: its param names, its steps in order, its resource requirements and
// its offers.
type BlockSteps struct {
	Params   []string              `json:"params,omitempty"`
	Steps    []Step                `json:"steps,omitempty"`
	Requires []ResourceRequirement `json:"requires,omitempty"`
	Offers   []Offer               `json:"offers,omitempty"`
}

// clone returns a copy of b that shares no backing array with it.
func (b BlockSteps) clone() BlockSteps {
	out := BlockSteps{
		Params:   append([]string{}, b.Params...),
		Steps:    make([]Step, len(b.Steps)),
		Requires: make([]ResourceRequirement, len(b.Requires)),
		Offers:   append([]Offer{}, b.Offers...),
	}
	for i, s := range b.Steps {
		if s.Inputs != nil {
			inputs := make(map[string]string, len(s.Inputs))
			for k, v := range s.Inputs {
				inputs[k] = v
			}
			s.Inputs = inputs
		}
		out.Steps[i] = s
	}
	for i, r := range b.Requires {
		r.Reads = append([]string{}, r.Reads...)
		out.Requires[i] = r
	}
	return out
}

// ValidateBlockSteps returns every defect in b joined into one error, or nil.
// Each message names the step, requirement or offer it is about. It checks
// that each step has a unique id, a catalogued type, a valid ownership and
// inputs bound to a declared param, an output of an earlier step's type or a
// name in the reads of a declared one-provider requirement; that each
// requirement has a unique id, exactly one of a catalogued kind and a
// catalogued type, and reads outputs one catalogued type of its kind, or its
// type, produces together; and that each offer names a declared step. No
// input binds a many-provider requirement, since ADR 0079 fixes no form for
// one.
func ValidateBlockSteps(b BlockSteps) error {
	var errs []error
	requires := map[string]ResourceRequirement{}
	for i, r := range b.Requires {
		if r.Resource == "" {
			errs = append(errs, fmt.Errorf("requirement at index %d: empty resource id", i))
			continue
		}
		if _, dup := requires[r.Resource]; dup {
			errs = append(errs, fmt.Errorf("requirement %q: listed more than once", r.Resource))
			continue
		}
		requires[r.Resource] = r
		if err := validateRequirement(r); err != nil {
			errs = append(errs, fmt.Errorf("requirement %q: %w", r.Resource, err))
		}
	}

	params := map[string]bool{}
	for _, p := range b.Params {
		params[p] = true
	}

	earlier := map[string]ResourceType{}
	declared := map[string]bool{}
	for _, s := range b.Steps {
		declared[s.ID] = true
	}
	for i, s := range b.Steps {
		if s.ID == "" {
			errs = append(errs, fmt.Errorf("step at index %d: empty id", i))
			continue
		}
		name := fmt.Sprintf("step %q", s.ID)
		if _, dup := earlier[s.ID]; dup {
			errs = append(errs, fmt.Errorf("%s: listed more than once", name))
			continue
		}
		t, catalogued := LookupResourceType(s.Type)
		if !catalogued {
			errs = append(errs, fmt.Errorf("%s: type %q is not in the resource-type catalogue", name, s.Type))
		}
		switch {
		case s.Ownership == "":
			errs = append(errs, fmt.Errorf("%s: no ownership, want one of %q", name, Ownerships()))
		case !s.Ownership.Valid():
			errs = append(errs, fmt.Errorf("%s: ownership %q is not one of %q", name, s.Ownership, Ownerships()))
		}
		for _, input := range sortedKeys(s.Inputs) {
			if err := validateBinding(s.Inputs[input], params, earlier, declared, requires); err != nil {
				errs = append(errs, fmt.Errorf("%s: input %q: %w", name, input, err))
			}
		}
		earlier[s.ID] = t
	}

	for i, o := range b.Offers {
		switch {
		case o.Step == "":
			errs = append(errs, fmt.Errorf("offer at index %d: names no step", i))
		case !declared[o.Step]:
			errs = append(errs, fmt.Errorf("offer of step %q: no step with that id is declared", o.Step))
		}
	}
	return errors.Join(errs...)
}

// validateRequirement checks one requirement whose id is set.
func validateRequirement(r ResourceRequirement) error {
	switch {
	case r.Kind == "" && r.Type == "":
		return fmt.Errorf("names neither a kind nor a type, want exactly one")
	case r.Kind != "" && r.Type != "":
		return fmt.Errorf("names both kind %q and type %q, want exactly one", r.Kind, r.Type)
	}
	if r.Type != "" {
		t, ok := LookupResourceType(r.Type)
		if !ok {
			return fmt.Errorf("type %q is not in the resource-type catalogue", r.Type)
		}
		for _, read := range r.Reads {
			if !slices.Contains(t.Outputs, read) {
				return fmt.Errorf("reads %q, which type %q does not output", read, r.Type)
			}
		}
		return nil
	}
	var ofKind []ResourceType
	for _, t := range resourceTypeCatalogue {
		if t.Kind == r.Kind {
			ofKind = append(ofKind, t)
		}
	}
	if len(ofKind) == 0 {
		return fmt.Errorf("no catalogued type is of kind %q", r.Kind)
	}
	for _, read := range r.Reads {
		if !slices.ContainsFunc(ofKind, func(t ResourceType) bool { return slices.Contains(t.Outputs, read) }) {
			return fmt.Errorf("reads %q, which no catalogued type of kind %q outputs", read, r.Kind)
		}
	}
	if !slices.ContainsFunc(ofKind, r.Matches) {
		return fmt.Errorf("reads %q, which no single catalogued type of kind %q outputs together, so nothing can meet it", r.Reads, r.Kind)
	}
	return nil
}

// validateBinding checks one input binding against the params, the steps
// before the one binding it, every declared step id, and the requirements.
func validateBinding(ref string, params map[string]bool, earlier map[string]ResourceType,
	declared map[string]bool, requires map[string]ResourceRequirement) error {
	parts := strings.Split(ref, ".")
	switch {
	case len(parts) == 2 && parts[0] == "params" && parts[1] != "":
		if !params[parts[1]] {
			return fmt.Errorf("binds param %q, which the manifest does not declare", parts[1])
		}
		return nil
	case len(parts) == 4 && parts[0] == "steps" && parts[2] == "outputs" && parts[1] != "" && parts[3] != "":
		t, ok := earlier[parts[1]]
		if !ok {
			if declared[parts[1]] {
				return fmt.Errorf("binds step %q, which is not an earlier step", parts[1])
			}
			return fmt.Errorf("binds step %q, which is not a declared earlier step", parts[1])
		}
		if t.ID != "" && !slices.Contains(t.Outputs, parts[3]) {
			return fmt.Errorf("binds output %q of step %q, which its type %q does not output", parts[3], parts[1], t.ID)
		}
		return nil
	case len(parts) == 4 && parts[0] == "requires" && parts[2] == "outputs" && parts[1] != "" && parts[3] != "":
		r, ok := requires[parts[1]]
		if !ok {
			return fmt.Errorf("binds requirement %q, which the manifest does not declare", parts[1])
		}
		if r.Many {
			return fmt.Errorf("binds requirement %q, which collects many offers; no binding form reads a many-provider requirement yet (ADR 0079)", parts[1])
		}
		if !slices.Contains(r.Reads, parts[3]) {
			return fmt.Errorf("binds output %q of requirement %q, which is not in its reads", parts[3], parts[1])
		}
		return nil
	}
	return fmt.Errorf("binding %q is none of params.<name>, steps.<id>.outputs.<name> or requires.<id>.outputs.<name>", ref)
}

// DeriveCapabilities returns the capabilities a block's steps and narrowed
// requirements fix, as ADR 0079 sets them: the sorted union of the place of
// each listed resource's type and of each narrowed requirement's type, where
// that place is a capability. A type that lives on a kind adds nothing, since its place
// is a resource, not a capability. A kind requirement adds nothing here,
// since the type that meets it is known only when the stack is checked. An
// uncatalogued type adds nothing either, which ValidateBlockSteps refuses.
// The result is never nil.
func DeriveCapabilities(steps []Step, requires []ResourceRequirement) []string {
	out := []string{}
	add := func(id string) {
		t, ok := LookupResourceType(id)
		if ok && isCapability(t.Place()) && !slices.Contains(out, t.Place()) {
			out = append(out, t.Place())
		}
	}
	for _, s := range steps {
		add(s.Type)
	}
	for _, r := range requires {
		if r.Type != "" {
			add(r.Type)
		}
	}
	slices.Sort(out)
	return out
}

// sortedKeys returns m's keys in order, so messages come out in a stable order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// StepsManifestGolden returns the manifest golden that declares spec.steps,
// resource requirements and an offer. It panics if the embedded file is
// missing, since that can only be a packaging bug in this module.
func StepsManifestGolden() []byte {
	raw, err := ManifestGoldens.ReadFile(ManifestGoldensDir + "/block-steps.yaml")
	if err != nil {
		panic("contract: embedded manifest golden missing: " + err.Error())
	}
	return raw
}

// stepsManifestGoldenDeclares is what StepsManifestGolden declares.
var stepsManifestGoldenDeclares = BlockSteps{
	Params: []string{"appName"},
	Requires: []ResourceRequirement{
		{Resource: "image", Kind: "container-image", Reads: []string{"imageRepo"}},
		{Resource: "workflows", Type: "github.workflow-file", Reads: []string{"workflowPath"}, Many: true},
	},
	Steps: []Step{
		{ID: "registry", Type: "aws.ecr-repository", Ownership: OwnershipCreated,
			Inputs: map[string]string{"name": "params.appName"}},
		{ID: "build-image", Type: "aws.ecr-image", Ownership: OwnershipCreated,
			Inputs: map[string]string{
				"registry": "steps.registry.outputs.registryHost",
				"base":     "requires.image.outputs.imageRepo",
			}},
	},
	Offers: []Offer{{Step: "build-image"}},
}

// StepsManifestGoldenDeclares returns the params, steps in order,
// requirements in order and offers StepsManifestGolden declares, so a test on
// either side can assert its parser reads exactly these. Each call returns a
// fresh copy the caller may modify.
func StepsManifestGoldenDeclares() BlockSteps {
	return stepsManifestGoldenDeclares.clone()
}

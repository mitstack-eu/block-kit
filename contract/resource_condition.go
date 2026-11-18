package contract

import (
	"fmt"
	"slices"
	"strings"
)

// TypeCatalogue is what a manifest's types are judged by: whether a type is served, and the inputs it takes.
type TypeCatalogue interface {
	Holds(typeID string) bool
	Inputs(typeID string) []SnapshotInput
}

// Inputs lists the inputs the type takes, and none for a type the snapshot does not hold.
func (s TypeSnapshot) Inputs(typeID string) []SnapshotInput {
	t, _ := s.Lookup(typeID)
	return t.Inputs
}

// blockInput is one input as the block takes it, merged over the types of its entries not marked many, since an entry marked many is asked for its types' many inputs instead.
type blockInput struct {
	declared bool
	// anyValue holds while no declaring type lists allowed values.
	anyValue bool
	allowed  []string
	// fallback is the default every declaring type gives, and empty when two differ.
	fallback string
}

func mergeInput(resources []ManifestResource, types TypeCatalogue, name string) blockInput {
	in := blockInput{anyValue: true}
	for _, r := range resources {
		if r.Many {
			continue
		}
		for _, id := range r.Types {
			for _, typeInput := range types.Inputs(id) {
				if typeInput.Name != name {
					continue
				}
				switch {
				case !in.declared:
					in.fallback = typeInput.Default
				case in.fallback != typeInput.Default:
					in.fallback = ""
				}
				in.declared = true
				if len(typeInput.AllowedValues) == 0 {
					continue
				}
				if in.anyValue {
					in.allowed, in.anyValue = slices.Clone(typeInput.AllowedValues), false
					continue
				}
				in.allowed = slices.DeleteFunc(in.allowed, func(v string) bool { return !slices.Contains(typeInput.AllowedValues, v) })
			}
		}
	}
	return in
}

// CheckResourceConditions refuses a condition naming an input the types of the block's entries not marked many do not declare or list allowed values for, and a listed value one of them does not allow.
func CheckResourceConditions(resources []ManifestResource, types TypeCatalogue) error {
	for i, r := range resources {
		if r.When == nil {
			continue
		}
		path := fmt.Sprintf("spec.resources[%d].when", i)
		in := mergeInput(resources, types, r.When.Input)
		switch {
		case !in.declared:
			return fmt.Errorf("%s: resource %q names input %q, which none of the block's resource types declares", path, r.Name, r.When.Input)
		case in.anyValue:
			return fmt.Errorf("%s: resource %q names input %q, which declares no allowed values to check the listed ones against", path, r.Name, r.When.Input)
		}
		for _, v := range r.When.Values {
			if !slices.Contains(in.allowed, v) {
				return fmt.Errorf("%s: resource %q is created for value %q of input %q, which allows only %s", path, r.Name, v, r.When.Input, strings.Join(in.allowed, ", "))
			}
		}
	}
	return nil
}

// IncludedResources keeps each entry with no condition, and each whose condition lists the request's value of its input, read from params or else from the default every declaring type gives.
func IncludedResources(resources []ManifestResource, types TypeCatalogue, params map[string]string) []ManifestResource {
	var out []ManifestResource
	for _, r := range resources {
		if r.When == nil {
			out = append(out, r)
			continue
		}
		value := params[r.When.Input]
		if value == "" {
			value = mergeInput(resources, types, r.When.Input).fallback
		}
		if value != "" && slices.Contains(r.When.Values, value) {
			out = append(out, r)
		}
	}
	return out
}

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

// CheckResourceConditions refuses, in a block carrying a condition, an entry not marked many listing a type the catalogue does not hold, a condition naming an input the types of those entries do not declare or list allowed values for, and a listed value one of them does not allow.
func CheckResourceConditions(resources []ManifestResource, types TypeCatalogue) error {
	if !slices.ContainsFunc(resources, func(r ManifestResource) bool { return r.When != nil }) {
		return nil
	}
	if err := refuseUnheldTypes(resources, types); err != nil {
		return err
	}
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

// refuseUnheldTypes refuses a type the catalogue does not hold, since a condition would read none of its inputs and so judge the block's inputs without it.
func refuseUnheldTypes(resources []ManifestResource, types TypeCatalogue) error {
	for i, r := range resources {
		if r.Many {
			continue
		}
		for _, id := range r.Types {
			if !types.Holds(id) {
				return fmt.Errorf("spec.resources[%d]: resource %q lists type %q, which the type snapshot does not hold, so a condition cannot read the inputs it declares", i, r.Name, id)
			}
		}
	}
	return nil
}

// IncludedResources keeps each entry with no condition, and each whose condition lists the request's value of its input, read from params or else from the default every declaring type gives. It refuses a request giving no value where the types give no one default, and a value the input does not allow, since either leaves unknown which entries the block holds.
func IncludedResources(resources []ManifestResource, types TypeCatalogue, params map[string]string) ([]ManifestResource, error) {
	var out []ManifestResource
	for _, r := range resources {
		if r.When == nil {
			out = append(out, r)
			continue
		}
		in := mergeInput(resources, types, r.When.Input)
		value := params[r.When.Input]
		switch {
		case value == "" && in.fallback == "":
			return nil, fmt.Errorf("resource %q is created only for some values of input %q, but the request gives it no value and its types give no one default", r.Name, r.When.Input)
		case value == "":
			value = in.fallback
		case !in.anyValue && !slices.Contains(in.allowed, value):
			return nil, fmt.Errorf("resource %q is created only for some values of input %q, but the request gives it %q, which the input does not allow (it allows %s)", r.Name, r.When.Input, value, strings.Join(in.allowed, ", "))
		}
		if slices.Contains(r.When.Values, value) {
			out = append(out, r)
		}
	}
	return out, nil
}

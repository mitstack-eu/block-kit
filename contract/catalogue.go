package contract

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
)

// CatalogueMajor is the major version of the catalogue this release ships, raised by one for a breaking change.
const CatalogueMajor = 1

// Catalogue is the resource-type catalogue as a release ships it.
type Catalogue struct {
	Major int             `json:"major"`
	Types []CatalogueType `json:"types"`
}

// CatalogueType is one type of a Catalogue.
type CatalogueType struct {
	ID      string            `json:"id"`
	Kind    string            `json:"kind"`
	Inputs  []CatalogueInput  `json:"inputs"`
	Outputs []CatalogueOutput `json:"outputs"`
}

// CatalogueInput is one input of a CatalogueType.
type CatalogueInput struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
}

// CatalogueOutput is one output of a CatalogueType.
type CatalogueOutput struct {
	Name   string `json:"name"`
	Secret bool   `json:"secret"`
}

// BreakReason names the way a catalogue change breaks a consumer.
type BreakReason string

const (
	// BreakTypeRemoved includes a renamed type.
	BreakTypeRemoved         BreakReason = "type-removed"
	BreakKindChanged         BreakReason = "kind-changed"
	BreakOutputRemoved       BreakReason = "output-removed"
	BreakOutputSecretChanged BreakReason = "output-secret-changed"
	// BreakRequiredInputAdded is an input required now but absent or optional before.
	BreakRequiredInputAdded BreakReason = "required-input-added"
)

// Break is one breaking change between two catalogues.
type Break struct {
	Type   string
	Reason BreakReason
	// Output is set for an output break and "" otherwise.
	Output string
	// Input is set for BreakRequiredInputAdded and "" otherwise.
	Input string
}

// String names the break's type and reason, and its output or input when set.
func (b Break) String() string {
	switch {
	case b.Output != "":
		return fmt.Sprintf("type %q: %s: output %q", b.Type, b.Reason, b.Output)
	case b.Input != "":
		return fmt.Sprintf("type %q: %s: input %q", b.Type, b.Reason, b.Input)
	}
	return fmt.Sprintf("type %q: %s", b.Type, b.Reason)
}

// CurrentCatalogue returns the catalogue this release ships, with every list sorted by name and never nil.
func CurrentCatalogue() Catalogue {
	rts := ResourceTypes()
	c := Catalogue{Major: CatalogueMajor, Types: make([]CatalogueType, 0, len(rts))}
	for _, rt := range rts {
		kind, _ := LookupKind(rt.Kind)
		ct := CatalogueType{
			ID:      rt.ID,
			Kind:    rt.Kind,
			Inputs:  make([]CatalogueInput, 0, len(rt.Inputs)),
			Outputs: make([]CatalogueOutput, 0, len(rt.Outputs)),
		}
		for _, in := range rt.Inputs {
			ct.Inputs = append(ct.Inputs, CatalogueInput{Name: in.Name, Required: in.Required})
		}
		for _, name := range rt.Outputs {
			ct.Outputs = append(ct.Outputs, CatalogueOutput{Name: name, Secret: kindMarksSecret(kind, name)})
		}
		c.Types = append(c.Types, ct)
	}
	return sortedCatalogue(c)
}

func kindMarksSecret(k Kind, name string) bool {
	outputs := k.outputs()
	i := slices.IndexFunc(outputs, func(o KindOutput) bool { return o.Name == name })
	return i >= 0 && outputs[i].Secret
}

// MarshalCatalogue returns c as sorted, indented JSON with a trailing newline, writing an empty list as [].
func MarshalCatalogue(c Catalogue) []byte {
	raw, err := json.MarshalIndent(sortedCatalogue(c), "", "  ")
	if err != nil {
		panic("contract: a catalogue holds only strings, ints and bools, so it always marshals: " + err.Error())
	}
	return append(raw, '\n')
}

// sortedCatalogue returns a copy sharing no list with c, with every list non-nil.
func sortedCatalogue(c Catalogue) Catalogue {
	types := make([]CatalogueType, len(c.Types))
	for i, ct := range c.Types {
		ct.Inputs = append([]CatalogueInput{}, ct.Inputs...)
		slices.SortFunc(ct.Inputs, func(a, b CatalogueInput) int { return cmp.Compare(a.Name, b.Name) })
		ct.Outputs = append([]CatalogueOutput{}, ct.Outputs...)
		slices.SortFunc(ct.Outputs, func(a, b CatalogueOutput) int { return cmp.Compare(a.Name, b.Name) })
		types[i] = ct
	}
	slices.SortFunc(types, func(a, b CatalogueType) int { return cmp.Compare(a.ID, b.ID) })
	c.Types = types
	return c
}

// ParseCatalogue decodes a catalogue as MarshalCatalogue writes it.
func ParseCatalogue(raw []byte) (Catalogue, error) {
	var c Catalogue
	if err := json.Unmarshal(raw, &c); err != nil {
		return Catalogue{}, fmt.Errorf("contract: parsing the catalogue: %w", err)
	}
	return c, nil
}

// CatalogueMajorRaised reports whether current is exactly one above golden, and errors on any other difference.
func CatalogueMajorRaised(golden, current int) (bool, error) {
	switch current {
	case golden:
		return false, nil
	case golden + 1:
		return true, nil
	}
	return false, fmt.Errorf("contract: catalogue major %d is neither the golden's major %d nor exactly one above it", current, golden)
}

// CompareCatalogues returns the breaking changes from previous to current, or nothing when majorRaised is true; a removed type is one break with no output break under it.
func CompareCatalogues(previous, current Catalogue, majorRaised bool) []Break {
	if majorRaised {
		return nil
	}
	now := make(map[string]CatalogueType, len(current.Types))
	for _, ct := range sortedCatalogue(current).Types {
		now[ct.ID] = ct
	}
	var breaks []Break
	for _, was := range sortedCatalogue(previous).Types {
		is, ok := now[was.ID]
		if !ok {
			breaks = append(breaks, Break{Type: was.ID, Reason: BreakTypeRemoved})
			continue
		}
		breaks = append(breaks, typeBreaks(was, is)...)
	}
	return breaks
}

func typeBreaks(was, is CatalogueType) []Break {
	var breaks []Break
	if was.Kind != is.Kind {
		breaks = append(breaks, Break{Type: was.ID, Reason: BreakKindChanged})
	}
	for _, out := range was.Outputs {
		i := slices.IndexFunc(is.Outputs, func(o CatalogueOutput) bool { return o.Name == out.Name })
		switch {
		case i < 0:
			breaks = append(breaks, Break{Type: was.ID, Reason: BreakOutputRemoved, Output: out.Name})
		case is.Outputs[i].Secret != out.Secret:
			breaks = append(breaks, Break{Type: was.ID, Reason: BreakOutputSecretChanged, Output: out.Name})
		}
	}
	for _, in := range is.Inputs {
		if !in.Required {
			continue
		}
		i := slices.IndexFunc(was.Inputs, func(w CatalogueInput) bool { return w.Name == in.Name })
		if i < 0 || !was.Inputs[i].Required {
			breaks = append(breaks, Break{Type: was.ID, Reason: BreakRequiredInputAdded, Input: in.Name})
		}
	}
	return breaks
}

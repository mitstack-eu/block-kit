package contract

import (
	"fmt"
	"strings"
)

// CheckManifestTypes refuses a requirement or resource of spec listing a type snap does not hold.
func CheckManifestTypes(spec ManifestSpec, snap TypeSnapshot) error {
	held := func(id string) bool { _, ok := snap.Lookup(id); return ok }
	return checkTypes(spec, held, fmt.Sprintf("the platform's type snapshot version %d", snap.Version))
}

// bindingOutputs splits at the first occurrence because a reference and an output may each hold dots.
const bindingOutputs = ".outputs."

// CheckBinding judges one requires.<reference>.outputs.<output> or resources.<name>.outputs.<output> binding against spec and snap.
func CheckBinding(spec ManifestSpec, snap TypeSnapshot, binding string) error {
	source, rest, _ := strings.Cut(binding, ".")
	ref, output, ok := strings.Cut(rest, bindingOutputs)
	if !ok || ref == "" || output == "" || (source != "requires" && source != "resources") {
		return fmt.Errorf("binding %q is not requires.<reference>.outputs.<output> or resources.<name>.outputs.<output>", binding)
	}
	var types []string
	if source == "requires" {
		found := false
		for _, q := range spec.Requires {
			if q.Reference() == ref {
				types, found = q.Types, true
				break
			}
		}
		if !found {
			return fmt.Errorf("binding %s: %q names no requirement in spec.requires, where a requirement is named by its name when set and by its type otherwise", binding, ref)
		}
	} else {
		found := false
		for _, r := range spec.Resources {
			if r.Name == ref {
				types, found = r.Types, true
				break
			}
		}
		if !found {
			return fmt.Errorf("binding %s: %q names no resource in spec.resources", binding, ref)
		}
	}
	for _, id := range types {
		t, ok := snap.Lookup(id)
		if !ok {
			return fmt.Errorf("binding %s: type %s is not in the platform's type snapshot version %d", binding, id, snap.Version)
		}
		if _, ok := t.Output(output); !ok {
			return fmt.Errorf("binding %s: type %s does not declare output %q", binding, id, output)
		}
	}
	return nil
}

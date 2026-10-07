package contract

import (
	"fmt"
	"strings"
)

// CheckManifestTypes refuses a requirement or resource of spec listing a
// type snap does not hold, naming the entry's path and the type id.
func CheckManifestTypes(spec ManifestSpec, snap TypeSnapshot) error {
	for i, q := range spec.Requires {
		if err := checkTypesHeld(fmt.Sprintf("spec.requires[%d]", i), q.Types, snap); err != nil {
			return err
		}
	}
	for i, r := range spec.Resources {
		if err := checkTypesHeld(fmt.Sprintf("spec.resources[%d]", i), r.Types, snap); err != nil {
			return err
		}
	}
	return nil
}

func checkTypesHeld(path string, types []string, snap TypeSnapshot) error {
	for _, id := range types {
		if _, ok := snap.Lookup(id); !ok {
			return fmt.Errorf("%s: type %s is not in the platform's type snapshot version %d", path, id, snap.Version)
		}
	}
	return nil
}

// bindingOutputs separates a binding's reference from its output. A
// reference and an output may each hold dots, so the first ".outputs."
// splits them.
const bindingOutputs = ".outputs."

// CheckBinding judges one binding a block's code reads,
// requires.<reference>.outputs.<output> or resources.<name>.outputs.<output>,
// against spec and snap. It refuses a reference no requirement of spec has,
// a requirement being named by its Reference, a name no resource of spec
// has, and an output any type the requirement or resource lists does not
// declare in snap, naming the type and the output.
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

package contract

import (
	"fmt"
	"reflect"
)

// blockManifestType is the manifest type under ManifestAPIVersion, from
// which the decoder and the JSON Schema both read the shape.
var blockManifestType = reflect.TypeOf(BlockManifest{})

// inputValueType is read as a scalar or a mapping of inputs rather than as
// a struct of fields.
var inputValueType = reflect.TypeOf(InputValue{})

// v1betaDecoder reads a ManifestAPIVersion manifest by the json tags the
// JSON Schema is generated from, refusing every null, and every plain
// scalar YAML reads as a number or a boolean where a string is declared.
var v1betaDecoder = manifestDecoder{version: ManifestAPIVersion, tag: "json"}

// decodeBlockManifest reads root strictly into a BlockManifest, then holds
// it to what the types cannot say: type is "block", a resource name appears
// once, and every spec.provides entry names a resource.
func decodeBlockManifest(root *yamlNode) (*BlockManifest, error) {
	b := &BlockManifest{}
	if err := v1betaDecoder.decode(root, reflect.ValueOf(b).Elem(), ""); err != nil {
		return nil, err
	}
	if b.Type != "block" {
		return nil, fmt.Errorf("type is %q, but the type of a %s manifest must be \"block\"", b.Type, ManifestAPIVersion)
	}
	names := make(map[string]bool, len(b.Spec.Resources))
	for i, r := range b.Spec.Resources {
		if names[r.Name] {
			return nil, fmt.Errorf("spec.resources[%d].name: %q names an earlier resource too, and a resource name appears once in a block", i, r.Name)
		}
		names[r.Name] = true
	}
	for i, p := range b.Spec.Provides {
		if !names[p] {
			return nil, fmt.Errorf("spec.provides[%d]: %q names no resource in spec.resources", i, p)
		}
	}
	return b, nil
}

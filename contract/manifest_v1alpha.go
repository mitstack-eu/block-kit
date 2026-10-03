package contract

import (
	"fmt"
	"strings"
)

// readV1Alpha reads from a v1alpha manifest's root mapping the fields
// blockverify's manifest suite checks: metadata.id, spec.version,
// spec.releaseClass, spec.releaseNote, spec.outputs and spec.requires with
// each item's capability and reads. It reads nothing else, so a field it
// does not know is never refused. Where it reads, a flow mapping, an alias,
// a merge key or a misplaced line is refused, since what it declares is
// unknown.
func readV1Alpha(root *yamlNode) (*V1AlphaManifest, error) {
	spec, err := topLevelMap(root, "spec")
	if err != nil {
		return nil, err
	}
	m, err := v1alphaSpec(spec)
	if err != nil {
		return nil, err
	}
	metadata, err := topLevelMap(root, "metadata")
	if err != nil {
		return nil, err
	}
	if m.ID, err = rawScalarField(metadata, "metadata.id", "id"); err != nil {
		return nil, err
	}
	return m, nil
}

// topLevelMap returns the mapping under key in root, or nil when it is
// absent or empty.
func topLevelMap(root *yamlNode, key string) (*yamlNode, error) {
	n := root.get(key)
	if n == nil || n.kind == yamlNull {
		return nil, nil
	}
	if !n.followableMap() {
		return nil, unfollowable(key)
	}
	return n, nil
}

// v1alphaSpec reads the fields the manifest suite checks from spec.
func v1alphaSpec(spec *yamlNode) (*V1AlphaManifest, error) {
	m := &V1AlphaManifest{}
	var err error
	if m.Version, err = scalarField(spec, "spec.version", "version"); err != nil {
		return nil, err
	}
	if m.ReleaseClass, err = scalarField(spec, "spec.releaseClass", "releaseClass"); err != nil {
		return nil, err
	}
	if m.ReleaseNote, err = scalarField(spec, "spec.releaseNote", "releaseNote"); err != nil {
		return nil, err
	}
	if m.Outputs, err = nameList(spec.get("outputs"), "spec.outputs"); err != nil {
		return nil, err
	}
	requires := spec.get("requires")
	switch {
	case requires == nil || requires.kind == yamlNull:
		return m, nil
	case requires.kind != yamlSeq:
		return nil, unfollowable("spec.requires")
	}
	for _, item := range requires.items {
		if !item.followableMap() {
			return nil, unfollowable("an item of spec.requires")
		}
		capability, err := scalarField(item, "spec.requires[].capability", "capability")
		if err != nil {
			return nil, err
		}
		reads, err := nameList(item.get("reads"), fmt.Sprintf("spec.requires[%s].reads", capability))
		if err != nil {
			return nil, err
		}
		m.Requires = append(m.Requires, V1AlphaRequirement{Capability: capability, Reads: reads})
	}
	return m, nil
}

// scalarField returns the trimmed scalar under key in n, or "" when the key
// is absent or empty. field names it in an error.
func scalarField(n *yamlNode, field, key string) (string, error) {
	v, err := rawScalarField(n, field, key)
	return strings.TrimSpace(v), err
}

// rawScalarField is scalarField without the trim, for a value whose every
// character is judged, as an id is.
func rawScalarField(n *yamlNode, field, key string) (string, error) {
	if n == nil {
		return "", nil
	}
	return scalarValue(n.get(key), field)
}

// scalarValue returns the scalar n holds, or "" when n is absent or null.
// field names it in an error.
func scalarValue(v *yamlNode, field string) (string, error) {
	switch {
	case v == nil || v.kind == yamlNull:
		return "", nil
	case v.kind == yamlScalar:
		return v.value, nil
	case v.reserved != 0:
		return "", fmt.Errorf("%s starts with %q, which YAML does not allow at the start of an unquoted value, so quote it, as in id: \"@acme/billing-db\"", field, v.reserved)
	}
	return "", unfollowable(field)
}

// nameList reads n as a list of names: absent and empty give none.
func nameList(n *yamlNode, field string) ([]string, error) {
	if n == nil || n.kind == yamlNull {
		return nil, nil
	}
	if n.kind != yamlSeq {
		return nil, unfollowable(field)
	}
	names := make([]string, 0, len(n.items))
	for _, item := range n.items {
		switch item.kind {
		case yamlNull:
			names = append(names, "")
		case yamlScalar:
			names = append(names, strings.TrimSpace(item.value))
		default:
			return nil, unfollowable("an item of " + field)
		}
	}
	return names, nil
}

package contract

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

var blockManifestType = reflect.TypeOf(BlockManifest{})

// requirementType and resourceType are read as a type id or a mapping, not as a struct of fields.
var (
	requirementType = reflect.TypeOf(ManifestRequirement{})
	resourceType    = reflect.TypeOf(ManifestResource{})
)

// v1betaDecoder refuses every null, and every plain scalar YAML reads as a number or a boolean where a string is declared.
var v1betaDecoder = manifestDecoder{version: ManifestAPIVersion, tag: "json"}

// typeIDPattern is the grammar the JSON Schema states; typeID also holds an id to ParseResourceTypeID, which bounds each part's length.
var typeIDPattern = regexp.MustCompile(`^(@[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?/)?[a-z][a-z0-9]*(-[a-z0-9]+)*\.[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// decodeBlockManifest also holds what the types cannot say: type is block or capability, names and references are unique, no resource name holds a slash, and every provides entry names a resource not marked many.
func decodeBlockManifest(root *yamlNode) (*BlockManifest, error) {
	b := &BlockManifest{}
	if err := v1betaDecoder.decode(root, reflect.ValueOf(b).Elem(), ""); err != nil {
		return nil, err
	}
	if b.Type != ManifestTypeBlock && b.Type != ManifestTypeCapability {
		return nil, fmt.Errorf("type is %q, but the type of a %s manifest must be %q or %q",
			b.Type, ManifestAPIVersion, ManifestTypeBlock, ManifestTypeCapability)
	}
	if b.Type == ManifestTypeBlock && len(b.Spec.Resources) == 0 {
		return nil, fmt.Errorf("spec.resources is required and must not be empty")
	}
	if err := checkMarkShapes(b.Spec.Presentation); err != nil {
		return nil, err
	}
	markedMany := make(map[string]bool, len(b.Spec.Resources))
	for i, r := range b.Spec.Resources {
		if strings.Contains(r.Name, "/") && r.Name == r.Type {
			return nil, fmt.Errorf("spec.resources[%d]: %q is a private type, whose id holds a slash, so give the resource a name", i, r.Type)
		}
		if strings.Contains(r.Name, "/") {
			return nil, fmt.Errorf("spec.resources[%d].name: %q holds a slash, which is kept for the members of an entry marked many, named <entry>/<key>", i, r.Name)
		}
		if _, ok := markedMany[r.Name]; ok {
			return nil, fmt.Errorf("spec.resources[%d].name: %q names an earlier resource too, and a resource name appears once in a block", i, r.Name)
		}
		markedMany[r.Name] = r.Many
	}
	refs := make(map[string]int, len(b.Spec.Requires))
	for i, q := range b.Spec.Requires {
		ref := q.Reference()
		if j, ok := refs[ref]; ok {
			return nil, fmt.Errorf("spec.requires[%d]: %q is the reference of spec.requires[%d] too, and a requirement's reference appears once in a block", i, ref, j)
		}
		refs[ref] = i
	}
	for i, p := range b.Spec.Provides {
		marked, ok := markedMany[p]
		if !ok {
			return nil, fmt.Errorf("spec.provides[%d]: %q names no resource in spec.resources", i, p)
		}
		if marked {
			return nil, fmt.Errorf("spec.provides[%d]: %q is marked many, and an entry marked many is never provided, since a binding names one resource", i, p)
		}
	}
	return b, nil
}

type manifestEntry struct {
	name  string
	types []string
	many  bool
}

func (d manifestDecoder) requirement(n *yamlNode, path string) (ManifestRequirement, error) {
	e, err := d.entry(n, path)
	if err != nil {
		return ManifestRequirement{}, err
	}
	return ManifestRequirement{Name: e.name, Types: e.types, Many: e.many}, nil
}

// resource names an entry without a name by the one type id it lists.
func (d manifestDecoder) resource(n *yamlNode, path string) (ManifestResource, error) {
	e, err := d.entry(n, path)
	if err != nil {
		return ManifestResource{}, err
	}
	r := ManifestResource{Name: e.name, Types: e.types, Many: e.many}
	if len(e.types) == 1 {
		r.Type = e.types[0]
		if r.Name == "" {
			r.Name = r.Type
		}
	}
	return r, nil
}

// entry refuses a key that is not a field of an entry, and an entry listing several types without a name.
func (d manifestDecoder) entry(n *yamlNode, path string) (manifestEntry, error) {
	if n.kind == yamlScalar {
		id, err := typeID(n, path)
		return manifestEntry{types: []string{id}}, err
	}
	if n.kind != yamlMap {
		return manifestEntry{}, fmt.Errorf("%s must be a type id or a mapping of type and name", path)
	}
	if err := mapEntries(n, path); err != nil {
		return manifestEntry{}, err
	}
	var e manifestEntry
	hasType := false
	for i, key := range n.keys {
		keyPath := fieldPath(path, key)
		val := n.vals[i]
		var err error
		switch {
		case key == "type":
			hasType = true
			e.types, err = typeList(val, keyPath)
		case key == "name":
			err = d.decode(val, reflect.ValueOf(&e.name).Elem(), keyPath)
			if err == nil && e.name == "" {
				err = fmt.Errorf("%s must not be empty; leave it out to name the entry by its type", keyPath)
			}
		case key == "many":
			err = d.decode(val, reflect.ValueOf(&e.many).Elem(), keyPath)
		default:
			err = fmt.Errorf("%s is not a field of a %s manifest", keyPath, d.version)
		}
		if err != nil {
			return manifestEntry{}, err
		}
	}
	if !hasType {
		return manifestEntry{}, fmt.Errorf("%s is required", fieldPath(path, "type"))
	}
	if len(e.types) > 1 && e.name == "" {
		return manifestEntry{}, fmt.Errorf("%s lists types %s but has no name; an entry listing more than one type needs a name to be told apart by",
			path, strings.Join(e.types, ", "))
	}
	return e, nil
}

func typeList(n *yamlNode, path string) ([]string, error) {
	if err := readable(n, path); err != nil {
		return nil, err
	}
	switch {
	case isNull(n):
		return nil, noValue(path)
	case n.kind == yamlScalar:
		id, err := typeID(n, path)
		return []string{id}, err
	case n.kind != yamlSeq:
		return nil, fmt.Errorf("%s must be a type id or a list of type ids", path)
	case len(n.items) == 0:
		return nil, fmt.Errorf("%s is required and must not be empty", path)
	}
	ids := make([]string, 0, len(n.items))
	for i, item := range n.items {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		if err := readable(item, itemPath); err != nil {
			return nil, err
		}
		if item.kind != yamlScalar || isNull(item) {
			return nil, fmt.Errorf("%s must be a type id", itemPath)
		}
		id, err := typeID(item, itemPath)
		if err != nil {
			return nil, err
		}
		if slices.Contains(ids, id) {
			return nil, fmt.Errorf("%s: type %s is listed twice", path, id)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func typeID(n *yamlNode, path string) (string, error) {
	if _, _, _, err := ParseResourceTypeID(n.value); err != nil || !typeIDPattern.MatchString(n.value) {
		return "", fmt.Errorf("%s: %q is not a type id, which is a community type <capability>.<name> or an org's private type "+
			"@<org>/<capability>.<name>, where org and capability are each 1 to 40 lowercase letters, digits and dashes, "+
			"and capability and name are lowercase kebab case that starts with a letter", path, n.value)
	}
	return n.value, nil
}

func checkMarkShapes(p *ManifestPresentation) error {
	if p == nil {
		return nil
	}
	for i, shape := range p.Mark {
		held := 0
		for _, set := range []bool{shape.Path != nil, shape.Circle != nil, shape.Rect != nil, shape.Line != nil,
			shape.Polyline != nil, shape.Polygon != nil, shape.Ellipse != nil} {
			if set {
				held++
			}
		}
		if held != 1 {
			return fmt.Errorf("spec.presentation.mark[%d] must hold exactly one shape, a path, circle, rect, line, polyline, polygon or ellipse, and holds %d", i, held)
		}
	}
	return nil
}

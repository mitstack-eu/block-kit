package contract

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

// blockManifestType is the manifest type under ManifestAPIVersion, from
// which the decoder and the JSON Schema both read the shape.
var blockManifestType = reflect.TypeOf(BlockManifest{})

// inputValueType is read as a scalar or a mapping of inputs rather than as
// a struct of fields.
var inputValueType = reflect.TypeOf(InputValue{})

// The YAML 1.2 core schema reads a plain scalar matching one of these as a
// null, a boolean or a number rather than as a string.
var (
	plainNull    = regexp.MustCompile(`^(null|Null|NULL|~)$`)
	plainBoolean = regexp.MustCompile(`^(true|True|TRUE|false|False|FALSE)$`)
	plainNumber  = regexp.MustCompile(`^([-+]?[0-9]+|0o[0-7]+|0x[0-9a-fA-F]+|` +
		`[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?|[-+]?\.(inf|Inf|INF)|\.(nan|NaN|NAN))$`)
)

// isNull reports whether YAML reads n as a null.
func isNull(n *yamlNode) bool {
	return n.kind == yamlNull || n.kind == yamlScalar && !n.str && plainNull.MatchString(n.value)
}

// manifestField is one field of a manifest struct as its json tag names it.
type manifestField struct {
	name  string
	index int
	// required is a field whose tag has no omitempty. A required field must
	// be present and, for a string or a list, not empty; the schema says so
	// with minLength and minItems.
	required bool
	// oneOrList is a list field that also takes a single value, as a
	// requirement's in takes one capability or a list of them.
	oneOrList bool
}

// manifestFields returns t's fields in declaration order, by json name.
func manifestFields(t reflect.Type) []manifestField {
	fields := make([]manifestField, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		fields = append(fields, manifestField{
			name:      name,
			index:     i,
			required:  opts != "omitempty",
			oneOrList: t == reflect.TypeOf(ManifestRequirement{}) && f.Name == "In",
		})
	}
	return fields
}

// decodeBlockManifest reads root strictly into a BlockManifest, then holds
// it to what the types cannot say: type is "block", a resource name appears
// once, and every spec.provides entry names a resource.
func decodeBlockManifest(root *yamlNode) (*BlockManifest, error) {
	b := &BlockManifest{}
	if err := decodeValue(root, reflect.ValueOf(b).Elem(), ""); err != nil {
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

// fieldPath names key under path.
func fieldPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// describe names path in an error, the document itself when path is empty.
func describe(path string) string {
	if path == "" {
		return "the document"
	}
	return path
}

// readable refuses a node the reader could not follow, naming path.
func readable(n *yamlNode, path string) error {
	switch {
	case n.alias:
		return fmt.Errorf("%s is an alias, which the reader does not follow, so what it declares is unknown", describe(path))
	case n.reserved != 0:
		return fmt.Errorf("%s starts with %q, which YAML does not allow at the start of an unquoted value, so quote it", describe(path), n.reserved)
	case n.kind == yamlOpaque:
		return fmt.Errorf("%s is in a form the reader cannot follow (an unclosed quote or flow collection, or a key inside a value), so what it declares is unknown", describe(path))
	case n.misplaced:
		return fmt.Errorf("%s holds a line that is neither one of its keys nor inside one of their values", describe(path))
	}
	return nil
}

// noValue refuses a null, which the schema allows nowhere: a key is left
// out or given a value.
func noValue(path string) error {
	return fmt.Errorf("%s has no value, so leave it out or give it one", describe(path))
}

// decodeValue reads n into v. A null is refused, and so is a plain scalar
// YAML reads as a number or a boolean where v takes a string; quoting it
// makes it a string.
func decodeValue(n *yamlNode, v reflect.Value, path string) error {
	if err := readable(n, path); err != nil {
		return err
	}
	if isNull(n) {
		return noValue(path)
	}
	if v.Type() == inputValueType {
		in, err := decodeInput(n, path)
		v.Set(reflect.ValueOf(in))
		return err
	}
	switch v.Kind() {
	case reflect.String:
		if n.kind != yamlScalar {
			return fmt.Errorf("%s must be one value, not a mapping or a list", path)
		}
		if !n.str && (plainBoolean.MatchString(n.value) || plainNumber.MatchString(n.value)) {
			return fmt.Errorf("%s is %s, which YAML reads as a number or a boolean, but it takes a string, so quote it", path, n.value)
		}
		v.SetString(n.value)
	case reflect.Bool:
		if n.kind != yamlScalar || n.str || n.value != "true" && n.value != "false" {
			return fmt.Errorf("%s takes true or false only", path)
		}
		v.SetBool(n.value == "true")
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		if err := decodeValue(n, p.Elem(), path); err != nil {
			return err
		}
		v.Set(p)
	case reflect.Slice:
		return decodeList(n, v, path, false)
	case reflect.Map:
		return decodeMap(n, v, path)
	case reflect.Struct:
		return decodeStruct(n, v, path)
	default:
		return fmt.Errorf("%s has a Go type, %s, the manifest decoder does not read", path, v.Type())
	}
	return nil
}

// decodeList reads the list n into the slice v. oneOrList also takes a
// single value as a one-item list.
func decodeList(n *yamlNode, v reflect.Value, path string, oneOrList bool) error {
	if oneOrList && n.kind == yamlScalar {
		n = &yamlNode{kind: yamlSeq, items: []*yamlNode{n}}
	}
	if n.kind != yamlSeq {
		if oneOrList {
			return fmt.Errorf("%s must be one value or a list", path)
		}
		return fmt.Errorf("%s must be a list", path)
	}
	list := reflect.MakeSlice(v.Type(), len(n.items), len(n.items))
	for i, item := range n.items {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		if err := decodeValue(item, list.Index(i), itemPath); err != nil {
			return err
		}
	}
	v.Set(list)
	return nil
}

// mapEntries checks that n is a mapping with no merge key and no key
// written twice, naming path.
func mapEntries(n *yamlNode, path string) error {
	if n.kind != yamlMap {
		return fmt.Errorf("%s must be a mapping", describe(path))
	}
	seen := make(map[string]bool, len(n.keys))
	for _, key := range n.keys {
		switch {
		case key == "<<":
			return fmt.Errorf("%s holds a merge key, which the reader does not follow, so what it declares is unknown", describe(path))
		case seen[key]:
			return fmt.Errorf("%s is written twice", fieldPath(path, key))
		}
		seen[key] = true
	}
	return nil
}

// decodeMap reads the mapping n into the map v.
func decodeMap(n *yamlNode, v reflect.Value, path string) error {
	if err := mapEntries(n, path); err != nil {
		return err
	}
	m := reflect.MakeMapWithSize(v.Type(), len(n.keys))
	for i, key := range n.keys {
		keyPath := fieldPath(path, key)
		elem := reflect.New(v.Type().Elem()).Elem()
		if err := decodeValue(n.vals[i], elem, keyPath); err != nil {
			return err
		}
		m.SetMapIndex(reflect.ValueOf(key), elem)
	}
	v.Set(m)
	return nil
}

// decodeInput reads one resource input: a scalar, which may be a number or
// a boolean, or a mapping of named inputs.
func decodeInput(n *yamlNode, path string) (InputValue, error) {
	switch n.kind {
	case yamlScalar:
		return InputValue{Scalar: n.value}, nil
	case yamlMap:
		m := map[string]InputValue{}
		err := decodeMap(n, reflect.ValueOf(&m).Elem(), path)
		return InputValue{Map: m}, err
	}
	return InputValue{}, fmt.Errorf("%s must be one value or a mapping of inputs, not a list", path)
}

// decodeStruct reads the mapping n into the struct v by its json tags. A
// key no field names is refused, and so is a required field that is absent
// or, for a string or a list, empty.
func decodeStruct(n *yamlNode, v reflect.Value, path string) error {
	if err := mapEntries(n, path); err != nil {
		return err
	}
	fields := manifestFields(v.Type())
	byName := make(map[string]manifestField, len(fields))
	for _, f := range fields {
		byName[f.name] = f
	}
	present := make(map[string]bool, len(n.keys))
	for i, key := range n.keys {
		keyPath := fieldPath(path, key)
		f, ok := byName[key]
		if !ok {
			return fmt.Errorf("%s is not a field of a %s manifest", keyPath, ManifestAPIVersion)
		}
		val := n.vals[i]
		var err error
		switch {
		case f.oneOrList && isNull(val):
			err = noValue(keyPath)
		case f.oneOrList:
			err = decodeList(val, v.Field(f.index), keyPath, true)
		default:
			err = decodeValue(val, v.Field(f.index), keyPath)
		}
		if err != nil {
			return err
		}
		present[key] = true
	}
	for _, f := range fields {
		if !f.required {
			continue
		}
		fv := v.Field(f.index)
		switch {
		case !present[f.name]:
			return fmt.Errorf("%s is required", fieldPath(path, f.name))
		case (fv.Kind() == reflect.String || fv.Kind() == reflect.Slice) && fv.Len() == 0:
			return fmt.Errorf("%s is required and must not be empty", fieldPath(path, f.name))
		}
	}
	return nil
}

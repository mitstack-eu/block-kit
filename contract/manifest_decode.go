package contract

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// manifestDecoder reads a node tree parseYAML built into a manifest's Go
// types by their struct tags. Both manifest versions read through it, so
// under either a key no field names is refused, naming its path, and so is
// a key written twice, a merge key, an alias and a line the reader could not
// place.
//
// A field whose tag has no omitempty option is required: it must be present
// and, for a string or a list, not empty. A field whose fold tag names a
// second key also takes its value under that key; written under both keys
// the two values must agree. A field named _ takes its key's value by its
// type and keeps none of it, for a key a version accepts and drops.
type manifestDecoder struct {
	// version is the apiVersion a refused unknown key names.
	version string
	// tag is the struct tag whose name, before any option, is a field's key.
	tag string
	// coerce reads each scalar as yaml.v3 reads it into a typed Go value,
	// as the platform's decoder does: a null is the zero value and a null
	// list item is dropped, a string takes any scalar's text, a bool also
	// takes the YAML 1.1 words for true and false, and an int takes a plain
	// number, a float truncated toward zero. Without it a null is refused, a string refuses a plain scalar
	// YAML reads as a number or a boolean, and a bool takes true or false
	// only.
	coerce bool
}

// The YAML 1.2 core schema reads a plain scalar matching one of these as a
// null, a boolean or a number rather than as a string.
var (
	plainNull    = regexp.MustCompile(`^(null|Null|NULL|~)$`)
	plainBoolean = regexp.MustCompile(`^(true|True|TRUE|false|False|FALSE)$`)
	plainNumber  = regexp.MustCompile(`^([-+]?[0-9]+|0o[0-7]+|0x[0-9a-fA-F]+|` +
		`[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?|[-+]?\.(inf|Inf|INF)|\.(nan|NaN|NAN))$`)
	// yamlStyleFloat is the decimal float form yaml.v3 resolves a plain
	// scalar to a float by, once its underscores are dropped.
	yamlStyleFloat = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$`)
)

// yaml11Booleans are the words yaml.v3 also reads into a Go bool, plain or
// quoted, for compatibility with YAML 1.1.
var yaml11Booleans = map[string]bool{
	"y": true, "Y": true, "yes": true, "Yes": true, "YES": true, "on": true, "On": true, "ON": true,
	"n": false, "N": false, "no": false, "No": false, "NO": false, "off": false, "Off": false, "OFF": false,
}

// isNull reports whether YAML reads n as a null.
func isNull(n *yamlNode) bool {
	return n.kind == yamlNull || n.kind == yamlScalar && !n.str && plainNull.MatchString(n.value)
}

// manifestField is one field of a manifest struct as its tag names it.
type manifestField struct {
	name  string
	index int
	// required is a field whose tag has no omitempty.
	required bool
	// oneOrList is a list field that also takes a single value, as a
	// requirement's in takes one capability or a list of them.
	oneOrList bool
	// fold is a second key the field's value may be written under.
	fold string
	// dropped is a field named _, whose value is read and not kept.
	dropped bool
}

// manifestFields returns t's fields in declaration order, by the name tag
// gives them.
func manifestFields(t reflect.Type, tag string) []manifestField {
	fields := make([]manifestField, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get(tag), ",")
		fields = append(fields, manifestField{
			name:      name,
			index:     i,
			required:  opts != "omitempty",
			oneOrList: t == reflect.TypeOf(ManifestRequirement{}) && f.Name == "In",
			fold:      f.Tag.Get("fold"),
			dropped:   f.Name == "_",
		})
	}
	return fields
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

// noValue refuses a null where a value is required: a key is left out or
// given a value.
func noValue(path string) error {
	return fmt.Errorf("%s has no value, so leave it out or give it one", describe(path))
}

// decode reads n into v.
func (d manifestDecoder) decode(n *yamlNode, v reflect.Value, path string) error {
	if err := readable(n, path); err != nil {
		return err
	}
	if isNull(n) {
		if d.coerce {
			v.Set(reflect.Zero(v.Type()))
			return nil
		}
		return noValue(path)
	}
	if v.Type() == inputValueType {
		in, err := d.input(n, path)
		v.Set(reflect.ValueOf(in))
		return err
	}
	switch v.Kind() {
	case reflect.String:
		if n.kind != yamlScalar {
			return fmt.Errorf("%s must be one value, not a mapping or a list", path)
		}
		if !d.coerce && !n.str && (plainBoolean.MatchString(n.value) || plainNumber.MatchString(n.value)) {
			return fmt.Errorf("%s is %s, which YAML reads as a number or a boolean, but it takes a string, so quote it", path, n.value)
		}
		v.SetString(n.value)
	case reflect.Bool:
		b, ok := d.boolean(n)
		if !ok {
			if d.coerce {
				return fmt.Errorf("%s takes a boolean only: true or false, or yes, no, on or off", path)
			}
			return fmt.Errorf("%s takes true or false only", path)
		}
		v.SetBool(b)
	case reflect.Int:
		i, ok := plainInteger(n)
		if !ok || v.OverflowInt(i) {
			return fmt.Errorf("%s takes an integer only", path)
		}
		v.SetInt(i)
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		if err := d.decode(n, p.Elem(), path); err != nil {
			return err
		}
		v.Set(p)
	case reflect.Slice:
		return d.list(n, v, path, false)
	case reflect.Map:
		return d.mapping(n, v, path)
	case reflect.Struct:
		return d.structure(n, v, path)
	default:
		return fmt.Errorf("%s has a Go type, %s, the manifest decoder does not read", path, v.Type())
	}
	return nil
}

// boolean reads the scalar n as a bool. Under coerce a quoted true or false
// is a string, which a bool does not take, while a YAML 1.1 word is taken
// plain or quoted, as yaml.v3 takes it.
func (d manifestDecoder) boolean(n *yamlNode) (value, ok bool) {
	if n.kind != yamlScalar {
		return false, false
	}
	if !n.str && plainBoolean.MatchString(n.value) && (d.coerce || n.value == "true" || n.value == "false") {
		return strings.EqualFold(n.value, "true"), true
	}
	if d.coerce {
		value, ok = yaml11Booleans[n.value]
	}
	return value, ok
}

// plainInteger reads the plain scalar n into an integer as yaml.v3 does for
// a Go int: with any underscores dropped, an integer in decimal or with a
// 0x, 0o or 0b prefix, or a decimal float that fits an int64, truncated
// toward zero. A quoted scalar is a string, which an int does not take.
func plainInteger(n *yamlNode) (int64, bool) {
	if n.kind != yamlScalar || n.str {
		return 0, false
	}
	plain := strings.ReplaceAll(n.value, "_", "")
	if i, err := strconv.ParseInt(plain, 0, 64); err == nil {
		return i, true
	}
	if u, err := strconv.ParseUint(plain, 0, 64); err == nil {
		return int64(u), u <= math.MaxInt64
	}
	if !yamlStyleFloat.MatchString(plain) {
		return 0, false
	}
	f, err := strconv.ParseFloat(plain, 64)
	if err != nil || f < math.MinInt64 || f >= math.MaxInt64 {
		return 0, false
	}
	return int64(f), true
}

// list reads the list n into the slice v. oneOrList also takes a single
// value as a one-item list.
func (d manifestDecoder) list(n *yamlNode, v reflect.Value, path string, oneOrList bool) error {
	if oneOrList && n.kind == yamlScalar {
		n = &yamlNode{kind: yamlSeq, items: []*yamlNode{n}}
	}
	if n.kind != yamlSeq {
		if oneOrList {
			return fmt.Errorf("%s must be one value or a list", path)
		}
		return fmt.Errorf("%s must be a list", path)
	}
	list := reflect.MakeSlice(v.Type(), 0, len(n.items))
	for i, item := range n.items {
		if d.coerce && isNull(item) {
			continue
		}
		elem := reflect.New(v.Type().Elem()).Elem()
		if err := d.decode(item, elem, fmt.Sprintf("%s[%d]", path, i)); err != nil {
			return err
		}
		list = reflect.Append(list, elem)
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

// mapping reads the mapping n into the map v.
func (d manifestDecoder) mapping(n *yamlNode, v reflect.Value, path string) error {
	if err := mapEntries(n, path); err != nil {
		return err
	}
	m := reflect.MakeMapWithSize(v.Type(), len(n.keys))
	for i, key := range n.keys {
		elem := reflect.New(v.Type().Elem()).Elem()
		if err := d.decode(n.vals[i], elem, fieldPath(path, key)); err != nil {
			return err
		}
		m.SetMapIndex(reflect.ValueOf(key), elem)
	}
	v.Set(m)
	return nil
}

// input reads one resource input: a scalar, which may be a number or a
// boolean, or a mapping of named inputs.
func (d manifestDecoder) input(n *yamlNode, path string) (InputValue, error) {
	switch n.kind {
	case yamlScalar:
		return InputValue{Scalar: n.value}, nil
	case yamlMap:
		m := map[string]InputValue{}
		err := d.mapping(n, reflect.ValueOf(&m).Elem(), path)
		return InputValue{Map: m}, err
	}
	return InputValue{}, fmt.Errorf("%s must be one value or a mapping of inputs, not a list", path)
}

// foldedValue is a value written under a field's fold key.
type foldedValue struct {
	field manifestField
	value string
}

// structure reads the mapping n into the struct v by its tags. A key no
// field names is refused, and so is a required field that is absent, null
// or, for a string or a list, empty.
func (d manifestDecoder) structure(n *yamlNode, v reflect.Value, path string) error {
	if err := mapEntries(n, path); err != nil {
		return err
	}
	fields := manifestFields(v.Type(), d.tag)
	byKey := make(map[string]manifestField, len(fields))
	for _, f := range fields {
		byKey[f.name] = f
		if f.fold != "" {
			byKey[f.fold] = f
		}
	}
	present := make(map[string]bool, len(n.keys))
	var folded []foldedValue
	for i, key := range n.keys {
		keyPath := fieldPath(path, key)
		f, ok := byKey[key]
		if !ok {
			return fmt.Errorf("%s is not a field of a %s manifest", keyPath, d.version)
		}
		val := n.vals[i]
		target := v.Field(f.index)
		underFold := key != f.name
		if f.dropped || underFold {
			target = reflect.New(target.Type()).Elem()
		}
		var err error
		switch {
		case isNull(val) && (f.required || f.oneOrList):
			err = noValue(keyPath)
		case f.oneOrList:
			err = d.list(val, target, keyPath, true)
		default:
			err = d.decode(val, target, keyPath)
		}
		if err != nil {
			return err
		}
		if underFold {
			folded = append(folded, foldedValue{f, target.String()})
		}
		present[key] = true
	}
	for _, fv := range folded {
		if err := foldInto(v.Field(fv.field.index), fv, path); err != nil {
			return err
		}
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

// foldInto writes a value given under a field's fold key into the field:
// the one that is set, or their shared value. Two different values are
// refused, naming both keys and both values.
func foldInto(field reflect.Value, fv foldedValue, path string) error {
	switch current := field.String(); {
	case fv.value == "":
		return nil
	case current == "" || current == fv.value:
		field.SetString(fv.value)
		return nil
	default:
		return fmt.Errorf("%s: %s %q and %s %q disagree; write one of them, or both with the same value",
			describe(path), fv.field.fold, fv.value, fv.field.name, current)
	}
}

package handler

import (
	"encoding/json"
	"strings"

	"github.com/mitstack-eu/operator-kit/contract"
)

// paramBindingPrefix starts a manifest input bound to one of the block's
// params, as in params.image.
const paramBindingPrefix = "params."

// secretResourceInputs returns every secret value in inputs, the
// resourceInputs of a request naming the declared resource decl. A
// top-level input decl's catalogued type marks secret is secret whole, so
// every string at any depth of its value is returned. Any other input is
// secret where the manifest binds it to a param in secretParams, at the top
// level or inside a mapping input, and the value at that same key path of
// the input is returned. A value is returned as its decoded string; a value
// that is not a JSON string, or an input that does not decode, adds
// nothing.
func secretResourceInputs(decl contract.ManifestResource, secretParams map[string]bool, inputs map[string]json.RawMessage) []string {
	typeSecret := map[string]bool{}
	if t, ok := contract.LookupResourceType(decl.Type); ok {
		for _, in := range t.Inputs {
			if in.Secret {
				typeSecret[in.Name] = true
			}
		}
	}

	var out []string
	for name, raw := range inputs {
		if typeSecret[name] {
			var v any
			if json.Unmarshal(raw, &v) == nil {
				out = appendStrings(out, v)
			}
			continue
		}
		if bound, ok := decl.Inputs[name]; ok {
			out = appendBoundSecrets(out, bound, secretParams, raw)
		}
	}
	return out
}

// appendBoundSecrets appends raw's decoded string to out when bound is a
// scalar binding to a param in secretParams. When bound is a mapping, it
// descends into raw's JSON object key by key alongside it.
func appendBoundSecrets(out []string, bound contract.InputValue, secretParams map[string]bool, raw json.RawMessage) []string {
	if bound.Map == nil {
		param, ok := strings.CutPrefix(bound.Scalar, paramBindingPrefix)
		if !ok || !secretParams[param] {
			return out
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			out = append(out, s)
		}
		return out
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return out
	}
	for key, nested := range bound.Map {
		if v, ok := fields[key]; ok {
			out = appendBoundSecrets(out, nested, secretParams, v)
		}
	}
	return out
}

// appendStrings appends every string at any depth of v, a decoded JSON
// value, to out.
func appendStrings(out []string, v any) []string {
	switch v := v.(type) {
	case string:
		out = append(out, v)
	case map[string]any:
		for _, e := range v {
			out = appendStrings(out, e)
		}
	case []any:
		for _, e := range v {
			out = appendStrings(out, e)
		}
	}
	return out
}

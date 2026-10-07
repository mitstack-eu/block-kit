package handler

import (
	"encoding/json"
	"strings"

	"github.com/mitstack-eu/operator-kit/contract"
)

const paramBindingPrefix = "params."

// secretResourceInputs treats an input its catalogued type marks secret as secret whole, at any depth, and any other input as secret only where the manifest binds it to a secret param; a value that does not decode as a string adds nothing.
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

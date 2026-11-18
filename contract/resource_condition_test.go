package contract_test

// IncludedResources keeps the entries a request's inputs include, and CheckResourceConditions judges each condition by the inputs every declaring type gives.

import (
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
)

func conditionSnapshot() contract.TypeSnapshot {
	location := func(def string, allowed ...string) []contract.SnapshotInput {
		return []contract.SnapshotInput{{Name: "location", Default: def, AllowedValues: allowed}}
	}
	return contract.TypeSnapshot{Version: 1, Types: []contract.SnapshotType{
		{ID: "hetzner.server", Inputs: location("nbg1", "fsn1", "nbg1", "hel1")},
		{ID: "hetzner.volume", Inputs: location("nbg1", "nbg1", "hel1")},
		{ID: "hetzner.primary-ip", Inputs: location("fsn1", "fsn1", "nbg1", "hel1")},
		{ID: "hetzner.network"},
	}}
}

// beside lists first and then an entry named spare of typ, created for the given values of location.
func beside(first contract.ManifestResource, typ string, many bool, values ...string) []contract.ManifestResource {
	spare := contract.ManifestResource{Name: "spare", Type: typ, Types: []string{typ}, Many: many, When: &contract.ResourceCondition{Input: "location", Values: values}}
	return []contract.ManifestResource{first, spare}
}

var (
	plainServer  = contract.ManifestResource{Name: "server", Type: "hetzner.server", Types: []string{"hetzner.server"}}
	plainNetwork = contract.ManifestResource{Name: "network", Type: "hetzner.network", Types: []string{"hetzner.network"}}
)

func TestIncludedResources_KeepsTheEntriesTheInputsInclude(t *testing.T) {
	cases := []struct {
		label     string
		resources []contract.ManifestResource
		params    map[string]string
		want      []string
	}{
		{"a value the condition lists includes the entry", beside(plainServer, "hetzner.server", false, "hel1"), map[string]string{"location": "hel1"}, []string{"server", "spare"}},
		{"a value it does not list leaves the entry out", beside(plainServer, "hetzner.server", false, "hel1"), map[string]string{"location": "fsn1"}, []string{"server"}},
		{"no value takes the default the declaring types share", beside(plainServer, "hetzner.volume", false, "nbg1"), nil, []string{"server", "spare"}},
		{"an empty value takes that default too", beside(plainServer, "hetzner.volume", false, "nbg1"), map[string]string{"location": ""}, []string{"server", "spare"}},
		{"no value, with declaring types giving two defaults, leaves the entry out", beside(plainServer, "hetzner.primary-ip", false, "fsn1"), nil, []string{"server"}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			included := contract.IncludedResources(tc.resources, conditionSnapshot(), tc.params)

			var got []string
			for _, r := range included {
				got = append(got, r.Name)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("IncludedResources(%v) = %q, want %q", tc.params, got, tc.want)
			}
		})
	}
}

func TestCheckResourceConditions_JudgesAConditionByTheInputsEveryDeclaringTypeGives(t *testing.T) {
	cases := []struct {
		label     string
		resources []contract.ManifestResource
		want      string
	}{
		{"a value every declaring type allows", beside(plainServer, "hetzner.volume", false, "hel1"), ""},
		{"a value one declaring type does not allow", beside(plainServer, "hetzner.volume", false, "fsn1"),
			`spec.resources[1].when: resource "spare" is created for value "fsn1" of input "location", which allows only nbg1, hel1`},
		{"an input only an entry marked many declares", beside(plainNetwork, "hetzner.server", true, "hel1"),
			`spec.resources[1].when: resource "spare" names input "location", which none of the block's resource types declares`},
		{"a type the snapshot does not hold", beside(plainNetwork, "hetzner.no-such-type", false, "hel1"),
			`spec.resources[1].when: resource "spare" names input "location", which none of the block's resource types declares`},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			err := contract.CheckResourceConditions(tc.resources, conditionSnapshot())

			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != tc.want {
				t.Errorf("CheckResourceConditions refused %q, want %q", got, tc.want)
			}
		})
	}
}

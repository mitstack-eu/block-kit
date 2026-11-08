package cloud_test

// A block reads serverType as an ordered list of the types it runs on, and refuses an entry outside them, a repeat, or one past the most it takes.

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/hetzner/cloud"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
)

var fallbackPair = cloud.ServerTypes{Allowed: []string{"cx23", "cx33", "cpx22", "cpx32"}, Max: 2, Default: "cx23"}

func desiredServerType(value string) contract.Desired {
	return contract.Desired{Params: map[string]string{"serverType": value}}
}

func TestServerTypesRead_ReturnsTheEntriesInTheOrderGivenWithTheSpacesAroundEachTrimmed(t *testing.T) {
	cases := []struct {
		name  string
		types cloud.ServerTypes
		value string
		want  []string
	}{
		{"one type", fallbackPair, "cx33", []string{"cx33"}},
		{"a type and its fallback", fallbackPair, "cpx22,cx23", []string{"cpx22", "cx23"}},
		{"entries with spaces", fallbackPair, " cx33 , cpx32 ", []string{"cx33", "cpx32"}},
		{"no Max takes every allowed type", cloud.ServerTypes{Allowed: []string{"cx23", "cx33", "cpx22"}}, "cpx22,cx33,cx23", []string{"cpx22", "cx33", "cx23"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.types.Read(desiredServerType(tc.value))

			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("Read(%q) = %q, %v; want %q", tc.value, got, err, tc.want)
			}
		})
	}
}

func TestServerTypesRead_ReadsTheBlocksDefaultListWhenNoTypeIsGiven(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]string
	}{
		{"an empty value", map[string]string{"serverType": ""}},
		{"no serverType param", map[string]string{}},
		{"no params", nil},
	}
	types := cloud.ServerTypes{Allowed: []string{"cx53", "cpx52", "cpx62"}, Max: 2, Default: "cx53,cpx52"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := types.Read(contract.Desired{Params: tc.params})

			if want := []string{"cx53", "cpx52"}; err != nil || !slices.Equal(got, want) {
				t.Fatalf("Read = %q, %v; want the default %q", got, err, want)
			}
		})
	}
}

func TestServerTypesRead_RefusesAsInvalidParamNamingServerTypeAndTheOffendingEntry(t *testing.T) {
	onlyCx23 := cloud.ServerTypes{Allowed: []string{"cx23"}, Max: 1}
	cases := []struct {
		name  string
		types cloud.ServerTypes
		value string
		says  []string
	}{
		{"a type the block does not run on", fallbackPair, "cx23,cx11", []string{`"cx11"`, "cx23, cx33, cpx22, cpx32"}},
		{"a type given twice", fallbackPair, "cx23,cx23", []string{`"cx23"`, "twice"}},
		{"a list past the most the block takes", fallbackPair, "cx23,cpx22,cx33", []string{`"cx33"`, "at most 2"}},
		{"a fallback to a block that takes one type", onlyCx23, "cx23,cpx22", []string{`"cpx22"`, "at most 1"}},
		{"an empty entry", fallbackPair, "cx23,,cpx22", []string{`""`, "cx23, cx33, cpx22, cpx32"}},
		{"no type to a block with no default", onlyCx23, "", []string{"cx23"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.types.Read(desiredServerType(tc.value))

			var refused *handler.Refused
			if !errors.As(err, &refused) || refused.Class != "invalid-param" {
				t.Fatalf("Read(%q) = %q, %v; want an invalid-param refusal", tc.value, got, err)
			}
			for _, said := range append(tc.says, `params["serverType"]`) {
				if !strings.Contains(refused.Detail, said) {
					t.Errorf("refusal %q, want it to say %s", refused.Detail, said)
				}
			}
		})
	}
}

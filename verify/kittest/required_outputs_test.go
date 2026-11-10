package kittest

import (
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
)

// CheckBlock cannot send an answer carrying no resourceSecretOutputs beside a merged secret, since the kit handler fills both, so requiredOutputs is handed one built by hand.
func TestRequiredOutputs_JudgesAnAnswerCarryingNoResourceSecretOutputsByItsMergedSecretOutputs(t *testing.T) {
	snap := contract.TypeSnapshot{Types: []contract.SnapshotType{
		{ID: "acme.api-key", Outputs: []contract.SnapshotOutput{{Name: "token", Secret: true, Required: true}}},
	}}
	cases := []struct {
		name          string
		secretOutputs map[string]string
		// want lists, per expected problem, what it must name.
		want [][]string
	}{
		{"token in the merged map finds no problem", map[string]string{"token": "token-of-api-key"}, nil},
		{"token missing from the merged map finds a problem naming the resource and token", nil, [][]string{{`resource "api-key"`, `"token"`}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &blockCheck{in: Block{Config: handler.Config{Types: snap}}, spec: contract.ManifestSpec{Provides: []string{"api-key"}}, ran: map[string]bool{}}
			resp := contract.Response{
				SecretOutputs: tc.secretOutputs,
				Resources:     map[string]contract.ResourceOutcome{"api-key": {Observe: &contract.CallOutcome{State: contract.Present, Type: "acme.api-key"}}},
			}

			c.requiredOutputs(resp, "subscribe")

			if len(c.problems) != len(tc.want) {
				t.Fatalf("requiredOutputs problems = %q; want %d", c.problems, len(tc.want))
			}
			for i, wants := range tc.want {
				for _, w := range wants {
					if !strings.Contains(c.problems[i], w) {
						t.Errorf("requiredOutputs problem %q; want it to name %s", c.problems[i], w)
					}
				}
			}
		})
	}
}

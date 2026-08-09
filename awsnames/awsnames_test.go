package awsnames_test

// The point of these tests is not that string concatenation works. It is that
// these exact strings are what all three engines produce — the CloudFormation
// !Sub, the OpenTofu local.prefix, and the Crossplane Format transform. If any
// engine's template changes its naming, this file is where the disagreement
// has to show up, because nothing else compares them.

import (
	"testing"

	"github.com/mitstack-eu/operator-kit/awsnames"
)

func TestNames_MatchWhatTheEnginesActuallyCreate(t *testing.T) {
	for _, tc := range []struct{ got, want, engineSource string }{
		{awsnames.ECRRepository("checkout"), "mitstack-app-checkout", `template.yaml !Sub "mitstack-app-${AppName}"`},
		{awsnames.InstanceRole("checkout"), "mitstack-app-checkout-instance", `template.yaml RoleName !Sub "...-instance"`},
		{awsnames.DeployRole("checkout"), "mitstack-app-checkout-deploy", `template.yaml RoleName !Sub "...-deploy"`},
		{awsnames.InstanceProfile("checkout"), "mitstack-app-checkout-instance", `template.yaml InstanceProfileName`},
		{awsnames.Prefix("checkout"), "mitstack-app-checkout", `perapp/data.tf local.prefix`},
	} {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q — this must match %s", tc.got, tc.want, tc.engineSource)
		}
	}
}

// An app name with surrounding whitespace would otherwise produce a name that
// looks right in a log and matches nothing in AWS.
func TestNames_TrimTheAppName(t *testing.T) {
	if got := awsnames.ECRRepository("  checkout "); got != "mitstack-app-checkout" {
		t.Fatalf("got %q", got)
	}
}

func TestColliding_ListsEveryAccountUniqueName(t *testing.T) {
	got := awsnames.Colliding("checkout")
	want := []string{"mitstack-app-checkout", "mitstack-app-checkout-instance", "mitstack-app-checkout-deploy"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v — a name missing here is a name a switch will not wait for", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

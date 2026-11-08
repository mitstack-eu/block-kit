package awsnames_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/aws/awsnames"
)

func TestFor_NamesEveryResourceUnderTheCallersPrefix(t *testing.T) {
	n := awsnames.For("acme-", "  checkout ")

	for _, tc := range []struct{ name, got, want string }{
		{"base", n.Base(), "acme-checkout"},
		{"ECR repository", n.ECRRepository(), "acme-checkout"},
		{"instance role", n.InstanceRole(), "acme-checkout-instance"},
		{"instance profile", n.InstanceProfile(), "acme-checkout-instance"},
		{"deploy role", n.DeployRole(), "acme-checkout-deploy"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestFor_ACallerKeepsTheNamesItsLiveResourcesCarryByPassingTheirPrefix(t *testing.T) {
	n := awsnames.For("mitstack-app-", "checkout")

	got := []string{n.ECRRepository(), n.InstanceRole(), n.InstanceProfile(), n.DeployRole()}

	want := []string{"mitstack-app-checkout", "mitstack-app-checkout-instance", "mitstack-app-checkout-instance", "mitstack-app-checkout-deploy"}
	if !slices.Equal(got, want) {
		t.Errorf("names = %q, want %q, or a live stack's resources are renamed and replaced", got, want)
	}
}

func TestColliding_ListsEveryAccountUniqueName(t *testing.T) {
	want := []string{"acme-checkout", "acme-checkout-instance", "acme-checkout-deploy"}

	if got := awsnames.For("acme-", "checkout").Colliding(); !slices.Equal(got, want) {
		t.Errorf("Colliding() = %q, want %q: a name missing here is a name a switch will not wait for", got, want)
	}
}

var cfnStackNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)

func TestStackName_IsADeterministicCloudFormationSafeName(t *testing.T) {
	fits := strings.Repeat("a", 123)
	for _, tc := range []struct{ name, prefix, id, want string }{
		{"an empty id is the prefix and sub", "acme-", "", "acme-sub"},
		{"slashes, underscores and colons become dashes", "acme-", "sub/DEMO_123:x", "acme-sub-DEMO-123-x"},
		{"an id starting with a digit still yields a name starting with a letter", "acme-", "123x", "acme-123x"},
		{"a non-ASCII rune becomes one dash", "acme-", "café", "acme-caf-"},
		{"control characters become dashes", "acme-", "sub\x00\n", "acme-sub--"},
		{"a long id is cut to exactly 128 characters", "acme-", "sub_" + strings.Repeat("\x00", 200), "acme-sub-" + strings.Repeat("-", 119)},
		{"a 123-character id fits whole after the prefix", "acme-", fits, "acme-" + fits},
		{"a 124-character id is cut to 128 characters", "acme-", fits + "b", "acme-" + fits},
		{"a prefix with a colon is made safe too", "acme:", "x", "acme-x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := awsnames.StackName(tc.prefix, tc.id)
			again, _ := awsnames.StackName(tc.prefix, tc.id)

			if err != nil || got != tc.want || again != got {
				t.Errorf("StackName(%q, %q) = %q, %v then %q; want %q both times", tc.prefix, tc.id, got, err, again, tc.want)
			}
			if !cfnStackNameRE.MatchString(got) || len(got) > 128 {
				t.Errorf("StackName = %q (%d characters), not a CloudFormation stack name of at most 128", got, len(got))
			}
		})
	}
}

func TestStackName_RefusesAPrefixThatCannotStartAStackName(t *testing.T) {
	for _, prefix := range []string{"", "1acme-", "-acme", "écho-"} {
		if got, err := awsnames.StackName(prefix, "sub"); err == nil {
			t.Errorf("StackName(%q, sub) = %q with no error, want a refusal: a stack name starts with a letter", prefix, got)
		}
	}
}

func TestSanitiseCFNName_TurnsEachRuneOutsideAlphanumericsIntoOneDash(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"AZaz09", "AZaz09"},
		{"sub/DEMO_123:x", "sub-DEMO-123-x"},
		{"café", "caf-"},
		{"a  b", "a--b"},
	} {
		if got := awsnames.SanitiseCFNName(tc.in); got != tc.want {
			t.Errorf("SanitiseCFNName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

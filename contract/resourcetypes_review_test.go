package contract_test

// resourcetypes_review_test.go pins what the catalogue naming review settled:
// every catalogue id keeps to ADR 0080's community grammar, and each resource
// requirement that ADR 0079 turns a former extension point or capability-only
// entry into has a catalogued type owned by one capability.

import (
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

// ValidateResourceTypes refuses an id outside the community grammar, even
// when the id's prefix equals the type's capability, so the grammar rule
// alone refuses each case. A slash or an org namespace in the capability, a
// capability that starts or ends with a dash, one in uppercase and one past
// 40 characters are each refused, naming the id.
func TestValidateResourceTypes_RefusesAnIDOutsideTheCommunityGrammar(t *testing.T) {
	long := "a" + strings.Repeat("b", 39) + "c"
	cases := map[string]string{
		"slash in the capability":         "reconcile/crossplane",
		"org namespace on the capability": "@acme/aws",
		"capability starting with a dash": "-aws",
		"capability ending with a dash":   "aws-",
		"uppercase capability":            "Aws",
		"underscore in the capability":    "aws_east",
		"41-character capability":         long,
	}
	for name, capability := range cases {
		t.Run(name, func(t *testing.T) {
			rt := validType()
			rt.ID, rt.Capability = capability+".thing", capability
			err := contract.ValidateResourceTypes([]contract.ResourceType{rt})
			if err == nil {
				t.Fatalf("ValidateResourceTypes with ID %q and capability %q = nil, want an error", rt.ID, rt.Capability)
			}
			if !strings.Contains(err.Error(), rt.ID) {
				t.Errorf("error %q does not name the type's ID %q", err, rt.ID)
			}
			if rest := strings.ReplaceAll(err.Error(), rt.ID, ""); !strings.Contains(rest, "community") {
				t.Errorf("error %q does not say the ID breaks the community grammar", err)
			}
		})
	}
}

// A 40-character capability is the longest the grammar allows, so it is
// accepted, as is a capability holding a dash and a digit.
func TestValidateResourceTypes_AcceptsACapabilityAtTheGrammarsLimits(t *testing.T) {
	for _, capability := range []string{"a" + strings.Repeat("b", 38) + "c", "app-host", "s3"} {
		rt := validType()
		rt.ID, rt.Capability = capability+".thing", capability
		if err := contract.ValidateResourceTypes([]contract.ResourceType{rt}); err != nil {
			t.Errorf("ValidateResourceTypes with capability %q = %v, want nil", capability, err)
		}
	}
}

// The review adds github.scaffold-contribution, which ADR 0079 names, and
// cloudformation.llm-endpoint, and gives aws.route53-record the app-address
// kind and the appUrl output the former aws-topology requirement reads.
func TestResourceTypes_ReviewAddsTheScaffoldContributionAndLLMEndpointAndAnAppAddress(t *testing.T) {
	cases := []struct {
		id, capability, kind string
		outputs              []string
	}{
		{"github.scaffold-contribution", "github", "scaffold-contribution", []string{"scaffoldContribution"}},
		{"cloudformation.llm-endpoint", "cloudformation", "llm-endpoint", []string{"llmEndpoint", "model"}},
		{"aws.route53-record", "aws", "app-address", []string{"appHostname", "appUrl"}},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			rt, ok := contract.LookupResourceType(c.id)
			if !ok {
				t.Fatalf("the catalogue has no %q", c.id)
			}
			if rt.Capability != c.capability || rt.Kind != c.kind {
				t.Errorf("%s: capability, kind = %q, %q; want %q, %q", c.id, rt.Capability, rt.Kind, c.capability, c.kind)
			}
			for _, o := range c.outputs {
				if !slices.Contains(rt.Outputs, o) {
					t.Errorf("%s outputs %q, want it to publish %q", c.id, rt.Outputs, o)
				}
			}
		})
	}
}

// Each former extension point or capability-only requirement ADR 0079 lists
// is met by a catalogued type owned by one capability. A requirement narrowed
// to a type names it; one matched by kind names the kind ADR 0078 fixes and
// the outputs the requiring manifests read, and the test names the type
// expected to meet it.
func TestResourceTypes_EveryADR0079RequirementHasACataloguedType(t *testing.T) {
	cases := []struct {
		requirement string
		req         contract.ResourceRequirement
		meetingType string
		capability  string
	}{
		{"scaffold-contribution", contract.ResourceRequirement{Resource: "contributions", Type: "github.scaffold-contribution", Many: true}, "github.scaffold-contribution", "github"},
		{"ci-check", contract.ResourceRequirement{Resource: "checks", Type: "github.workflow-file", Reads: []string{"ciCheckContribution"}, Many: true}, "github.workflow-file", "github"},
		{"runner", contract.ResourceRequirement{Resource: "runner", Kind: "ci-runner", Reads: []string{"runnerLabel", "runsOn", "runsOnPr", "awsCredential"}}, "github.self-hosted-runner", "github"},
		{"tracker", contract.ResourceRequirement{Resource: "token", Type: "tracker.api-token"}, "tracker.api-token", "tracker"},
		{"llm", contract.ResourceRequirement{Resource: "llm", Kind: "llm-endpoint", Reads: []string{"llmEndpoint"}}, "cloudformation.llm-endpoint", "cloudformation"},
		{"app-host", contract.ResourceRequirement{Resource: "container", Type: "app-host.container"}, "app-host.container", "app-host"},
		{"aws-topology", contract.ResourceRequirement{Resource: "address", Kind: "app-address", Reads: []string{"appUrl"}}, "aws.route53-record", "aws"},
		{"aws-iam", contract.ResourceRequirement{Resource: "role", Kind: "iam-role", Reads: []string{"deployRoleArn"}}, "aws.iam-role", "aws"},
		{"database", contract.ResourceRequirement{Resource: "database", Kind: "postgres-database", Reads: []string{"host", "port", "dbName", "username"}}, "app-host.postgres-database", "app-host"},
		{"generated-source", contract.ResourceRequirement{Resource: "source", Kind: "source-revision", Reads: []string{"sourceBranch", "sourceSha"}}, "github.commit", "github"},
		{"deployment", contract.ResourceRequirement{Resource: "deployment", Kind: "deployment"}, "app-host.deployment", "app-host"},
	}
	for _, c := range cases {
		t.Run(c.requirement, func(t *testing.T) {
			rt, ok := contract.LookupResourceType(c.meetingType)
			if !ok {
				t.Fatalf("the catalogue has no %q to meet the %s requirement", c.meetingType, c.requirement)
			}
			if rt.Capability != c.capability {
				t.Errorf("%s belongs to %q, want %q", c.meetingType, rt.Capability, c.capability)
			}
			if !c.req.Matches(rt) {
				t.Errorf("requirement %+v does not match %s (kind %q, outputs %q)", c.req, rt.ID, rt.Kind, rt.Outputs)
			}
		})
	}
}

// The output registry holds scaffoldContribution, the one output
// github.scaffold-contribution publishes, as a canonical entry that carries an
// example and belongs to the scaffold-contribution fan-in, as
// ciCheckContribution belongs to ci-check.
func TestLookupResource_ScaffoldContributionIsACanonicalEntry(t *testing.T) {
	r, ok := contract.LookupResource("scaffoldContribution")
	if !ok {
		t.Fatal("the output registry has no scaffoldContribution")
	}
	if r.Name != "scaffoldContribution" {
		t.Errorf("scaffoldContribution resolves to %q, want its own canonical entry", r.Name)
	}
	if r.Kind != "Source" {
		t.Errorf("Kind = %q, want %q", r.Kind, "Source")
	}
	if r.Secret || r.Example == "" || r.Meaning == "" || r.Shape == "" {
		t.Errorf("entry %+v, want a non-secret entry with a meaning, a shape and an example", r)
	}
	if !slices.Equal(r.Capabilities, []string{"scaffold-contribution"}) {
		t.Errorf("Capabilities = %q, want [scaffold-contribution]", r.Capabilities)
	}
}

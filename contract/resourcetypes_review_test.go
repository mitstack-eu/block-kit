package contract_test

// resourcetypes_review_test.go pins what the catalogue naming review settled:
// every catalogue id keeps to ADR 0080's community grammar, and each resource
// requirement that ADR 0079 turns a former extension point or capability-only
// entry into has a catalogued type that lives in one place.

import (
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

// ValidateResourceTypes refuses an id outside the community grammar, so the
// grammar rule refuses each case before the place rule is reached. A slash or
// an org namespace before the dot, a prefix that starts or ends with a dash,
// one in uppercase and one past 40 characters are each refused, naming the id.
func TestValidateResourceTypes_RefusesAnIDOutsideTheCommunityGrammar(t *testing.T) {
	long := "a" + strings.Repeat("b", 39) + "c"
	cases := map[string]string{
		"slash in the prefix":         "reconcile/crossplane",
		"org namespace on the prefix": "@acme/aws",
		"prefix starting with a dash": "-aws",
		"prefix ending with a dash":   "aws-",
		"uppercase prefix":            "Aws",
		"underscore in the prefix":    "aws_east",
		"41-character prefix":         long,
	}
	for name, prefix := range cases {
		t.Run(name, func(t *testing.T) {
			rt := validType()
			rt.ID = prefix + ".thing"
			err := contract.ValidateResourceTypes([]contract.ResourceType{rt})
			if err == nil {
				t.Fatalf("ValidateResourceTypes with ID %q = nil, want an error", rt.ID)
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

// A prefix the grammar accepts still names a place only when it is a
// capability or a kind, so a well-formed prefix that is neither is refused for
// its place, not its grammar.
func TestValidateResourceTypes_RefusesAWellFormedPrefixThatNamesNoPlace(t *testing.T) {
	for _, prefix := range []string{"a" + strings.Repeat("b", 38) + "c", "app-host", "s3", "kube-cluster", "tracker"} {
		rt := validType()
		rt.ID = prefix + ".thing"
		err := contract.ValidateResourceTypes([]contract.ResourceType{rt})
		if err == nil {
			t.Errorf("ValidateResourceTypes with ID %q = nil, want an error", rt.ID)
			continue
		}
		rest := strings.ReplaceAll(err.Error(), rt.ID, "")
		if !strings.Contains(rest, "place") {
			t.Errorf("error %q does not say %q names no place", err, prefix)
		}
		if strings.Contains(rest, "community") {
			t.Errorf("error %q blames the grammar for a well-formed prefix", err)
		}
	}
}

// The review adds github.scaffold-contribution, which ADR 0079 names, and
// gives aws.route53-record the app-address kind and the appUrl output the
// former aws-topology requirement reads. The model then moves the LLM endpoint
// into aws, named for what oss-llm-serving creates.
func TestResourceTypes_ReviewAddsTheScaffoldContributionAndLLMEndpointAndAnAppAddress(t *testing.T) {
	cases := []struct {
		id, place, kind string
		outputs         []string
	}{
		{"github.scaffold-contribution", "github", "scaffold-contribution", []string{"scaffoldContribution"}},
		{"aws.vllm-instance", "aws", "llm-endpoint", []string{"llmEndpoint", "model"}},
		{"aws.route53-record", "aws", "app-address", []string{"appHostname", "appUrl"}},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			rt, ok := contract.LookupResourceType(c.id)
			if !ok {
				t.Fatalf("the catalogue has no %q", c.id)
			}
			if rt.Place() != c.place || rt.Kind != c.kind {
				t.Errorf("%s: place, kind = %q, %q; want %q, %q", c.id, rt.Place(), rt.Kind, c.place, c.kind)
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
// is met by a catalogued type living in one place: the test names the type
// expected to meet it, the kind a requiring manifest asks for, and the
// outputs it reads, which that type publishes. The deployment requirement has
// no case: a CD registration is its operator's bookkeeping, and no type meets
// it.
func TestResourceTypes_EveryADR0079RequirementHasACataloguedType(t *testing.T) {
	cases := []struct {
		requirement string
		kind        string
		reads       []string
		meetingType string
		place       string
	}{
		{"scaffold-contribution", "scaffold-contribution", nil, "github.scaffold-contribution", "github"},
		{"ci-check", "ci-workflow", []string{"ciCheckContribution"}, "github.workflow-file", "github"},
		{"runner", "ci-runner", []string{"runnerLabel", "runsOn", "awsCredential"}, "github.self-hosted-runner", "github"},
		{"runner from ubicloud", "ci-runner", []string{"runsOn"}, "ubicloud.runner", "ubicloud"},
		{"plain", "api-token", nil, "plain.api-token", "plain"},
		{"llm", "llm-endpoint", []string{"llmEndpoint"}, "aws.vllm-instance", "aws"},
		{"host container", "container", nil, "host.container", "host"},
		{"aws-topology", "app-address", []string{"appUrl"}, "aws.route53-record", "aws"},
		{"aws-iam", "iam-role", []string{"roleArn"}, "aws.iam-role", "aws"},
		{"database", "postgres-database", []string{"host", "port", "dbName", "username", "password"}, "host.postgres-database", "host"},
		{"generated-source", "source-revision", []string{"sourceBranch", "sourceSha"}, "github.commit", "github"},
		{"kubernetes cluster", "kubernetes-cluster", []string{"apiEndpoint", "caCertificate", "clusterCredential"}, "hetzner.k3s-cluster", "hetzner"},
	}
	for _, c := range cases {
		t.Run(c.requirement, func(t *testing.T) {
			rt, ok := contract.LookupResourceType(c.meetingType)
			if !ok {
				t.Fatalf("the catalogue has no %q to meet the %s requirement", c.meetingType, c.requirement)
			}
			if rt.Place() != c.place {
				t.Errorf("%s lives in %q, want %q", c.meetingType, rt.Place(), c.place)
			}
			if rt.Kind != c.kind {
				t.Errorf("%s is of kind %q, want %q", c.meetingType, rt.Kind, c.kind)
			}
			for _, read := range c.reads {
				if !slices.Contains(rt.Outputs, read) {
					t.Errorf("%s outputs %q, which lacks %q the %s requirement reads", c.meetingType, rt.Outputs, read, c.requirement)
				}
			}
		})
	}
}

// The output registry holds scaffoldContribution, the one output
// github.scaffold-contribution publishes, as a canonical entry that carries an
// example and is the required output of the scaffold-contribution kind.
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
	k, ok := contract.LookupKind("scaffold-contribution")
	if !ok {
		t.Fatal(`LookupKind("scaffold-contribution") found nothing`)
	}
	if !slices.Equal(k.Required, []contract.KindOutput{{Name: "scaffoldContribution"}}) {
		t.Errorf("scaffold-contribution requires %+v, want only scaffoldContribution, not secret", k.Required)
	}
}

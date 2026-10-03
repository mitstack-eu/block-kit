package contract_test

// catalogue_model_test.go walks the released catalogue against the data model
// of platform ADR 0083: every type produces its kind's required outputs and no
// output its kind does not name, every type lives in the place its id prefix
// names, the capabilities are the ones that hold or support a type, and the
// shapes the model removes are gone.

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

func TestResourceTypes_EveryTypeProducesItsKindsRequiredOutputsAndNoOther(t *testing.T) {
	for _, rt := range catalogue(t) {
		t.Run(rt.ID, func(t *testing.T) {
			k, ok := contract.LookupKind(rt.Kind)
			if !ok {
				t.Fatalf("kind %q is not a released kind", rt.Kind)
			}
			for _, o := range k.Required {
				if !slices.Contains(rt.Outputs, o.Name) {
					t.Errorf("does not produce %q, a required output of kind %q", o.Name, k.ID)
				}
			}
			named := map[string]bool{}
			for _, o := range append(append([]contract.KindOutput{}, k.Required...), k.Optional...) {
				named[o.Name] = true
			}
			for _, name := range rt.Outputs {
				if !named[name] {
					t.Errorf("produces %q, which kind %q does not name", name, k.ID)
				}
			}
		})
	}
}

// Every released kind is the kind of some released type, so no kind is kept
// that nothing implements.
func TestKinds_EachIsTheKindOfAReleasedType(t *testing.T) {
	used := map[string]bool{}
	for _, rt := range catalogue(t) {
		used[rt.Kind] = true
	}
	for _, k := range kinds(t) {
		if !used[k.ID] {
			t.Errorf("kind %q is the kind of no released type", k.ID)
		}
	}
}

// Capabilities are the nine of ADR 0083's capability table. anthropic and
// openai stay capabilities by the owner's ruling, though which types live in
// them is open.
func TestCapabilities_AreTheNineOfTheADRCapabilityTable(t *testing.T) {
	want := []string{"anthropic", "aws", "crossplane", "github", "hetzner", "kubernetes", "openai", "plain", "ubicloud"}
	assertVocabulary(t, "Capabilities", contract.Capabilities, want)
}

// Every capability a released type relies on or hosts is a capability.
func TestResourceTypes_EveryReliesOnAndHostsValueNamesACapability(t *testing.T) {
	for _, rt := range catalogue(t) {
		for _, c := range rt.ReliesOn {
			if !slices.Contains(contract.Capabilities(), c) {
				t.Errorf("%s relies on %q, which is not a capability", rt.ID, c)
			}
		}
		if rt.Hosts != "" && !slices.Contains(contract.Capabilities(), rt.Hosts) {
			t.Errorf("%s hosts %q, which is not a capability", rt.ID, rt.Hosts)
		}
	}
}

func TestCapabilities_NoCapabilityIsAlsoAKind(t *testing.T) {
	for _, c := range contract.Capabilities() {
		if _, ok := contract.LookupKind(c); ok {
			t.Errorf("%q is both a capability and a kind, so a type id prefix of it names no single place", c)
		}
	}
}

func TestResourceTypes_EachPlaceIsItsIDPrefixAndNamesACapabilityOrAKind(t *testing.T) {
	for _, rt := range catalogue(t) {
		prefix, _, _ := strings.Cut(rt.ID, ".")
		if got := rt.Place(); got != prefix {
			t.Errorf("%s: Place() = %q, want its id prefix %q", rt.ID, got, prefix)
		}
		_, isKind := contract.LookupKind(rt.Place())
		if !isKind && !slices.Contains(contract.Capabilities(), rt.Place()) {
			t.Errorf("%s: place %q is neither a capability nor a kind", rt.ID, rt.Place())
		}
	}
}

func TestResourceType_PlaceIsDerivedFromTheIDAlone(t *testing.T) {
	cases := map[string]string{
		"aws.ec2-instance":       "aws",
		"host.container":         "host",
		"ubicloud.runner":        "ubicloud",
		"host.postgres-database": "host",
	}
	for id, want := range cases {
		if got := (contract.ResourceType{ID: id}).Place(); got != want {
			t.Errorf("ResourceType{ID: %q}.Place() = %q, want %q", id, got, want)
		}
	}
}

// The place is derived from the id, so the type carries no separate field
// that could disagree with it.
func TestResourceType_CarriesNoCapabilityField(t *testing.T) {
	if _, ok := reflect.TypeOf(contract.ResourceType{}).FieldByName("Capability"); ok {
		t.Error("ResourceType has a Capability field; a type's place is read from its id prefix")
	}
}

// Outputs belong to kinds alone, so a registry entry names no capability.
func TestResourceEntry_CarriesNoCapabilitiesField(t *testing.T) {
	if _, ok := reflect.TypeOf(contract.ResourceEntry{}).FieldByName("Capabilities"); ok {
		t.Error("ResourceEntry has a Capabilities field; an output belongs to the kinds that name it")
	}
}

func TestResourceTypes_TypesThatLiveOnAHostAreTheThreeHostTypes(t *testing.T) {
	var onHost []string
	for _, rt := range catalogue(t) {
		if rt.Place() == "host" {
			onHost = append(onHost, rt.ID)
		}
	}
	if want := []string{"host.configuration", "host.container", "host.postgres-database"}; !reflect.DeepEqual(onHost, want) {
		t.Errorf("types living on a host = %q, want %q", onHost, want)
	}
}

func TestResourceTypes_HoldNoAppHostCloudFormationOrMirrorCrossplaneType(t *testing.T) {
	for _, rt := range catalogue(t) {
		switch {
		case strings.HasPrefix(rt.ID, "app-host."):
			t.Errorf("the catalogue holds %q; app-host is no longer a place", rt.ID)
		case strings.HasPrefix(rt.ID, "cloudformation."):
			t.Errorf("the catalogue holds %q; CloudFormation is part of the aws API", rt.ID)
		case strings.HasPrefix(rt.ID, "crossplane.") && rt.ID != "crossplane.reconcile-claim":
			t.Errorf("the catalogue holds %q; only crossplane.reconcile-claim lives in crossplane", rt.ID)
		case strings.HasPrefix(rt.ID, "tracker."):
			t.Errorf("the catalogue holds %q; the tracker capability is named plain", rt.ID)
		}
	}
	if _, ok := contract.LookupResourceType("crossplane.reconcile-claim"); !ok {
		t.Error("the catalogue has no crossplane.reconcile-claim")
	}
}

// A CI or CD registration is its operator's bookkeeping. No type is of the
// deployment kind, and no type publishes the registration outputs: artifact,
// which says a build pipeline is registered, and deployment, which identifies
// a registered deploy pipeline.
func TestResourceTypes_HoldNoTypeForACIOrCDRegistration(t *testing.T) {
	for _, rt := range catalogue(t) {
		if rt.Kind == "deployment" {
			t.Errorf("%s is of kind deployment, a CD registration", rt.ID)
		}
		for _, o := range []string{"artifact", "deployment"} {
			if slices.Contains(rt.Outputs, o) {
				t.Errorf("%s publishes %q, a pipeline registration", rt.ID, o)
			}
		}
	}
	if _, ok := contract.LookupResourceType("app-host.deployment"); ok {
		t.Error("the catalogue holds app-host.deployment")
	}
}

func TestResourceTypes_GitHubRepositoryOutputsRepoAndNoOtherRepositoryName(t *testing.T) {
	rt, ok := contract.LookupResourceType("github.repository")
	if !ok {
		t.Fatal("the catalogue has no github.repository")
	}
	if !reflect.DeepEqual(rt.Outputs, []string{"repo"}) {
		t.Errorf("github.repository outputs %q, want exactly [repo]", rt.Outputs)
	}
	for _, rt := range catalogue(t) {
		for _, old := range []string{"boardRepo", "cdRepo", "ciRepo", "repoUrl"} {
			if slices.Contains(rt.Outputs, old) {
				t.Errorf("%s publishes %q, a repository name for one reading block", rt.ID, old)
			}
		}
	}
}

func TestResourceTypes_AnIAMRoleOutputsItsOwnRoleARN(t *testing.T) {
	rt, ok := contract.LookupResourceType("aws.iam-role")
	if !ok {
		t.Fatal("the catalogue has no aws.iam-role")
	}
	if !reflect.DeepEqual(rt.Outputs, []string{"roleArn"}) {
		t.Errorf("aws.iam-role outputs %q, want exactly [roleArn]", rt.Outputs)
	}
	for _, rt := range catalogue(t) {
		for _, old := range []string{"deployRoleArn", "ecrPushRoleArn"} {
			if slices.Contains(rt.Outputs, old) {
				t.Errorf("%s publishes %q, a role ARN named for the block that assumes it", rt.ID, old)
			}
		}
	}
}

func TestResourceTypes_UbicloudRunnerLivesInUbicloudAndReliesOnGitHub(t *testing.T) {
	rt, ok := contract.LookupResourceType("ubicloud.runner")
	if !ok {
		t.Fatal("the catalogue has no ubicloud.runner")
	}
	if rt.Kind != "ci-runner" {
		t.Errorf("kind = %q, want ci-runner", rt.Kind)
	}
	if rt.Place() != "ubicloud" {
		t.Errorf("place = %q, want ubicloud", rt.Place())
	}
	if !reflect.DeepEqual(rt.ReliesOn, []string{"github"}) {
		t.Errorf("ReliesOn = %q, want [github]", rt.ReliesOn)
	}
	if rt.Hosts != "" {
		t.Errorf("Hosts = %q, want none", rt.Hosts)
	}
}

func TestResourceTypes_AClusterTypeOfKindKubernetesClusterHostsKubernetes(t *testing.T) {
	var clusters []contract.ResourceType
	for _, rt := range catalogue(t) {
		if rt.Kind == "kubernetes-cluster" {
			clusters = append(clusters, rt)
		}
	}
	if len(clusters) == 0 {
		t.Fatal("no released type is of kind kubernetes-cluster")
	}
	for _, rt := range clusters {
		if rt.Hosts != "kubernetes" {
			t.Errorf("%s: Hosts = %q, want kubernetes", rt.ID, rt.Hosts)
		}
	}
	rt, ok := contract.LookupResourceType("hetzner.k3s-cluster")
	if !ok {
		t.Fatal("the catalogue has no hetzner.k3s-cluster")
	}
	if rt.Kind != "kubernetes-cluster" || rt.Hosts != "kubernetes" || rt.Scope != "org-wide" {
		t.Errorf("hetzner.k3s-cluster: kind %q, hosts %q, scope %q; want kubernetes-cluster, kubernetes, org-wide", rt.Kind, rt.Hosts, rt.Scope)
	}
}

// Only a type that runs a capability's API names one under hosts, and the one
// that does today is the cluster type.
func TestResourceTypes_OnlyTheClusterTypeHostsACapability(t *testing.T) {
	for _, rt := range catalogue(t) {
		if rt.Hosts != "" && rt.Kind != "kubernetes-cluster" {
			t.Errorf("%s of kind %q hosts %q", rt.ID, rt.Kind, rt.Hosts)
		}
	}
}

// The type that replaces cloudformation.llm-endpoint is named for what
// oss-llm-serving creates: a GPU EC2 instance running the vLLM server behind
// an Elastic IP and a security group, all created and deleted together.
func TestResourceTypes_TheLLMEndpointLivesInAWSAsAVLLMInstance(t *testing.T) {
	rt, ok := contract.LookupResourceType("aws.vllm-instance")
	if !ok {
		t.Fatal("the catalogue has no aws.vllm-instance")
	}
	if rt.Kind != "llm-endpoint" || rt.Category != "ai-model" || rt.PricingKey != "AmazonEC2" {
		t.Errorf("aws.vllm-instance: kind %q, category %q, pricing key %q; want llm-endpoint, ai-model, AmazonEC2", rt.Kind, rt.Category, rt.PricingKey)
	}
	if rt.ProviderResource != nil {
		t.Errorf("aws.vllm-instance maps onto %+v; it is a composite of an instance, an Elastic IP and a security group, so its inputs are written by hand", rt.ProviderResource)
	}
}

// Every type declares a time budget for each resource verb, an observe and an
// absence declaration, at least one failure, an upgrade behaviour, and for a
// stateful type a handover. ValidateResourceTypes checks each rule; this walks
// the released catalogue directly so a rule dropped from the validator still
// fails here.
func TestResourceTypes_EveryTypeDeclaresItsLifecycleFacts(t *testing.T) {
	for _, rt := range catalogue(t) {
		t.Run(rt.ID, func(t *testing.T) {
			if rt.Observe == "" {
				t.Error("no observe declaration")
			}
			if rt.Absence == "" {
				t.Error("no absence declaration")
			}
			for _, verb := range contract.ResourceVerbs() {
				if rt.TimeBudgets[verb] <= 0 {
					t.Errorf("no time budget for %q", verb)
				}
			}
			if len(rt.Failures) == 0 {
				t.Error("declares no failure")
			}
			for _, f := range rt.Failures {
				if !slices.Contains(contract.FailureClasses(), f.Class) || f.Cause == "" {
					t.Errorf("failure %+v: want a cause and a class of %q", f, contract.FailureClasses())
				}
			}
			if !slices.Contains(contract.UpgradeBehaviours(), rt.Upgrade) {
				t.Errorf("upgrade %q is not one of %q", rt.Upgrade, contract.UpgradeBehaviours())
			}
			if (rt.Lifecycle == "stateful") != (rt.Handover != "") {
				t.Errorf("lifecycle %q with handover %q; a handover is declared exactly when stateful", rt.Lifecycle, rt.Handover)
			}
			if rt.Inputs == nil || rt.ReliesOn == nil {
				t.Errorf("Inputs, ReliesOn = %#v, %#v; want empty lists, never nil", rt.Inputs, rt.ReliesOn)
			}
			for _, in := range rt.Inputs {
				if !slices.Contains(contract.InputReplacements(), in.Replacement) {
					t.Errorf("input %q: replacement %q is not one of %q", in.Name, in.Replacement, contract.InputReplacements())
				}
			}
		})
	}
}

func TestResourceTypes_BlockVolumesHandTheirDataOverBySnapshot(t *testing.T) {
	for _, id := range []string{"aws.ebs-volume", "hetzner.volume"} {
		rt, ok := contract.LookupResourceType(id)
		if !ok {
			t.Errorf("the catalogue has no %q", id)
			continue
		}
		if rt.Lifecycle != "stateful" || rt.Handover != "snapshot" {
			t.Errorf("%s: lifecycle %q, handover %q; want stateful, snapshot", id, rt.Lifecycle, rt.Handover)
		}
	}
}

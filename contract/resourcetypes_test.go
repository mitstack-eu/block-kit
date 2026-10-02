package contract_test

// resourcetypes_test.go pins the resource-type catalogue the kit owns: the
// facet vocabularies, the rules ValidateResourceTypes enforces, the released
// catalogue's content, and the Ownership a step and a supplied resource carry.

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

// validType returns a fresh resource type every rule accepts, for a test to
// mutate one field of.
func validType() contract.ResourceType {
	return contract.ResourceType{
		ID:           "aws.ecr-repository",
		Kind:         "container-repository",
		Capability:   "aws",
		Category:     "artifact-registry",
		Languages:    []string{"any-container"},
		ArtifactType: "container-image",
		Lifecycle:    "replaceable",
		Scope:        "per-app",
		Priced:       false,
		PricingKey:   "",
		Outputs:      []string{"ecrRegistry", "registryHost"},
	}
}

// catalogue returns contract.ResourceTypes(), failing the test when it is
// empty so a check over every entry cannot pass by having none to check.
func catalogue(t *testing.T) []contract.ResourceType {
	t.Helper()
	ts := contract.ResourceTypes()
	if len(ts) == 0 {
		t.Fatal("contract.ResourceTypes() is empty")
	}
	return ts
}

func TestValidateResourceTypes_AcceptsTheReleasedCatalogue(t *testing.T) {
	if err := contract.ValidateResourceTypes(catalogue(t)); err != nil {
		t.Errorf("ValidateResourceTypes(ResourceTypes()) = %v, want nil", err)
	}
}

func TestValidateResourceTypes_AcceptsValidVariants(t *testing.T) {
	cases := map[string]func(rt *contract.ResourceType){
		"the base fixture": func(rt *contract.ResourceType) {},
		"priced with a pricing key": func(rt *contract.ResourceType) {
			rt.Priced, rt.PricingKey = true, "AmazonEC2"
		},
		"empty languages and outputs and no artifact type": func(rt *contract.ResourceType) {
			rt.ID, rt.Kind, rt.Category = "aws.security-group", "firewall", "network"
			rt.Languages, rt.Outputs, rt.ArtifactType = []string{}, []string{}, ""
		},
		"several distinct languages": func(rt *contract.ResourceType) {
			rt.Languages = []string{"go", "node", "python"}
		},
		"a capability slug with a hyphen": func(rt *contract.ResourceType) {
			rt.ID, rt.Capability = "app-host.container", "app-host"
		},
		"a dotted canonical output name": func(rt *contract.ResourceType) {
			rt.Outputs = []string{"cloud.region", "host.type"}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			rt := validType()
			mutate(&rt)
			if err := contract.ValidateResourceTypes([]contract.ResourceType{rt}); err != nil {
				t.Errorf("ValidateResourceTypes(%+v) = %v, want nil", rt, err)
			}
		})
	}
}

func TestValidateResourceTypes_AcceptsEveryVocabularyValue(t *testing.T) {
	check := func(t *testing.T, rt contract.ResourceType) {
		t.Helper()
		if err := contract.ValidateResourceTypes([]contract.ResourceType{rt}); err != nil {
			t.Errorf("ValidateResourceTypes(%+v) = %v, want nil", rt, err)
		}
	}
	for _, c := range contract.ResourceTypeCategories() {
		rt := validType()
		rt.Category = c
		check(t, rt)
	}
	for _, l := range contract.ResourceTypeLanguages() {
		rt := validType()
		rt.Languages = []string{l}
		check(t, rt)
	}
	for _, a := range contract.ArtifactTypes() {
		rt := validType()
		rt.ArtifactType = a
		check(t, rt)
	}
	for _, l := range contract.ResourceTypeLifecycles() {
		rt := validType()
		rt.Lifecycle = l
		check(t, rt)
	}
	for _, s := range contract.ResourceTypeScopes() {
		rt := validType()
		rt.Scope = s
		check(t, rt)
	}
}

// TestValidateResourceTypes_RefusesADefectNamingTheTypeID mutates the valid
// fixture so that exactly one rule refuses it, then checks the error names the
// type's ID and carries a fragment identifying that rule. A case no other rule
// refuses fails if its own rule is dropped.
func TestValidateResourceTypes_RefusesADefectNamingTheTypeID(t *testing.T) {
	cases := []struct {
		name string
		// rule is a fragment the error text carries, apart from the ID,
		// naming the rule that refused the type.
		rule   string
		mutate func(rt *contract.ResourceType)
	}{
		// The capability equals the whole ID, so only the missing dot can
		// refuse it, not a prefix that differs from the capability.
		{"ID without a dot", "ID", func(rt *contract.ResourceType) {
			rt.ID = "aws"
		}},
		{"ID with an empty name after the dot", "ID", func(rt *contract.ResourceType) {
			rt.ID = "aws."
		}},
		{"ID with an uppercase name", "ID", func(rt *contract.ResourceType) {
			rt.ID = "aws.EcrRepository"
		}},
		{"ID with an underscore in the name", "ID", func(rt *contract.ResourceType) {
			rt.ID = "aws.ecr_repository"
		}},
		{"ID with a second dot in the name", "ID", func(rt *contract.ResourceType) {
			rt.ID = "aws.ecr.repository"
		}},
		{"ID prefix differs from the capability", "prefix", func(rt *contract.ResourceType) {
			rt.ID = "hetzner.ecr-repository"
		}},
		// The ID's prefix is empty too, so it equals the empty capability and
		// only the empty-capability rule can refuse it.
		{"empty capability", "capability", func(rt *contract.ResourceType) {
			rt.ID, rt.Capability = ".ecr-repository", ""
		}},
		{"empty kind", "kind", func(rt *contract.ResourceType) {
			rt.Kind = ""
		}},
		{"kind with uppercase letters", "kind", func(rt *contract.ResourceType) {
			rt.Kind = "Container-Repository"
		}},
		{"kind with an underscore", "kind", func(rt *contract.ResourceType) {
			rt.Kind = "container_repository"
		}},
		{"kind with a space", "kind", func(rt *contract.ResourceType) {
			rt.Kind = "container repository"
		}},
		{"empty category", "category", func(rt *contract.ResourceType) {
			rt.Category = ""
		}},
		{"category outside the vocabulary", "category", func(rt *contract.ResourceType) {
			rt.Category = "registry"
		}},
		{"category in the wrong case", "category", func(rt *contract.ResourceType) {
			rt.Category = "Artifact-Registry"
		}},
		{"empty lifecycle", "lifecycle", func(rt *contract.ResourceType) {
			rt.Lifecycle = ""
		}},
		{"lifecycle outside the vocabulary", "lifecycle", func(rt *contract.ResourceType) {
			rt.Lifecycle = "ephemeral"
		}},
		{"empty scope", "scope", func(rt *contract.ResourceType) {
			rt.Scope = ""
		}},
		{"scope outside the vocabulary", "scope", func(rt *contract.ResourceType) {
			rt.Scope = "global"
		}},
		{"language outside the vocabulary", "language", func(rt *contract.ResourceType) {
			rt.Languages = []string{"golang"}
		}},
		{"language repeated", "language", func(rt *contract.ResourceType) {
			rt.Languages = []string{"go", "node", "go"}
		}},
		{"nil languages", "language", func(rt *contract.ResourceType) {
			rt.Languages = nil
		}},
		{"artifact type outside the vocabulary", "artifact type", func(rt *contract.ResourceType) {
			rt.ArtifactType = "docker-image"
		}},
		{"priced without a pricing key", "pricing key", func(rt *contract.ResourceType) {
			rt.Priced, rt.PricingKey = true, ""
		}},
		{"pricing key without priced", "pricing key", func(rt *contract.ResourceType) {
			rt.Priced, rt.PricingKey = false, "AmazonEC2"
		}},
		{"nil outputs", "output", func(rt *contract.ResourceType) {
			rt.Outputs = nil
		}},
		{"output the registry does not hold", "output", func(rt *contract.ResourceType) {
			rt.Outputs = []string{"ecrRegistry", "noSuchOutput"}
		}},
		{"output named by a registry alias, not its canonical name", "output", func(rt *contract.ResourceType) {
			rt.Outputs = []string{"region"}
		}},
		{"output repeated", "output", func(rt *contract.ResourceType) {
			rt.Outputs = []string{"ecrRegistry", "registryHost", "ecrRegistry"}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rt := validType()
			c.mutate(&rt)
			err := contract.ValidateResourceTypes([]contract.ResourceType{rt})
			if err == nil {
				t.Fatalf("ValidateResourceTypes(%+v) = nil, want an error", rt)
			}
			if !strings.Contains(err.Error(), rt.ID) {
				t.Errorf("error %q does not name the type's ID %q", err, rt.ID)
			}
			// The ID is removed first so a fragment cannot match inside it.
			if rest := strings.ReplaceAll(err.Error(), rt.ID, ""); !strings.Contains(rest, c.rule) {
				t.Errorf("error %q does not carry %q, the rule that should refuse the type", err, c.rule)
			}
		})
	}
}

func TestValidateResourceTypes_RefusesADuplicateID(t *testing.T) {
	err := contract.ValidateResourceTypes([]contract.ResourceType{validType(), validType()})
	if err == nil {
		t.Fatal("ValidateResourceTypes with the same ID twice = nil, want an error")
	}
	if !strings.Contains(err.Error(), "aws.ecr-repository") {
		t.Errorf("error %q does not name the duplicated ID %q", err, "aws.ecr-repository")
	}
}

func TestValidateResourceTypes_NamesTheDefectiveTypeNotItsValidNeighbour(t *testing.T) {
	good := validType()
	bad := validType()
	bad.ID, bad.Capability, bad.Kind = "hetzner.volume", "hetzner", ""
	err := contract.ValidateResourceTypes([]contract.ResourceType{good, bad})
	if err == nil {
		t.Fatal("ValidateResourceTypes with an empty kind on the second entry = nil, want an error")
	}
	if !strings.Contains(err.Error(), "hetzner.volume") {
		t.Errorf("error %q does not name the defective type %q", err, "hetzner.volume")
	}
	if strings.Contains(err.Error(), good.ID) {
		t.Errorf("error %q names the valid type %q, want only the defective one", err, good.ID)
	}
}

func TestValidateResourceTypes_EmptyIDIsRefusedNamingItsIndex(t *testing.T) {
	a := validType()
	b := validType()
	b.ID, b.Capability = "hetzner.firewall", "hetzner"
	c := validType()
	c.ID, c.Capability = "github.commit", "github"
	empty := validType()
	empty.ID = ""
	err := contract.ValidateResourceTypes([]contract.ResourceType{a, b, c, empty})
	if err == nil {
		t.Fatal("ValidateResourceTypes with an empty ID = nil, want an error")
	}
	if !strings.Contains(err.Error(), "index 3") {
		t.Errorf("error %q does not name index 3, the entry with no ID", err)
	}
}

func TestResourceTypeCategories_AreExactlyTheADRVocabulary(t *testing.T) {
	want := []string{
		"compute", "network", "storage", "database", "artifact-registry", "build-and-delivery",
		"identity-and-access", "secrets", "observability", "messaging", "source-and-work", "ai-model",
	}
	assertVocabulary(t, "ResourceTypeCategories", contract.ResourceTypeCategories, want)
}

func TestResourceTypeLanguages_AreExactlyTheADRVocabulary(t *testing.T) {
	want := []string{"go", "node", "python", "java", "rust", "static-site", "any-container"}
	assertVocabulary(t, "ResourceTypeLanguages", contract.ResourceTypeLanguages, want)
}

func TestArtifactTypes_AreExactlyTheADRVocabulary(t *testing.T) {
	want := []string{"container-image", "helm-chart", "language-package", "binary", "static-bundle", "machine-image"}
	assertVocabulary(t, "ArtifactTypes", contract.ArtifactTypes, want)
}

func TestResourceTypeLifecycles_AreExactlyTheADRVocabulary(t *testing.T) {
	assertVocabulary(t, "ResourceTypeLifecycles", contract.ResourceTypeLifecycles, []string{"stateful", "replaceable"})
}

func TestResourceTypeScopes_AreExactlyTheADRVocabulary(t *testing.T) {
	assertVocabulary(t, "ResourceTypeScopes", contract.ResourceTypeScopes, []string{"per-app", "org-wide"})
}

// assertVocabulary checks that fn returns exactly want, in order, and that
// changing what it returned leaves the next call's result as it was.
func assertVocabulary(t *testing.T, name string, fn func() []string, want []string) {
	t.Helper()
	got := fn()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s() = %q, want %q", name, got, want)
	}
	for i := range got {
		got[i] = "mutated"
	}
	if again := fn(); !reflect.DeepEqual(again, want) {
		t.Errorf("mutating what %s returned changed a later call's result to %q", name, again)
	}
}

func TestResourceTypes_AreSortedByIDWithEachIDOnce(t *testing.T) {
	ts := catalogue(t)
	for i := 1; i < len(ts); i++ {
		if ts[i-1].ID >= ts[i].ID {
			t.Errorf("entry %d (%q) must sort strictly before entry %d (%q)", i-1, ts[i-1].ID, i, ts[i].ID)
		}
	}
}

func TestResourceTypes_ReturnsCopiesACallerCanMutate(t *testing.T) {
	before := contract.ResourceTypes()
	snapshot := make([]contract.ResourceType, len(before))
	for i, rt := range before {
		rt.Languages = append([]string{}, rt.Languages...)
		rt.Outputs = append([]string{}, rt.Outputs...)
		snapshot[i] = rt
	}

	got := catalogue(t)
	mutatedASlice := false
	for i := range got {
		for j := range got[i].Languages {
			got[i].Languages[j] = "mutated"
			mutatedASlice = true
		}
		for j := range got[i].Outputs {
			got[i].Outputs[j] = "mutated"
			mutatedASlice = true
		}
		got[i].ID = "mutated"
	}
	if !mutatedASlice {
		t.Fatal("no catalogue entry has a language or an output for this test to mutate")
	}

	if after := contract.ResourceTypes(); !reflect.DeepEqual(after, snapshot) {
		t.Error("mutating what ResourceTypes returned changed a later call's result")
	}
}

func TestLookupResourceType_FindsEveryCatalogueEntryByItsID(t *testing.T) {
	for _, rt := range catalogue(t) {
		got, ok := contract.LookupResourceType(rt.ID)
		if !ok || !reflect.DeepEqual(got, rt) {
			t.Errorf("LookupResourceType(%q) = %+v, %v; want the entry %+v", rt.ID, got, ok, rt)
		}
	}
}

func TestLookupResourceType_ReturnsACopyACallerCanMutate(t *testing.T) {
	first, ok := contract.LookupResourceType("aws.ecr-repository")
	if !ok {
		t.Fatal(`LookupResourceType("aws.ecr-repository") found nothing`)
	}
	if len(first.Languages) == 0 || len(first.Outputs) == 0 {
		t.Fatalf("aws.ecr-repository needs a language and an output for this test to mutate, got %+v", first)
	}
	want := first
	want.Languages = append([]string{}, first.Languages...)
	want.Outputs = append([]string{}, first.Outputs...)

	first.Languages[0] = "mutated"
	first.Outputs[0] = "mutated"

	if again, _ := contract.LookupResourceType("aws.ecr-repository"); !reflect.DeepEqual(again, want) {
		t.Errorf("mutating a looked-up type changed the catalogue: a later lookup returned %+v, want %+v", again, want)
	}
}

func TestLookupResourceType_UnknownOrMiscasedIDIsNotFound(t *testing.T) {
	for _, id := range []string{"", "github.repo", "GitHub.repository", "github", "repository", "aws.ecr-images"} {
		if rt, ok := contract.LookupResourceType(id); ok {
			t.Errorf("LookupResourceType(%q) found %q, want not found", id, rt.ID)
		}
	}
}

func TestResourceTypes_ADR0078FixesTheRepositoryCommitAndImageRows(t *testing.T) {
	want := []struct{ id, kind, capability string }{
		{"github.repository", "git-repository", "github"},
		{"github.commit", "source-revision", "github"},
		{"aws.ecr-image", "container-image", "aws"},
	}
	for _, w := range want {
		rt, ok := contract.LookupResourceType(w.id)
		if !ok {
			t.Errorf("the catalogue has no %q", w.id)
			continue
		}
		if rt.Kind != w.kind || rt.Capability != w.capability {
			t.Errorf("%q: kind %q, capability %q; want kind %q, capability %q", w.id, rt.Kind, rt.Capability, w.kind, w.capability)
		}
	}
}

// TestResourceTypes_AppRunnerResourcesMapToCataloguedTypes checks the three
// types app-runner's spec.resources entries map to. Its runner lanes are the
// ci-runner kind ADR 0078 has managed-ci and managed-cd require, publishing
// the four outputs that requirement reads, while its runner group and its host
// configuration publish nothing.
func TestResourceTypes_AppRunnerResourcesMapToCataloguedTypes(t *testing.T) {
	want := []struct {
		id, kind, capability string
		outputs              []string
	}{
		{"github.self-hosted-runner", "ci-runner", "github", []string{"runnerLabel", "runsOn", "runsOnPr", "awsCredential"}},
		{"github.runner-group", "runner-group", "github", nil},
		{"app-host.host-configuration", "host-configuration", "app-host", nil},
	}
	for _, w := range want {
		t.Run(w.id, func(t *testing.T) {
			rt, ok := contract.LookupResourceType(w.id)
			if !ok {
				t.Fatalf("the catalogue has no %q", w.id)
			}
			if rt.Kind != w.kind || rt.Capability != w.capability {
				t.Errorf("kind %q, capability %q; want kind %q, capability %q", rt.Kind, rt.Capability, w.kind, w.capability)
			}
			if rt.Outputs == nil {
				t.Error("Outputs is nil, want an empty list when the type publishes none")
			}
			if got, want := sortedCopy(rt.Outputs), sortedCopy(w.outputs); !reflect.DeepEqual(got, want) {
				t.Errorf("Outputs = %q, want %q", rt.Outputs, want)
			}
		})
	}
}

// TestResourceTypes_AppScaffoldResourcesMapToCataloguedTypes checks the two
// types app-scaffold's spec.resources entries map to. Its scaffold commit maps
// to github.commit, which publishes scaffoldCommit beside the source branch
// and sha, and its scaffold pull request maps to github.pull-request, a
// change-request publishing scaffoldPullRequest.
func TestResourceTypes_AppScaffoldResourcesMapToCataloguedTypes(t *testing.T) {
	want := []struct {
		id, kind, capability string
		outputs              []string
	}{
		{"github.commit", "source-revision", "github", []string{"sourceBranch", "sourceSha", "scaffoldCommit"}},
		{"github.pull-request", "change-request", "github", []string{"scaffoldPullRequest"}},
	}
	for _, w := range want {
		t.Run(w.id, func(t *testing.T) {
			rt, ok := contract.LookupResourceType(w.id)
			if !ok {
				t.Fatalf("the catalogue has no %q", w.id)
			}
			if rt.Kind != w.kind || rt.Capability != w.capability {
				t.Errorf("kind %q, capability %q; want kind %q, capability %q", rt.Kind, rt.Capability, w.kind, w.capability)
			}
			if rt.Outputs == nil {
				t.Error("Outputs is nil, want an empty list when the type publishes none")
			}
			if got, want := sortedCopy(rt.Outputs), sortedCopy(w.outputs); !reflect.DeepEqual(got, want) {
				t.Errorf("Outputs = %q, want %q", rt.Outputs, want)
			}
		})
	}
}

// TestResourceTypes_BoardAutomationResourcesMapToCataloguedTypes checks the
// types board-automation's spec.resources entries map to. Its tracker ledger
// token maps to tracker.api-token, its TRACKER_TOKEN Actions secret to
// github.actions-secret and its TRACKER_BASE_URL Actions variable to
// github.actions-variable, each a replaceable per-app type that is not priced
// and publishes nothing. Its board repository maps to github.repository, which
// publishes boardRepo beside its existing outputs, and its seeded workflows to
// github.workflow-file, which publishes ciCheckContribution beside workflowUrl.
func TestResourceTypes_BoardAutomationResourcesMapToCataloguedTypes(t *testing.T) {
	newTypes := []struct{ id, kind, capability, category string }{
		{"tracker.api-token", "api-token", "tracker", "identity-and-access"},
		{"github.actions-secret", "secret", "github", "secrets"},
		{"github.actions-variable", "ci-variable", "github", "build-and-delivery"},
	}
	for _, w := range newTypes {
		t.Run(w.id, func(t *testing.T) {
			rt, ok := contract.LookupResourceType(w.id)
			if !ok {
				t.Fatalf("the catalogue has no %q", w.id)
			}
			if rt.Kind != w.kind || rt.Capability != w.capability {
				t.Errorf("kind %q, capability %q; want kind %q, capability %q", rt.Kind, rt.Capability, w.kind, w.capability)
			}
			if rt.Category != w.category {
				t.Errorf("Category = %q, want %q", rt.Category, w.category)
			}
			if rt.Lifecycle != "replaceable" {
				t.Errorf("Lifecycle = %q, want %q", rt.Lifecycle, "replaceable")
			}
			if rt.Scope != "per-app" {
				t.Errorf("Scope = %q, want %q", rt.Scope, "per-app")
			}
			if rt.Priced || rt.PricingKey != "" {
				t.Errorf("Priced, PricingKey = %v, %q; want false, %q", rt.Priced, rt.PricingKey, "")
			}
			if rt.Languages == nil || len(rt.Languages) != 0 {
				t.Errorf("Languages = %#v, want an empty, non-nil list", rt.Languages)
			}
			if rt.ArtifactType != "" {
				t.Errorf("ArtifactType = %q, want %q", rt.ArtifactType, "")
			}
			if rt.Outputs == nil || len(rt.Outputs) != 0 {
				t.Errorf("Outputs = %#v, want an empty, non-nil list", rt.Outputs)
			}
		})
	}

	widened := []struct {
		id      string
		outputs []string
	}{
		{"github.repository", []string{"cdRepo", "ciRepo", "repo", "repoUrl", "boardRepo"}},
		{"github.workflow-file", []string{"workflowUrl", "ciCheckContribution"}},
	}
	for _, w := range widened {
		t.Run(w.id, func(t *testing.T) {
			rt, ok := contract.LookupResourceType(w.id)
			if !ok {
				t.Fatalf("the catalogue has no %q", w.id)
			}
			if got, want := sortedCopy(rt.Outputs), sortedCopy(w.outputs); !reflect.DeepEqual(got, want) {
				t.Errorf("Outputs = %q, want %q", rt.Outputs, want)
			}
		})
	}
}

// TestResourceTypes_CrossplaneReconcileClaimIsCatalogued checks the type
// crossplane-reconcile's one created resource maps to: the ReconcileClaim it
// applies on a Crossplane control plane. It is crossplane.reconcile-claim, a
// reconcile-claim of the crossplane capability in build-and-delivery, a
// replaceable per-app type that serves no particular language, holds no
// artifact and is not priced. It publishes the claim's name, its drift policy,
// the resources found drifted and the count of resources kept converged.
func TestResourceTypes_CrossplaneReconcileClaimIsCatalogued(t *testing.T) {
	const id = "crossplane.reconcile-claim"
	rt, ok := contract.LookupResourceType(id)
	if !ok {
		t.Fatalf("the catalogue has no %q", id)
	}
	if rt.Kind != "reconcile-claim" || rt.Capability != "crossplane" {
		t.Errorf("kind %q, capability %q; want kind %q, capability %q", rt.Kind, rt.Capability, "reconcile-claim", "crossplane")
	}
	if rt.Category != "build-and-delivery" {
		t.Errorf("Category = %q, want %q", rt.Category, "build-and-delivery")
	}
	if rt.Lifecycle != "replaceable" {
		t.Errorf("Lifecycle = %q, want %q", rt.Lifecycle, "replaceable")
	}
	if rt.Scope != "per-app" {
		t.Errorf("Scope = %q, want %q", rt.Scope, "per-app")
	}
	if rt.Priced || rt.PricingKey != "" {
		t.Errorf("Priced, PricingKey = %v, %q; want false, %q", rt.Priced, rt.PricingKey, "")
	}
	if rt.ArtifactType != "" {
		t.Errorf("ArtifactType = %q, want %q", rt.ArtifactType, "")
	}
	if rt.Languages == nil || len(rt.Languages) != 0 {
		t.Errorf("Languages = %#v, want an empty, non-nil list", rt.Languages)
	}
	if want := []string{"claim", "driftPolicy", "driftedResources", "reconciledResources"}; !reflect.DeepEqual(rt.Outputs, want) {
		t.Errorf("Outputs = %q, want %q in that order", rt.Outputs, want)
	}
}

// catalogueRow is one released resource type: its ID, its kind and capability,
// the six facets ADR 0077 gives a type, and its outputs. A nil languages or
// outputs list means the type has none, and pricingKey is "" when the type is
// not priced.
type catalogueRow struct {
	id, kind, capability, category string
	languages                      []string
	artifactType                   string
	lifecycle, scope               string
	pricingKey                     string
	outputs                        []string
}

// catalogueTable pins every type the kit releases, so adding, removing or
// changing a type means changing this table. The github.repository,
// github.commit, aws.ecr-image and github.self-hosted-runner rows carry the
// kinds and capabilities ADR 0078 fixes.
var catalogueTable = []catalogueRow{
	{"aws.ec2-instance", "host", "aws", "compute", []string{"any-container"}, "", "replaceable", "per-app", "AmazonEC2", []string{"execTarget", "host.type", "hostId", "instanceId", "publicIp"}},
	{"aws.security-group", "firewall", "aws", "network", nil, "", "replaceable", "per-app", "", nil},
	{"aws.ebs-volume", "block-volume", "aws", "storage", nil, "", "stateful", "per-app", "", []string{"dataSnapshotId"}},
	{"aws.ecr-repository", "container-repository", "aws", "artifact-registry", []string{"any-container"}, "container-image", "replaceable", "per-app", "", []string{"ecrRegistry", "registryHost"}},
	{"aws.ecr-image", "container-image", "aws", "artifact-registry", []string{"any-container"}, "container-image", "replaceable", "per-app", "", []string{"artifact", "imageRepo"}},
	{"aws.iam-role", "iam-role", "aws", "identity-and-access", nil, "", "replaceable", "per-app", "", []string{"deployRoleArn", "ecrPushRoleArn"}},
	{"aws.iam-oidc-provider", "oidc-provider", "aws", "identity-and-access", nil, "", "replaceable", "org-wide", "", []string{"oidcProviderArn"}},
	{"aws.ssm-association", "host-configuration", "aws", "compute", nil, "", "replaceable", "per-app", "", nil},
	{"aws.ssm-parameter", "secret", "aws", "secrets", nil, "", "stateful", "per-app", "", nil},
	{"aws.egress-proxy", "egress-proxy", "aws", "network", nil, "", "replaceable", "per-app", "AmazonEC2", nil},
	{"aws.vpc", "network", "aws", "network", nil, "", "replaceable", "per-app", "", nil},
	{"aws.route53-record", "dns-record", "aws", "network", nil, "", "replaceable", "per-app", "", []string{"appHostname"}},
	{"github.repository", "git-repository", "github", "source-and-work", nil, "", "stateful", "per-app", "", []string{"boardRepo", "cdRepo", "ciRepo", "repo", "repoUrl"}},
	{"github.commit", "source-revision", "github", "source-and-work", nil, "", "replaceable", "per-app", "", []string{"scaffoldCommit", "sourceBranch", "sourceSha"}},
	{"github.pull-request", "change-request", "github", "source-and-work", nil, "", "replaceable", "per-app", "", []string{"scaffoldPullRequest"}},
	{"github.repository-webhook", "webhook", "github", "build-and-delivery", nil, "", "replaceable", "per-app", "", nil},
	{"github.workflow-file", "ci-workflow", "github", "build-and-delivery", nil, "", "replaceable", "per-app", "", []string{"ciCheckContribution", "workflowUrl"}},
	{"github.self-hosted-runner", "ci-runner", "github", "build-and-delivery", nil, "", "replaceable", "per-app", "", []string{"awsCredential", "runnerLabel", "runsOn", "runsOnPr"}},
	{"github.runner-group", "runner-group", "github", "build-and-delivery", nil, "", "replaceable", "per-app", "", nil},
	{"github.actions-secret", "secret", "github", "secrets", nil, "", "replaceable", "per-app", "", nil},
	{"github.actions-variable", "ci-variable", "github", "build-and-delivery", nil, "", "replaceable", "per-app", "", nil},
	{"tracker.api-token", "api-token", "tracker", "identity-and-access", nil, "", "replaceable", "per-app", "", nil},
	{"crossplane.reconcile-claim", "reconcile-claim", "crossplane", "build-and-delivery", nil, "", "replaceable", "per-app", "", []string{"claim", "driftPolicy", "driftedResources", "reconciledResources"}},
	{"hetzner.server", "host", "hetzner", "compute", []string{"any-container"}, "", "replaceable", "per-app", "HetznerCloud", []string{"cloud.region", "execTarget", "host.type", "hostId"}},
	{"hetzner.primary-ip", "ip-address", "hetzner", "network", nil, "", "replaceable", "per-app", "", []string{"publicIp"}},
	{"hetzner.firewall", "firewall", "hetzner", "network", nil, "", "replaceable", "per-app", "", nil},
	{"hetzner.volume", "block-volume", "hetzner", "storage", nil, "", "stateful", "per-app", "", []string{"dataSnapshotId"}},
	{"hetzner.ssh-key", "ssh-key", "hetzner", "identity-and-access", nil, "", "replaceable", "per-app", "", nil},
	{"app-host.container", "container", "app-host", "compute", []string{"any-container"}, "", "replaceable", "per-app", "", []string{"url"}},
	{"app-host.postgres-database", "postgres-database", "app-host", "database", nil, "", "stateful", "per-app", "", []string{"dbName", "host", "port", "username"}},
	{"app-host.deployment", "deployment", "app-host", "build-and-delivery", []string{"any-container"}, "", "replaceable", "per-app", "", []string{"deployment"}},
	{"app-host.host-configuration", "host-configuration", "app-host", "compute", nil, "", "replaceable", "per-app", "", nil},
}

// sortedCopy returns s sorted, as a non-nil list, so two lists compare as sets.
func sortedCopy(s []string) []string {
	out := append([]string{}, s...)
	slices.Sort(out)
	return out
}

func TestResourceTypes_HoldExactlyThePinnedTypes(t *testing.T) {
	if len(catalogueTable) != 32 {
		t.Fatalf("catalogueTable has %d rows, want 32", len(catalogueTable))
	}
	if got := len(catalogue(t)); got != len(catalogueTable) {
		t.Errorf("ResourceTypes() holds %d types, want %d: a type the table does not pin was added, or a pinned one is missing", got, len(catalogueTable))
	}
	for _, row := range catalogueTable {
		t.Run(row.id, func(t *testing.T) {
			rt, ok := contract.LookupResourceType(row.id)
			if !ok {
				t.Fatalf("the catalogue has no %q", row.id)
			}
			if rt.Kind != row.kind {
				t.Errorf("Kind = %q, want %q", rt.Kind, row.kind)
			}
			if rt.Capability != row.capability {
				t.Errorf("Capability = %q, want %q", rt.Capability, row.capability)
			}
			if rt.Category != row.category {
				t.Errorf("Category = %q, want %q", rt.Category, row.category)
			}
			if got, want := sortedCopy(rt.Languages), sortedCopy(row.languages); !reflect.DeepEqual(got, want) {
				t.Errorf("Languages = %q, want %q", rt.Languages, want)
			}
			if rt.ArtifactType != row.artifactType {
				t.Errorf("ArtifactType = %q, want %q", rt.ArtifactType, row.artifactType)
			}
			if rt.Lifecycle != row.lifecycle {
				t.Errorf("Lifecycle = %q, want %q", rt.Lifecycle, row.lifecycle)
			}
			if rt.Scope != row.scope {
				t.Errorf("Scope = %q, want %q", rt.Scope, row.scope)
			}
			if wantPriced := row.pricingKey != ""; rt.Priced != wantPriced || rt.PricingKey != row.pricingKey {
				t.Errorf("Priced, PricingKey = %v, %q; want %v, %q", rt.Priced, rt.PricingKey, wantPriced, row.pricingKey)
			}
			if got, want := sortedCopy(rt.Outputs), sortedCopy(row.outputs); !reflect.DeepEqual(got, want) {
				t.Errorf("Outputs = %q, want %q", rt.Outputs, want)
			}
		})
	}
}

func TestOwnerships_AreExactlyCreatedThenReferenced(t *testing.T) {
	if contract.OwnershipCreated != "created" {
		t.Errorf("OwnershipCreated = %q, want %q", contract.OwnershipCreated, "created")
	}
	if contract.OwnershipReferenced != "referenced" {
		t.Errorf("OwnershipReferenced = %q, want %q", contract.OwnershipReferenced, "referenced")
	}
	want := []contract.Ownership{contract.OwnershipCreated, contract.OwnershipReferenced}
	got := contract.Ownerships()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Ownerships() = %q, want %q", got, want)
	}
	got[0] = "mutated"
	if again := contract.Ownerships(); !reflect.DeepEqual(again, want) {
		t.Errorf("mutating what Ownerships returned changed a later call's result to %q", again)
	}
}

func TestOwnership_ValidOnlyForCreatedAndReferenced(t *testing.T) {
	for _, o := range []contract.Ownership{"created", "referenced"} {
		if !o.Valid() {
			t.Errorf("Ownership(%q).Valid() = false, want true", o)
		}
	}
	for _, o := range []contract.Ownership{"", "owned", "Created", "REFERENCED", "created ", "reference"} {
		if o.Valid() {
			t.Errorf("Ownership(%q).Valid() = true, want false", o)
		}
	}
}

// marshalToMap marshals v and decodes the JSON object back into a map, so a
// test can assert on the wire keys.
func marshalToMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal(%+v): %v", v, err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	return m
}

func TestStep_MarshalsOwnershipUnderTheOwnershipKey(t *testing.T) {
	step := contract.Step{
		ID:        "image",
		Type:      "aws.ecr-image",
		Ownership: contract.OwnershipCreated,
		Inputs:    map[string]string{"repository": "steps.registry.outputs.ecrRegistry"},
	}
	m := marshalToMap(t, step)
	want := map[string]any{
		"id":        "image",
		"type":      "aws.ecr-image",
		"ownership": "created",
		"inputs":    map[string]any{"repository": "steps.registry.outputs.ecrRegistry"},
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("Step marshals to %v, want %v", m, want)
	}

	var back contract.Step
	raw, _ := json.Marshal(step)
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	if !reflect.DeepEqual(back, step) {
		t.Errorf("Step round-trips to %+v, want %+v", back, step)
	}
}

func TestStep_OmitsEmptyInputs(t *testing.T) {
	m := marshalToMap(t, contract.Step{ID: "repo", Type: "github.repository", Ownership: contract.OwnershipReferenced})
	if _, ok := m["inputs"]; ok {
		t.Errorf("Step with no inputs marshals an inputs key: %v", m)
	}
	if m["ownership"] != "referenced" {
		t.Errorf(`Step marshals ownership as %v, want "referenced"`, m["ownership"])
	}
}

func TestSuppliedResource_MarshalsOwnershipUnderTheOwnershipKey(t *testing.T) {
	sr := contract.SuppliedResource{
		Requirement: "source",
		Type:        "github.repository",
		Ownership:   contract.OwnershipReferenced,
	}
	m := marshalToMap(t, sr)
	want := map[string]any{
		"requirement": "source",
		"type":        "github.repository",
		"ownership":   "referenced",
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("SuppliedResource marshals to %v, want %v", m, want)
	}
}

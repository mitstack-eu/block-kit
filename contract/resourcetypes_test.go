package contract_test

// resourcetypes_test.go pins the resource-type catalogue the kit owns: the
// facet vocabularies, the rules ValidateResourceTypes enforces, the released
// catalogue's content, and the Ownership a supplied resource carries.

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/operator-kit/contract"
)

// validBudgets returns a fresh time budget for every resource verb.
func validBudgets() map[string]time.Duration {
	return map[string]time.Duration{
		"ensure":  5 * time.Minute,
		"observe": 30 * time.Second,
		"delete":  5 * time.Minute,
	}
}

// validType returns a fresh resource type every rule accepts, for a test to
// mutate one field of. It is an ECR repository of the container-repository
// kind, which requires registryHost.
func validType() contract.ResourceType {
	return contract.ResourceType{
		ID:           "aws.ecr-repository",
		Kind:         "container-repository",
		ReliesOn:     []string{},
		Hosts:        "",
		Category:     "artifact-registry",
		Languages:    []string{"any-container"},
		ArtifactType: "container-image",
		Lifecycle:    "replaceable",
		Handover:     "",
		Upgrade:      "in-place",
		Scope:        "per-app",
		Priced:       false,
		PricingKey:   "",
		Inputs: []contract.Input{
			{Name: "name", Required: true, Secret: false, Accepts: "value", Replacement: "replace"},
			{Name: "image_tag_mutability", Required: false, Secret: false, Accepts: "value", Replacement: "update"},
		},
		Outputs:     []string{"registryHost"},
		Observe:     "DescribeRepositories finds the repository by name",
		Absence:     "DescribeRepositories answers RepositoryNotFoundException for the name",
		TimeBudgets: validBudgets(),
		Failures: []contract.Failure{
			{Class: "transient", Cause: "the ECR API throttled the call"},
			{Class: "permanent", Cause: "the repository name is taken by another account's repository"},
		},
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

// copyType returns a copy of rt that shares no list, map or pointer with it.
func copyType(rt contract.ResourceType) contract.ResourceType {
	rt.ReliesOn = append([]string{}, rt.ReliesOn...)
	rt.Languages = append([]string{}, rt.Languages...)
	rt.Inputs = append([]contract.Input{}, rt.Inputs...)
	rt.Outputs = append([]string{}, rt.Outputs...)
	rt.Failures = append([]contract.Failure{}, rt.Failures...)
	budgets := make(map[string]time.Duration, len(rt.TimeBudgets))
	for k, v := range rt.TimeBudgets {
		budgets[k] = v
	}
	rt.TimeBudgets = budgets
	if rt.ProviderResource != nil {
		pr := *rt.ProviderResource
		pr.ReplaceOn = append([]string{}, pr.ReplaceOn...)
		rt.ProviderResource = &pr
	}
	return rt
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
		"a kind with no outputs, empty languages and inputs and no artifact type": func(rt *contract.ResourceType) {
			rt.ID, rt.Kind, rt.Category = "aws.security-group", "firewall", "network"
			rt.Languages, rt.Outputs, rt.Inputs, rt.ArtifactType = []string{}, []string{}, []contract.Input{}, ""
		},
		"several distinct languages": func(rt *contract.ResourceType) {
			rt.Languages = []string{"go", "node", "python"}
		},
		"a type that lives on a kind": func(rt *contract.ResourceType) {
			rt.ID, rt.Kind, rt.Category, rt.ArtifactType = "host.thing", "container", "compute", ""
			rt.Outputs = []string{"url"}
		},
		"a type producing only its kind's required outputs, with dotted optional ones left out": func(rt *contract.ResourceType) {
			rt.ID, rt.Kind, rt.Category, rt.ArtifactType = "hetzner.thing", "host", "compute", ""
			rt.Outputs = []string{"execKind", "execTarget", "hostId"}
		},
		"a type producing dotted optional outputs of its kind": func(rt *contract.ResourceType) {
			rt.ID, rt.Kind, rt.Category, rt.ArtifactType = "hetzner.thing", "host", "compute", ""
			rt.Outputs = []string{"cloud.region", "execKind", "execTarget", "host.type", "hostId"}
		},
		"a type relying on another capability": func(rt *contract.ResourceType) {
			rt.ID, rt.Kind, rt.Category, rt.ArtifactType = "ubicloud.thing", "ci-runner", "build-and-delivery", ""
			rt.Outputs, rt.ReliesOn = []string{"runsOn"}, []string{"github"}
		},
		"a type hosting a capability": func(rt *contract.ResourceType) {
			rt.ID, rt.Kind, rt.Category, rt.ArtifactType = "hetzner.thing", "kubernetes-cluster", "compute", ""
			rt.Outputs, rt.Hosts = []string{"apiEndpoint", "caCertificate", "clusterCredential"}, "kubernetes"
		},
		"a stateful type with a snapshot handover": func(rt *contract.ResourceType) {
			rt.Lifecycle, rt.Handover = "stateful", "snapshot"
		},
		"a secret input": func(rt *contract.ResourceType) {
			rt.Inputs = append(rt.Inputs, contract.Input{Name: "token", Required: true, Secret: true, Accepts: "value", Replacement: "update"})
		},
		"an input accepting a whole resource of a kind": func(rt *contract.ResourceType) {
			rt.Inputs = append(rt.Inputs, contract.Input{Name: "host", Required: true, Accepts: "resource:host", Replacement: "replace"})
		},
		"an input accepting an output of a kind": func(rt *contract.ResourceType) {
			rt.Inputs = append(rt.Inputs, contract.Input{Name: "target", Required: true, Accepts: "output:host/publicIp", Replacement: "update"})
		},
		"a type mapped one to one onto a provider resource": func(rt *contract.ResourceType) {
			rt.ProviderResource = &contract.ProviderResource{
				Provider: "registry.opentofu.org/hashicorp/aws", Resource: "aws_ecr_repository", ReplaceOn: []string{"name"},
			}
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
		if l == "stateful" {
			rt.Handover = "snapshot"
		}
		check(t, rt)
	}
	for _, h := range contract.Handovers() {
		rt := validType()
		rt.Lifecycle, rt.Handover = "stateful", h
		check(t, rt)
	}
	for _, s := range contract.ResourceTypeScopes() {
		rt := validType()
		rt.Scope = s
		check(t, rt)
	}
	for _, u := range contract.UpgradeBehaviours() {
		rt := validType()
		rt.Upgrade = u
		check(t, rt)
	}
	for _, r := range contract.InputReplacements() {
		rt := validType()
		rt.Inputs[0].Replacement = r
		check(t, rt)
	}
	for _, c := range contract.FailureClasses() {
		rt := validType()
		rt.Failures = []contract.Failure{{Class: c, Cause: "a cause"}}
		check(t, rt)
	}
	for _, c := range contract.Capabilities() {
		rt := validType()
		rt.ReliesOn = []string{c}
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
		{"ID without a dot", "ID", func(rt *contract.ResourceType) { rt.ID = "aws" }},
		{"ID with an empty name after the dot", "ID", func(rt *contract.ResourceType) { rt.ID = "aws." }},
		{"ID with an empty place before the dot", "ID", func(rt *contract.ResourceType) { rt.ID = ".ecr-repository" }},
		{"ID with an uppercase name", "ID", func(rt *contract.ResourceType) { rt.ID = "aws.EcrRepository" }},
		{"ID with an underscore in the name", "ID", func(rt *contract.ResourceType) { rt.ID = "aws.ecr_repository" }},
		{"ID with a second dot in the name", "ID", func(rt *contract.ResourceType) { rt.ID = "aws.ecr.repository" }},
		{"ID prefix that is neither a capability nor a kind", "place", func(rt *contract.ResourceType) {
			rt.ID = "nowhere.ecr-repository"
		}},
		{"ID prefix of a retired capability", "place", func(rt *contract.ResourceType) {
			rt.ID = "cloudformation.ecr-repository"
		}},
		{"empty kind", "kind", func(rt *contract.ResourceType) { rt.Kind = "" }},
		{"kind with uppercase letters", "kind", func(rt *contract.ResourceType) { rt.Kind = "Container-Repository" }},
		{"kind that is not a released kind", "kind", func(rt *contract.ResourceType) { rt.Kind = "message-queue" }},
		{"empty category", "category", func(rt *contract.ResourceType) { rt.Category = "" }},
		{"category outside the vocabulary", "category", func(rt *contract.ResourceType) { rt.Category = "registry" }},
		{"empty lifecycle", "lifecycle", func(rt *contract.ResourceType) { rt.Lifecycle = "" }},
		{"lifecycle outside the vocabulary", "lifecycle", func(rt *contract.ResourceType) { rt.Lifecycle = "ephemeral" }},
		{"replaceable lifecycle with a handover", "handover", func(rt *contract.ResourceType) { rt.Handover = "snapshot" }},
		{"handover outside the vocabulary", "handover", func(rt *contract.ResourceType) {
			rt.Lifecycle, rt.Handover = "stateful", "teleport"
		}},
		{"empty upgrade behaviour", "upgrade", func(rt *contract.ResourceType) { rt.Upgrade = "" }},
		{"upgrade behaviour outside the vocabulary", "upgrade", func(rt *contract.ResourceType) { rt.Upgrade = "rolling" }},
		{"empty scope", "scope", func(rt *contract.ResourceType) { rt.Scope = "" }},
		{"scope outside the vocabulary", "scope", func(rt *contract.ResourceType) { rt.Scope = "global" }},
		{"language outside the vocabulary", "language", func(rt *contract.ResourceType) { rt.Languages = []string{"golang"} }},
		{"language repeated", "language", func(rt *contract.ResourceType) { rt.Languages = []string{"go", "node", "go"} }},
		{"nil languages", "language", func(rt *contract.ResourceType) { rt.Languages = nil }},
		{"artifact type outside the vocabulary", "artifact type", func(rt *contract.ResourceType) { rt.ArtifactType = "docker-image" }},
		{"priced without a pricing key", "pricing key", func(rt *contract.ResourceType) { rt.Priced, rt.PricingKey = true, "" }},
		{"pricing key without priced", "pricing key", func(rt *contract.ResourceType) { rt.Priced, rt.PricingKey = false, "AmazonEC2" }},
		{"nil outputs", "output", func(rt *contract.ResourceType) { rt.Outputs = nil }},
		{"output the registry does not hold", "output", func(rt *contract.ResourceType) {
			rt.Outputs = []string{"registryHost", "noSuchOutput"}
		}},
		{"output named by a registry alias, not its canonical name", "output", func(rt *contract.ResourceType) {
			rt.Outputs = []string{"registryHost", "region"}
		}},
		{"output repeated", "output", func(rt *contract.ResourceType) {
			rt.Outputs = []string{"registryHost", "registryHost"}
		}},
		{"nil reliesOn", "reliesOn", func(rt *contract.ResourceType) { rt.ReliesOn = nil }},
		{"reliesOn naming no capability", "reliesOn", func(rt *contract.ResourceType) { rt.ReliesOn = []string{"tracker"} }},
		{"reliesOn naming a capability twice", "reliesOn", func(rt *contract.ResourceType) { rt.ReliesOn = []string{"github", "github"} }},
		{"hosts naming no capability", "hosts", func(rt *contract.ResourceType) { rt.Hosts = "kube-cluster" }},
		{"nil inputs", "input", func(rt *contract.ResourceType) { rt.Inputs = nil }},
		{"input with no name", "input", func(rt *contract.ResourceType) { rt.Inputs[0].Name = "" }},
		{"input listed twice", "name", func(rt *contract.ResourceType) { rt.Inputs[1].Name = "name" }},
		{"input replacement outside the vocabulary", "replacement", func(rt *contract.ResourceType) {
			rt.Inputs[0].Replacement = "sometimes"
		}},
		{"input accepting nothing", "accepts", func(rt *contract.ResourceType) { rt.Inputs[0].Accepts = "" }},
		{"input accepting a resource of no kind", "accepts", func(rt *contract.ResourceType) {
			rt.Inputs[0].Accepts = "resource:no-such-kind"
		}},
		{"input accepting an output its kind does not name", "accepts", func(rt *contract.ResourceType) {
			rt.Inputs[0].Accepts = "output:host/repo"
		}},
		{"input accepting a form outside the grammar", "accepts", func(rt *contract.ResourceType) {
			rt.Inputs[0].Accepts = "anything"
		}},
		{"no failure declared", "failure", func(rt *contract.ResourceType) { rt.Failures = nil }},
		{"failure class outside the vocabulary", "failure", func(rt *contract.ResourceType) {
			rt.Failures = []contract.Failure{{Class: "retryable", Cause: "a cause"}}
		}},
		{"failure with no cause", "failure", func(rt *contract.ResourceType) {
			rt.Failures = []contract.Failure{{Class: "transient", Cause: ""}}
		}},
		{"time budget for a verb that is not a resource verb", "time budget", func(rt *contract.ResourceType) {
			rt.TimeBudgets["subscribe"] = time.Minute
		}},
		{"negative time budget", "time budget", func(rt *contract.ResourceType) {
			rt.TimeBudgets["delete"] = -time.Minute
		}},
		{"provider resource with no provider", "provider", func(rt *contract.ResourceType) {
			rt.ProviderResource = &contract.ProviderResource{Resource: "aws_ecr_repository", ReplaceOn: []string{}}
		}},
		{"provider resource with no resource", "provider", func(rt *contract.ResourceType) {
			rt.ProviderResource = &contract.ProviderResource{Provider: "registry.opentofu.org/hashicorp/aws", ReplaceOn: []string{}}
		}},
		{"provider resource replacing on an input the type does not declare", "provider", func(rt *contract.ResourceType) {
			rt.ProviderResource = &contract.ProviderResource{
				Provider: "registry.opentofu.org/hashicorp/aws", Resource: "aws_ecr_repository", ReplaceOn: []string{"no_such_input"},
			}
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
	bad.ID, bad.Kind = "hetzner.volume", ""
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
	b.ID = "hetzner.ecr-repository"
	c := validType()
	c.ID = "github.ecr-repository"
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
		snapshot[i] = copyType(rt)
	}

	got := catalogue(t)
	mutated := map[string]bool{}
	for i := range got {
		for j := range got[i].Languages {
			got[i].Languages[j] = "mutated"
			mutated["languages"] = true
		}
		for j := range got[i].Outputs {
			got[i].Outputs[j] = "mutated"
			mutated["outputs"] = true
		}
		for j := range got[i].Inputs {
			got[i].Inputs[j].Name = "mutated"
			mutated["inputs"] = true
		}
		for j := range got[i].ReliesOn {
			got[i].ReliesOn[j] = "mutated"
			mutated["reliesOn"] = true
		}
		for j := range got[i].Failures {
			got[i].Failures[j].Class = "mutated"
			mutated["failures"] = true
		}
		for verb := range got[i].TimeBudgets {
			got[i].TimeBudgets[verb] = -1
			mutated["time budgets"] = true
		}
		if pr := got[i].ProviderResource; pr != nil {
			pr.Resource = "mutated"
			for j := range pr.ReplaceOn {
				pr.ReplaceOn[j] = "mutated"
			}
			mutated["provider resource"] = true
		}
		got[i].ID = "mutated"
	}
	for _, what := range []string{"languages", "outputs", "inputs", "reliesOn", "failures", "time budgets", "provider resource"} {
		if !mutated[what] {
			t.Errorf("no catalogue entry has %s for this test to mutate", what)
		}
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
	if len(first.Languages) == 0 || len(first.Outputs) == 0 || len(first.Inputs) == 0 || first.ProviderResource == nil {
		t.Fatalf("aws.ecr-repository needs a language, an output, an input and a provider resource for this test to mutate, got %+v", first)
	}
	want := copyType(first)

	first.Languages[0] = "mutated"
	first.Outputs[0] = "mutated"
	first.Inputs[0].Name = "mutated"
	first.TimeBudgets["ensure"] = -1
	first.ProviderResource.Resource = "mutated"

	if again, _ := contract.LookupResourceType("aws.ecr-repository"); !reflect.DeepEqual(again, want) {
		t.Errorf("mutating a looked-up type changed the catalogue: a later lookup returned %+v, want %+v", again, want)
	}
}

func TestLookupResourceType_UnknownOrMiscasedIDIsNotFound(t *testing.T) {
	for _, id := range []string{"", "github.repo", "GitHub.repository", "github", "repository", "aws.ecr-images", "tracker.api-token", "app-host.container"} {
		if rt, ok := contract.LookupResourceType(id); ok {
			t.Errorf("LookupResourceType(%q) found %q, want not found", id, rt.ID)
		}
	}
}

func TestResourceTypes_ADR0078FixesTheRepositoryCommitAndImageRows(t *testing.T) {
	want := []struct{ id, kind, place string }{
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
		if rt.Kind != w.kind || rt.Place() != w.place {
			t.Errorf("%q: kind %q, place %q; want kind %q, place %q", w.id, rt.Kind, rt.Place(), w.kind, w.place)
		}
	}
}

// TestResourceTypes_CrossplaneReconcileClaimIsCatalogued checks the type
// crossplane-reconcile's one created resource maps to: the ReconcileClaim it
// applies on a Crossplane control plane. It is crossplane.reconcile-claim, a
// reconcile-claim living in crossplane in build-and-delivery, a replaceable
// per-app type that serves no particular language, holds no artifact and is
// not priced. It publishes the claim's name, its drift policy, the resources
// found drifted and the count of resources kept converged.
func TestResourceTypes_CrossplaneReconcileClaimIsCatalogued(t *testing.T) {
	const id = "crossplane.reconcile-claim"
	rt, ok := contract.LookupResourceType(id)
	if !ok {
		t.Fatalf("the catalogue has no %q", id)
	}
	if rt.Kind != "reconcile-claim" || rt.Place() != "crossplane" {
		t.Errorf("kind %q, place %q; want kind %q, place %q", rt.Kind, rt.Place(), "reconcile-claim", "crossplane")
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

// TestValidateResourceTypes_RefusesAReleasedNameGivenAnotherKind adds a
// hetzner.vpc of kind virtual-network beside the released aws.vpc of kind
// network, and checks ValidateResourceTypes refuses the catalogue naming both
// IDs and both kinds: a name after the dot names one real thing in every
// place, so every type sharing a name shares its kind.
func TestValidateResourceTypes_RefusesAReleasedNameGivenAnotherKind(t *testing.T) {
	ts := catalogue(t)
	clash := validType()
	clash.ID, clash.Kind, clash.Category, clash.ArtifactType = "hetzner.vpc", "network", "network", ""
	clash.Languages, clash.Outputs = []string{}, []string{}
	if err := contract.ValidateResourceTypes(append(ts, clash)); err != nil {
		t.Fatalf("ValidateResourceTypes with hetzner.vpc of kind network beside aws.vpc = %v, want nil so only the kind can refuse the clash", err)
	}
	clash.Kind = "firewall"
	err := contract.ValidateResourceTypes(append(ts, clash))
	if err == nil {
		t.Fatal("ValidateResourceTypes with hetzner.vpc of kind firewall beside aws.vpc of kind network = nil, want an error")
	}
	for _, want := range []string{"hetzner.vpc", "aws.vpc", `"firewall"`, `"network"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

// TestValidateResourceTypes_RefusesASharedNameWithTwoKinds checks the
// shared-name rule on two synthetic types that are each valid alone, so the
// rule is pinned apart from the released catalogue. Both orders are checked,
// since the rule must not depend on which type comes first.
func TestValidateResourceTypes_RefusesASharedNameWithTwoKinds(t *testing.T) {
	a := validType()
	a.ID, a.Kind, a.Category, a.ArtifactType, a.Outputs = "aws.thing", "firewall", "network", "", []string{}
	b := validType()
	b.ID, b.Kind, b.Category, b.ArtifactType, b.Outputs = "hetzner.thing", "network", "network", "", []string{}
	for _, rt := range []contract.ResourceType{a, b} {
		if err := contract.ValidateResourceTypes([]contract.ResourceType{rt}); err != nil {
			t.Fatalf("ValidateResourceTypes(%+v) alone = %v, want nil so only the shared name can refuse the pair", rt, err)
		}
	}
	orders := map[string][]contract.ResourceType{
		"aws first":     {a, b},
		"hetzner first": {b, a},
	}
	for name, ts := range orders {
		t.Run(name, func(t *testing.T) {
			err := contract.ValidateResourceTypes(ts)
			if err == nil {
				t.Fatal("ValidateResourceTypes with aws.thing of kind firewall and hetzner.thing of kind network = nil, want an error")
			}
			for _, want := range []string{"aws.thing", "hetzner.thing", `"firewall"`, `"network"`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %s", err, want)
				}
			}
		})
	}
}

// TestValidateResourceTypes_AcceptsASharedNameWithOneKind checks the
// shared-name rule refuses only a kind mismatch: two types in different
// places may share a name when they carry the same kind.
func TestValidateResourceTypes_AcceptsASharedNameWithOneKind(t *testing.T) {
	a := validType()
	a.ID = "aws.thing"
	b := validType()
	b.ID = "hetzner.thing"
	if err := contract.ValidateResourceTypes([]contract.ResourceType{a, b}); err != nil {
		t.Errorf("ValidateResourceTypes with aws.thing and hetzner.thing both of kind container-repository = %v, want nil", err)
	}
}

// catalogueRow is one released resource type: its ID, its kind, the catalogue
// facets, its outputs, the capabilities it relies on and the capability it
// hosts. A nil languages, outputs or reliesOn list means the type has none,
// and pricingKey is "" when the type is not priced.
type catalogueRow struct {
	id, kind, category string
	languages          []string
	artifactType       string
	lifecycle, scope   string
	pricingKey         string
	outputs            []string
	reliesOn           []string
	hosts              string
}

// catalogueTable pins every type the kit releases, so adding, removing or
// changing a type means changing this table. Each type lives in the place its
// id prefix names: a capability, or, for the host types, a resource of the
// host kind.
var catalogueTable = []catalogueRow{
	{"aws.ebs-volume", "block-volume", "storage", nil, "", "stateful", "per-app", "", []string{"dataSnapshotId"}, nil, ""},
	{"aws.ec2-instance", "host", "compute", []string{"any-container"}, "", "replaceable", "per-app", "AmazonEC2", []string{"cloud.region", "execKind", "execTarget", "host.type", "hostId", "publicIp"}, nil, ""},
	{"aws.ecr-image", "container-image", "artifact-registry", []string{"any-container"}, "container-image", "replaceable", "per-app", "", []string{"imageRepo"}, nil, ""},
	{"aws.ecr-repository", "container-repository", "artifact-registry", []string{"any-container"}, "container-image", "replaceable", "per-app", "", []string{"registryHost"}, nil, ""},
	{"aws.egress-proxy", "egress-proxy", "network", nil, "", "replaceable", "per-app", "AmazonEC2", nil, nil, ""},
	{"aws.iam-oidc-provider", "oidc-provider", "identity-and-access", nil, "", "replaceable", "org-wide", "", []string{"oidcProviderArn"}, nil, ""},
	{"aws.iam-role", "iam-role", "identity-and-access", nil, "", "replaceable", "per-app", "", []string{"roleArn"}, nil, ""},
	{"aws.route53-record", "app-address", "network", nil, "", "replaceable", "per-app", "", []string{"appHostname", "appUrl"}, nil, ""},
	{"aws.security-group", "firewall", "network", nil, "", "replaceable", "per-app", "", nil, nil, ""},
	{"aws.ssm-association", "host-configuration", "compute", nil, "", "replaceable", "per-app", "", nil, nil, ""},
	{"aws.ssm-parameter", "secret", "secrets", nil, "", "stateful", "per-app", "", nil, nil, ""},
	{"aws.vllm-instance", "llm-endpoint", "ai-model", nil, "", "replaceable", "per-app", "AmazonEC2", []string{"llmEndpoint", "model"}, nil, ""},
	{"aws.vpc", "network", "network", nil, "", "replaceable", "per-app", "", nil, nil, ""},
	{"crossplane.reconcile-claim", "reconcile-claim", "build-and-delivery", nil, "", "replaceable", "per-app", "", []string{"claim", "driftPolicy", "driftedResources", "reconciledResources"}, nil, ""},
	{"github.actions-secret", "secret", "secrets", nil, "", "replaceable", "per-app", "", nil, nil, ""},
	{"github.actions-variable", "ci-variable", "build-and-delivery", nil, "", "replaceable", "per-app", "", nil, nil, ""},
	{"github.commit", "source-revision", "source-and-work", nil, "", "replaceable", "per-app", "", []string{"sourceBranch", "sourceSha"}, nil, ""},
	{"github.container-image", "container-image", "artifact-registry", []string{"any-container"}, "container-image", "replaceable", "per-app", "", []string{"imageRepo"}, nil, ""},
	{"github.pull-request", "change-request", "source-and-work", nil, "", "replaceable", "per-app", "", []string{"pullRequestUrl"}, nil, ""},
	{"github.repository", "git-repository", "source-and-work", nil, "", "stateful", "per-app", "", []string{"repo"}, nil, ""},
	{"github.repository-webhook", "webhook", "build-and-delivery", nil, "", "replaceable", "per-app", "", nil, nil, ""},
	{"github.runner-group", "runner-group", "build-and-delivery", nil, "", "replaceable", "per-app", "", nil, nil, ""},
	{"github.scaffold-contribution", "scaffold-contribution", "source-and-work", nil, "", "replaceable", "per-app", "", []string{"scaffoldContribution"}, nil, ""},
	{"github.self-hosted-runner", "ci-runner", "build-and-delivery", nil, "", "replaceable", "per-app", "", []string{"awsCredential", "runnerLabel", "runsOn"}, nil, ""},
	{"github.workflow-file", "ci-workflow", "build-and-delivery", nil, "", "replaceable", "per-app", "", []string{"ciCheckContribution", "workflowPath", "workflowUrl"}, nil, ""},
	{"hetzner.firewall", "firewall", "network", nil, "", "replaceable", "per-app", "", nil, nil, ""},
	{"hetzner.k3s-cluster", "kubernetes-cluster", "compute", nil, "", "replaceable", "org-wide", "HetznerCloud", []string{"apiEndpoint", "caCertificate", "clusterCredential", "cloud.region", "issuerUrl", "privateEndpoint"}, nil, "kubernetes"},
	{"hetzner.primary-ip", "ip-address", "network", nil, "", "replaceable", "per-app", "", []string{"publicIp"}, nil, ""},
	{"hetzner.server", "host", "compute", []string{"any-container"}, "", "replaceable", "per-app", "HetznerCloud", []string{"cloud.region", "execKind", "execTarget", "host.type", "hostId"}, nil, ""},
	{"hetzner.ssh-key", "ssh-key", "identity-and-access", nil, "", "replaceable", "per-app", "", nil, nil, ""},
	{"hetzner.volume", "block-volume", "storage", nil, "", "stateful", "per-app", "", []string{"dataSnapshotId"}, nil, ""},
	{"host.configuration", "host-configuration", "compute", nil, "", "replaceable", "per-app", "", nil, nil, ""},
	{"host.container", "container", "compute", []string{"any-container"}, "", "replaceable", "per-app", "", []string{"url"}, nil, ""},
	{"host.postgres-database", "postgres-database", "database", nil, "", "stateful", "per-app", "", []string{"dbName", "host", "password", "port", "username"}, nil, ""},
	{"plain.api-token", "api-token", "identity-and-access", nil, "", "replaceable", "per-app", "", nil, nil, ""},
	{"ubicloud.runner", "ci-runner", "build-and-delivery", nil, "", "replaceable", "per-app", "", []string{"runsOn"}, []string{"github"}, ""},
}

// sortedCopy returns s sorted, as a non-nil list, so two lists compare as sets.
func sortedCopy(s []string) []string {
	out := append([]string{}, s...)
	slices.Sort(out)
	return out
}

func TestResourceTypes_HoldExactlyThePinnedTypes(t *testing.T) {
	if len(catalogueTable) != 36 {
		t.Fatalf("catalogueTable has %d rows, want 36", len(catalogueTable))
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
			if got, want := sortedCopy(rt.ReliesOn), sortedCopy(row.reliesOn); !reflect.DeepEqual(got, want) {
				t.Errorf("ReliesOn = %q, want %q", rt.ReliesOn, want)
			}
			if rt.Hosts != row.hosts {
				t.Errorf("Hosts = %q, want %q", rt.Hosts, row.hosts)
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

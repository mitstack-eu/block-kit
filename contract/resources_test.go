package contract_test

// resources_test.go pins the resource registry the kit owns: the output names
// a block may declare in spec.outputs and read under spec.requires[].reads.
// The platform imports this registry, so the names, kinds, aliases, secret
// flags and capabilities held here are the platform's; the per-entry table
// below is test data drawn from it, not a second registry.

import (
	"reflect"
	"sort"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

// registryEntry is what the table pins of each entry: the fields a verdict
// or a lookup depends on. Meaning, shape and example are prose and are
// checked for presence only.
type registryEntry struct {
	kind         string
	secret       bool
	aliases      []string
	capabilities []string
}

// platformRegistry is every entry of the platform's resource registry, by
// name.
var platformRegistry = map[string]registryEntry{
	"agentEndpoint":        {kind: "Coding agent", capabilities: []string{"agent-session"}},
	"appHostname":          {kind: "Host & network", capabilities: []string{"app-host", "aws-topology"}},
	"appName":              {kind: "App", capabilities: []string{"database"}},
	"appUrl":               {kind: "Host & network", capabilities: []string{"app-host", "aws-topology"}},
	"artifact":             {kind: "Build & deploy", capabilities: []string{"artifact"}},
	"awsCredential":        {kind: "Access", capabilities: []string{"runner"}},
	"boardRepo":            {kind: "Work", capabilities: []string{"ci-check"}},
	"buildRequestId":       {kind: "Coding agent", capabilities: []string{"generated-source"}},
	"cdRepo":               {kind: "Build & deploy", capabilities: []string{"deployment"}},
	"certTrusted":          {kind: "Host & network", capabilities: []string{"app-host", "aws-topology"}},
	"ciCheckContribution":  {kind: "Work", capabilities: []string{"ci-check"}},
	"ciRepo":               {kind: "Build & deploy", capabilities: []string{"artifact"}},
	"claim":                {kind: "Host & network", capabilities: []string{"reconcile/crossplane"}},
	"cloud.region":         {kind: "Host & network", aliases: []string{"region"}, capabilities: []string{"app-host", "aws-topology"}},
	"dataSnapshotId":       {kind: "Data", capabilities: []string{"aws-topology"}},
	"dbName":               {kind: "Data", capabilities: []string{"database"}},
	"deployRoleArn":        {kind: "Access", secret: true, capabilities: []string{"aws-iam", "aws-topology"}},
	"deployment":           {kind: "Build & deploy", capabilities: []string{"deployment"}},
	"driftPolicy":          {kind: "Host & network", capabilities: []string{"reconcile/crossplane"}},
	"driftedResources":     {kind: "Host & network", capabilities: []string{"reconcile/crossplane"}},
	"dshSessionUrl":        {kind: "Coding agent", capabilities: []string{"agent-session"}},
	"ecrPushRoleArn":       {kind: "Access", capabilities: []string{"aws-topology"}},
	"ecrRegistry":          {kind: "Build & deploy", capabilities: []string{"aws-topology"}},
	"execKind":             {kind: "Host & network", capabilities: []string{"app-host"}},
	"execTarget":           {kind: "Host & network", capabilities: []string{"app-host"}},
	"host":                 {kind: "Data", capabilities: []string{"database"}},
	"host.type":            {kind: "Host & network", aliases: []string{"instanceType"}, capabilities: []string{"app-host", "aws-topology"}},
	"hostId":               {kind: "Host & network", capabilities: []string{"app-host"}},
	"hostProvider":         {kind: "Host & network", capabilities: []string{"app-host"}},
	"imageRepo":            {kind: "Build & deploy", capabilities: []string{"artifact"}},
	"instanceId":           {kind: "Host & network", capabilities: []string{"aws-topology"}},
	"landedBranch":         {kind: "Source"},
	"landedBuildRequestId": {kind: "Coding agent"},
	"landedSha":            {kind: "Source"},
	"landingPullRequest":   {kind: "Source"},
	"llmEndpoint":          {kind: "Model", capabilities: []string{"llm"}},
	"model":                {kind: "Model", capabilities: []string{"llm"}},
	"oidcProviderArn":      {kind: "Access", secret: true, capabilities: []string{"aws-iam"}},
	"opencodeUrl":          {kind: "Coding agent"},
	"port":                 {kind: "Data", capabilities: []string{"database"}},
	"publicIp":             {kind: "Host & network", capabilities: []string{"app-host", "aws-topology"}},
	"reachable":            {kind: "Host & network", capabilities: []string{"app-host", "aws-topology"}},
	"reconciledResources":  {kind: "Host & network", capabilities: []string{"reconcile/crossplane"}},
	"registryHost":         {kind: "Build & deploy", capabilities: []string{"app-host"}},
	"registryKind":         {kind: "Build & deploy", capabilities: []string{"app-host"}},
	"repo":                 {kind: "Source", capabilities: []string{"repo"}},
	"repoUrl":              {kind: "Source", capabilities: []string{"repo"}},
	"runnerLabel":          {kind: "Build & deploy", capabilities: []string{"runner"}},
	"runsOn":               {kind: "Build & deploy", capabilities: []string{"runner"}},
	"runsOnPr":             {kind: "Build & deploy", capabilities: []string{"runner"}},
	"scaffoldCommit":       {kind: "Source"},
	"scaffoldPullRequest":  {kind: "Source"},
	"settingsWarning":      {kind: "Source"},
	"sourceBranch":         {kind: "Source", capabilities: []string{"generated-source"}},
	"sourceSha":            {kind: "Source", capabilities: []string{"generated-source"}},
	"standardPullRequest":  {kind: "Coding agent", capabilities: []string{"agent-session"}},
	"url":                  {kind: "Work"},
	"username":             {kind: "Data", capabilities: []string{"database"}},
	"workflowPath":         {kind: "Coding agent", capabilities: []string{"agent-session"}},
	"workflowUrl":          {kind: "Build & deploy", capabilities: []string{"ci"}},
}

// nineKinds is the closed kind vocabulary, as the labels the console shows.
var nineKinds = map[string]bool{
	"App": true, "Host & network": true, "Data": true, "Build & deploy": true, "Access": true,
	"Model": true, "Coding agent": true, "Work": true, "Source": true,
}

// orEmpty treats a nil list as the empty list, so the table can omit one.
func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// registry returns contract.Resources(), failing the test when it is empty so
// a check over every entry cannot pass by having none to check.
func registry(t *testing.T) []contract.ResourceEntry {
	t.Helper()
	rs := contract.Resources()
	if len(rs) == 0 {
		t.Fatal("contract.Resources() is empty")
	}
	return rs
}

func TestResources_HoldsTheSixtyPlatformNames(t *testing.T) {
	rs := contract.Resources()
	if len(rs) != 60 {
		t.Errorf("contract.Resources() holds %d entries, want 60", len(rs))
	}
	got := map[string]bool{}
	for _, r := range rs {
		got[r.Name] = true
	}
	for name := range platformRegistry {
		if !got[name] {
			t.Errorf("the registry has no entry named %q", name)
		}
	}
	for name := range got {
		if _, ok := platformRegistry[name]; !ok {
			t.Errorf("the registry holds %q, which the platform's registry does not", name)
		}
	}
}

func TestResources_EachEntryCarriesThePlatformsKindAliasesSecretAndCapabilities(t *testing.T) {
	for _, r := range registry(t) {
		want, ok := platformRegistry[r.Name]
		if !ok {
			continue // reported by TestResources_HoldsTheSixtyPlatformNames
		}
		if r.Kind != want.kind {
			t.Errorf("%q: kind %q, want %q", r.Name, r.Kind, want.kind)
		}
		if r.Secret != want.secret {
			t.Errorf("%q: secret %v, want %v", r.Name, r.Secret, want.secret)
		}
		if !reflect.DeepEqual(r.Aliases, orEmpty(want.aliases)) {
			t.Errorf("%q: aliases %#v, want %#v", r.Name, r.Aliases, orEmpty(want.aliases))
		}
		if !reflect.DeepEqual(r.Capabilities, orEmpty(want.capabilities)) {
			t.Errorf("%q: capabilities %#v, want %#v", r.Name, r.Capabilities, orEmpty(want.capabilities))
		}
	}
}

func TestResources_EveryEntryIsComplete(t *testing.T) {
	for _, r := range registry(t) {
		if !nineKinds[r.Kind] {
			t.Errorf("%q: kind %q is not one of the nine labels", r.Name, r.Kind)
		}
		if r.Meaning == "" {
			t.Errorf("%q has no meaning", r.Name)
		}
		if r.Shape == "" {
			t.Errorf("%q has no shape", r.Name)
		}
		if !r.Secret && r.Example == "" {
			t.Errorf("%q is not secret and has no example", r.Name)
		}
	}
}

func TestResources_AliasesAndCapabilitiesAreNeverNil(t *testing.T) {
	for _, r := range registry(t) {
		if r.Aliases == nil {
			t.Errorf("%q: Aliases is nil, want an empty list when the entry has none", r.Name)
		}
		if r.Capabilities == nil {
			t.Errorf("%q: Capabilities is nil, want an empty list when the entry has none", r.Name)
		}
	}
}

func TestResources_AreSortedByNameWithEachNameOnce(t *testing.T) {
	rs := registry(t)
	for i := 1; i < len(rs); i++ {
		if rs[i-1].Name >= rs[i].Name {
			t.Errorf("entry %d (%q) must sort strictly before entry %d (%q)", i-1, rs[i-1].Name, i, rs[i].Name)
		}
	}
}

func TestResources_NoAliasEqualsAnEntryName(t *testing.T) {
	names := map[string]bool{}
	for _, r := range registry(t) {
		names[r.Name] = true
	}
	for _, r := range registry(t) {
		for _, a := range r.Aliases {
			if names[a] {
				t.Errorf("%q: alias %q is also an entry's name", r.Name, a)
			}
		}
	}
}

func TestResources_TheOnlyAliasesAreRegionAndInstanceType(t *testing.T) {
	var all []string
	for _, r := range registry(t) {
		all = append(all, r.Aliases...)
	}
	sort.Strings(all)
	if want := []string{"instanceType", "region"}; !reflect.DeepEqual(all, want) {
		t.Errorf("aliases across the registry = %v, want %v", all, want)
	}
}

func TestResources_SecretEntriesAreDeployRoleArnAndOidcProviderArnWithNoExample(t *testing.T) {
	var secret []string
	for _, r := range registry(t) {
		if !r.Secret {
			continue
		}
		secret = append(secret, r.Name)
		if r.Example != "" {
			t.Errorf("secret entry %q carries example %q, want none", r.Name, r.Example)
		}
	}
	if want := []string{"deployRoleArn", "oidcProviderArn"}; !reflect.DeepEqual(secret, want) {
		t.Errorf("secret entries = %v, want %v", secret, want)
	}
}

func TestLookupResource_RegionResolvesToTheCloudRegionEntry(t *testing.T) {
	r, ok := contract.LookupResource("region")
	if !ok {
		t.Fatal(`LookupResource("region") found nothing`)
	}
	if r.Name != "cloud.region" {
		t.Errorf(`LookupResource("region").Name = %q, want "cloud.region"`, r.Name)
	}
}

func TestLookupResource_InstanceTypeResolvesToTheHostTypeEntry(t *testing.T) {
	r, ok := contract.LookupResource("instanceType")
	if !ok {
		t.Fatal(`LookupResource("instanceType") found nothing`)
	}
	if r.Name != "host.type" {
		t.Errorf(`LookupResource("instanceType").Name = %q, want "host.type"`, r.Name)
	}
}

func TestLookupResource_FindsEveryEntryByItsNameAndEachAlias(t *testing.T) {
	rs := contract.Resources()
	if len(rs) == 0 {
		t.Fatal("contract.Resources() is empty")
	}
	for _, r := range rs {
		byName, ok := contract.LookupResource(r.Name)
		if !ok || !reflect.DeepEqual(byName, r) {
			t.Errorf("LookupResource(%q) = %+v, %v; want the entry %+v", r.Name, byName, ok, r)
		}
		for _, a := range r.Aliases {
			byAlias, ok := contract.LookupResource(a)
			if !ok || !reflect.DeepEqual(byAlias, r) {
				t.Errorf("LookupResource(%q) = %+v, %v; want the entry %+v", a, byAlias, ok, r)
			}
		}
	}
}

func TestLookupResource_UnknownOrMiscasedNameIsNotFound(t *testing.T) {
	for _, name := range []string{"", "noSuchOutput", "Region", "cloud.Region", "Host"} {
		if r, ok := contract.LookupResource(name); ok {
			t.Errorf("LookupResource(%q) found %q, want not found (the match is case-sensitive)", name, r.Name)
		}
	}
}

func TestResources_ReturnsCopiesACallerCanMutate(t *testing.T) {
	before := contract.Resources()
	snapshot := make([]contract.ResourceEntry, len(before))
	for i, r := range before {
		r.Aliases = append([]string{}, r.Aliases...)
		r.Capabilities = append([]string{}, r.Capabilities...)
		snapshot[i] = r
	}

	got := contract.Resources()
	if len(got) == 0 {
		t.Fatal("contract.Resources() is empty")
	}
	for i := range got {
		for j := range got[i].Aliases {
			got[i].Aliases[j] = "mutated"
		}
		for j := range got[i].Capabilities {
			got[i].Capabilities[j] = "mutated"
		}
		got[i].Name = "mutated"
	}

	if after := contract.Resources(); !reflect.DeepEqual(after, snapshot) {
		t.Error("mutating what Resources returned changed a later call's result")
	}
}

func TestLookupResource_ReturnsACopyACallerCanMutate(t *testing.T) {
	first, ok := contract.LookupResource("cloud.region")
	if !ok {
		t.Fatal(`LookupResource("cloud.region") found nothing`)
	}
	first.Aliases[0] = "mutated"
	first.Capabilities[0] = "mutated"

	again, _ := contract.LookupResource("region")
	if again.Name != "cloud.region" || again.Aliases[0] != "region" || again.Capabilities[0] != "app-host" {
		t.Errorf("mutating a looked-up entry changed the registry: a later lookup returned %+v", again)
	}
}

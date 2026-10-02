package contract

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// The resource-type catalogue is the one list of resource types a block
// creates or references. Each type is described by its facets, and its
// Outputs name entries of the resource registry, so a type's outputs and a
// block's declared outputs come from the same vocabulary.

// ResourceType is one entry of the resource-type catalogue.
type ResourceType struct {
	// ID is "<capability>.<name>", the name in lowercase kebab case, for
	// example "aws.ecr-repository".
	ID string
	// Kind is the provider-neutral kind of the resource, in lowercase kebab
	// case, for example "container-image".
	Kind string
	// Capability is the capability slug the type belongs to. It equals the
	// part of ID before the first dot.
	Capability string
	// Category is exactly one of ResourceTypeCategories().
	Category string
	// Languages lists the ResourceTypeLanguages() the type serves. It is
	// never nil, empty when the type serves no particular language, and
	// holds no value twice.
	Languages []string
	// ArtifactType is "" or one of ArtifactTypes(): the artifact the type
	// holds or runs.
	ArtifactType string
	// Lifecycle is one of ResourceTypeLifecycles(): whether replacing the
	// resource loses data.
	Lifecycle string
	// Scope is one of ResourceTypeScopes(): whether one resource serves a
	// single app or the whole org.
	Scope string
	// Priced marks a type that costs money while it exists.
	Priced bool
	// PricingKey names the price list a priced type is costed against. It
	// is set exactly when Priced is true.
	PricingKey string
	// Outputs are the canonical resource registry names the type publishes.
	// It is never nil, empty when the type publishes none, and holds no name
	// twice.
	Outputs []string
}

// clone returns a copy of t whose Languages and Outputs share no backing
// array with t's. An empty list stays empty rather than becoming nil.
func (t ResourceType) clone() ResourceType {
	t.Languages = append([]string{}, t.Languages...)
	t.Outputs = append([]string{}, t.Outputs...)
	return t
}

// ResourceTypes returns every catalogue entry, sorted by ID. Each call
// returns fresh copies, so a caller may change the result without changing
// the catalogue.
func ResourceTypes() []ResourceType {
	out := make([]ResourceType, len(resourceTypeCatalogue))
	for i, t := range resourceTypeCatalogue {
		out[i] = t.clone()
	}
	slices.SortFunc(out, func(a, b ResourceType) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// LookupResourceType returns a copy of the catalogue entry whose ID is id.
// The match is case-sensitive.
func LookupResourceType(id string) (ResourceType, bool) {
	for _, t := range resourceTypeCatalogue {
		if t.ID == id {
			return t.clone(), true
		}
	}
	return ResourceType{}, false
}

// ResourceTypeCategories returns the categories a resource type may belong
// to, as a fresh slice.
func ResourceTypeCategories() []string {
	return []string{
		"compute", "network", "storage", "database", "artifact-registry", "build-and-delivery",
		"identity-and-access", "secrets", "observability", "messaging", "source-and-work", "ai-model",
	}
}

// ResourceTypeLanguages returns the languages a resource type may serve, as a
// fresh slice.
func ResourceTypeLanguages() []string {
	return []string{"go", "node", "python", "java", "rust", "static-site", "any-container"}
}

// ArtifactTypes returns the artifact types a resource type may hold or run,
// as a fresh slice.
func ArtifactTypes() []string {
	return []string{"container-image", "helm-chart", "language-package", "binary", "static-bundle", "machine-image"}
}

// ResourceTypeLifecycles returns the lifecycles a resource type may have, as
// a fresh slice.
func ResourceTypeLifecycles() []string {
	return []string{"stateful", "replaceable"}
}

// ResourceTypeScopes returns the scopes a resource type may have, as a fresh
// slice.
func ResourceTypeScopes() []string {
	return []string{"per-app", "org-wide"}
}

// ValidateResourceTypes returns the first defect it finds in ts, or nil. The
// error names the defective entry by its ID, or by its index when the ID is
// empty.
func ValidateResourceTypes(ts []ResourceType) error {
	seen := make(map[string]bool, len(ts))
	for i, t := range ts {
		if t.ID == "" {
			return fmt.Errorf("resource type at index %d: empty ID", i)
		}
		if err := validateResourceType(t); err != nil {
			return fmt.Errorf("resource type %q: %w", t.ID, err)
		}
		if seen[t.ID] {
			return fmt.Errorf("resource type %q: listed more than once", t.ID)
		}
		seen[t.ID] = true
	}
	return nil
}

// validateResourceType checks one entry whose ID is non-empty against every
// rule but uniqueness.
func validateResourceType(t ResourceType) error {
	if t.Capability == "" {
		return fmt.Errorf("empty capability")
	}
	prefix, name, ok := strings.Cut(t.ID, ".")
	if !ok || !isKebab(name) {
		return fmt.Errorf("ID must be <capability>.<name> with the name in lowercase kebab case")
	}
	if prefix != t.Capability {
		return fmt.Errorf("ID prefix %q differs from capability %q", prefix, t.Capability)
	}
	if !isKebab(t.Kind) {
		return fmt.Errorf("kind %q is not lowercase kebab case", t.Kind)
	}
	if !slices.Contains(ResourceTypeCategories(), t.Category) {
		return fmt.Errorf("category %q is not one of %q", t.Category, ResourceTypeCategories())
	}
	if !slices.Contains(ResourceTypeLifecycles(), t.Lifecycle) {
		return fmt.Errorf("lifecycle %q is not one of %q", t.Lifecycle, ResourceTypeLifecycles())
	}
	if !slices.Contains(ResourceTypeScopes(), t.Scope) {
		return fmt.Errorf("scope %q is not one of %q", t.Scope, ResourceTypeScopes())
	}
	if t.Languages == nil {
		return fmt.Errorf("languages is nil, want an empty list when the type serves none")
	}
	if err := checkDistinct("language", t.Languages, func(l string) bool {
		return slices.Contains(ResourceTypeLanguages(), l)
	}); err != nil {
		return err
	}
	if t.ArtifactType != "" && !slices.Contains(ArtifactTypes(), t.ArtifactType) {
		return fmt.Errorf("artifact type %q is not one of %q", t.ArtifactType, ArtifactTypes())
	}
	if t.Priced != (t.PricingKey != "") {
		return fmt.Errorf("priced is %v but pricing key is %q; a pricing key is set exactly when priced", t.Priced, t.PricingKey)
	}
	if t.Outputs == nil {
		return fmt.Errorf("outputs is nil, want an empty list when the type publishes none")
	}
	return checkDistinct("output", t.Outputs, isCanonicalResourceName)
}

// checkDistinct refuses a value of vs that known rejects, or one listed twice.
func checkDistinct(what string, vs []string, known func(string) bool) error {
	seen := make(map[string]bool, len(vs))
	for _, v := range vs {
		if !known(v) {
			return fmt.Errorf("unknown %s %q", what, v)
		}
		if seen[v] {
			return fmt.Errorf("%s %q listed more than once", what, v)
		}
		seen[v] = true
	}
	return nil
}

// isCanonicalResourceName reports whether name is the Name of a resource
// registry entry. An alias does not count.
func isCanonicalResourceName(name string) bool {
	r, ok := LookupResource(name)
	return ok && r.Name == name
}

// isKebab reports whether s is lowercase letters and digits in groups joined
// by single hyphens.
func isKebab(s string) bool {
	if s == "" {
		return false
	}
	for _, group := range strings.Split(s, "-") {
		if group == "" {
			return false
		}
		for _, c := range group {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				return false
			}
		}
	}
	return true
}

// Ownership says whether a resource is created by the block that declares it
// or referenced from outside, where something else created it and owns its
// lifetime.
type Ownership string

const (
	// OwnershipCreated marks a resource the declaring block creates and
	// deletes.
	OwnershipCreated Ownership = "created"
	// OwnershipReferenced marks a resource the block uses but never creates
	// or deletes.
	OwnershipReferenced Ownership = "referenced"
)

// Ownerships returns every valid Ownership, as a fresh slice.
func Ownerships() []Ownership {
	return []Ownership{OwnershipCreated, OwnershipReferenced}
}

// Valid reports whether o is exactly one of Ownerships().
func (o Ownership) Valid() bool {
	return slices.Contains(Ownerships(), o)
}

// Step is one resource a block's plan works on: its catalogue type, whether
// the block creates or references it, and the binding each of its named
// inputs reads from. A binding is a param, an earlier step's output or a
// required resource's output.
type Step struct {
	// ID names the step within the block.
	ID string `json:"id"`
	// Type is the ID of a resource-type catalogue entry.
	Type string `json:"type"`
	// Ownership says whether the block creates the resource or references it.
	Ownership Ownership `json:"ownership"`
	// Inputs maps an input name to the binding reference it reads, in exactly
	// one of three forms: "params.<name>" for a param the manifest declares,
	// "steps.<id>.outputs.<name>" for an output an earlier step's type
	// produces, or "requires.<id>.outputs.<name>" for an output a required
	// resource produces, where the name must be in that requirement's reads.
	Inputs map[string]string `json:"inputs,omitempty"`
}

// SuppliedResource is an existing resource that meets one of a block's
// requires entries. The block references it and never owns it.
type SuppliedResource struct {
	// Requirement names the requires entry the resource meets.
	Requirement string `json:"requirement"`
	// Type is the ID of a resource-type catalogue entry.
	Type string `json:"type"`
	// Ownership is always OwnershipReferenced for a supplied resource.
	Ownership Ownership `json:"ownership"`
}

// emptyList is the empty list a catalogue entry carries for no languages or no
// outputs. clone copies it, so entries never share it with a caller.
var emptyList = []string{}

var resourceTypeCatalogue = []ResourceType{
	{ID: "app-host.container", Kind: "container", Capability: "app-host", Category: "compute", Languages: []string{"any-container"}, Lifecycle: "replaceable", Scope: "per-app", Outputs: []string{"url"}},
	{ID: "app-host.deployment", Kind: "deployment", Capability: "app-host", Category: "build-and-delivery", Languages: []string{"any-container"}, Lifecycle: "replaceable", Scope: "per-app", Outputs: []string{"deployment"}},
	{ID: "app-host.host-configuration", Kind: "host-configuration", Capability: "app-host", Category: "compute", Languages: emptyList, Lifecycle: "replaceable", Scope: "per-app", Outputs: emptyList},
	{ID: "app-host.postgres-database", Kind: "postgres-database", Capability: "app-host", Category: "database", Languages: emptyList, Lifecycle: "stateful", Scope: "per-app", Outputs: []string{"dbName", "host", "port", "username"}},
	{ID: "aws.ebs-volume", Kind: "block-volume", Capability: "aws", Category: "storage", Languages: emptyList, Lifecycle: "stateful", Scope: "per-app", Outputs: []string{"dataSnapshotId"}},
	{ID: "aws.ec2-instance", Kind: "host", Capability: "aws", Category: "compute", Languages: []string{"any-container"}, Lifecycle: "replaceable", Scope: "per-app", Priced: true, PricingKey: "AmazonEC2", Outputs: []string{"execTarget", "host.type", "hostId", "instanceId", "publicIp"}},
	{ID: "aws.ecr-image", Kind: "container-image", Capability: "aws", Category: "artifact-registry", Languages: []string{"any-container"}, ArtifactType: "container-image", Lifecycle: "replaceable", Scope: "per-app", Outputs: []string{"artifact", "imageRepo"}},
	{ID: "aws.ecr-repository", Kind: "container-repository", Capability: "aws", Category: "artifact-registry", Languages: []string{"any-container"}, ArtifactType: "container-image", Lifecycle: "replaceable", Scope: "per-app", Outputs: []string{"ecrRegistry", "registryHost"}},
	{ID: "aws.egress-proxy", Kind: "egress-proxy", Capability: "aws", Category: "network", Languages: emptyList, Lifecycle: "replaceable", Scope: "per-app", Priced: true, PricingKey: "AmazonEC2", Outputs: emptyList},
	{ID: "aws.iam-oidc-provider", Kind: "oidc-provider", Capability: "aws", Category: "identity-and-access", Languages: emptyList, Lifecycle: "replaceable", Scope: "org-wide", Outputs: []string{"oidcProviderArn"}},
	{ID: "aws.iam-role", Kind: "iam-role", Capability: "aws", Category: "identity-and-access", Languages: emptyList, Lifecycle: "replaceable", Scope: "per-app", Outputs: []string{"deployRoleArn", "ecrPushRoleArn"}},
	{ID: "aws.route53-record", Kind: "dns-record", Capability: "aws", Category: "network", Languages: emptyList, Lifecycle: "replaceable", Scope: "per-app", Outputs: []string{"appHostname"}},
	{ID: "aws.security-group", Kind: "firewall", Capability: "aws", Category: "network", Languages: emptyList, Lifecycle: "replaceable", Scope: "per-app", Outputs: emptyList},
	{ID: "aws.ssm-association", Kind: "host-configuration", Capability: "aws", Category: "compute", Languages: emptyList, Lifecycle: "replaceable", Scope: "per-app", Outputs: emptyList},
	{ID: "aws.ssm-parameter", Kind: "secret", Capability: "aws", Category: "secrets", Languages: emptyList, Lifecycle: "stateful", Scope: "per-app", Outputs: emptyList},
	{ID: "aws.vpc", Kind: "network", Capability: "aws", Category: "network", Languages: emptyList, Lifecycle: "replaceable", Scope: "per-app", Outputs: emptyList},
	{ID: "github.actions-secret", Kind: "secret", Capability: "github", Category: "secrets", Languages: emptyList, Lifecycle: "replaceable", Scope: "per-app", Outputs: emptyList},
	{ID: "github.actions-variable", Kind: "ci-variable", Capability: "github", Category: "build-and-delivery", Languages: emptyList, Lifecycle: "replaceable", Scope: "per-app", Outputs: emptyList},
	{ID: "github.commit", Kind: "source-revision", Capability: "github", Category: "source-and-work", Languages: emptyList, Lifecycle: "replaceable", Scope: "per-app", Outputs: []string{"scaffoldCommit", "sourceBranch", "sourceSha"}},
	{ID: "github.pull-request", Kind: "change-request", Capability: "github", Category: "source-and-work", Languages: emptyList, Lifecycle: "replaceable", Scope: "per-app", Outputs: []string{"scaffoldPullRequest"}},
	{ID: "github.repository", Kind: "git-repository", Capability: "github", Category: "source-and-work", Languages: emptyList, Lifecycle: "stateful", Scope: "per-app", Outputs: []string{"boardRepo", "cdRepo", "ciRepo", "repo", "repoUrl"}},
	{ID: "github.repository-webhook", Kind: "webhook", Capability: "github", Category: "build-and-delivery", Languages: emptyList, Lifecycle: "replaceable", Scope: "per-app", Outputs: emptyList},
	{ID: "github.runner-group", Kind: "runner-group", Capability: "github", Category: "build-and-delivery", Languages: emptyList, Lifecycle: "replaceable", Scope: "per-app", Outputs: emptyList},
	{ID: "github.self-hosted-runner", Kind: "ci-runner", Capability: "github", Category: "build-and-delivery", Languages: emptyList, Lifecycle: "replaceable", Scope: "per-app", Outputs: []string{"awsCredential", "runnerLabel", "runsOn", "runsOnPr"}},
	{ID: "github.workflow-file", Kind: "ci-workflow", Capability: "github", Category: "build-and-delivery", Languages: emptyList, Lifecycle: "replaceable", Scope: "per-app", Outputs: []string{"ciCheckContribution", "workflowUrl"}},
	{ID: "hetzner.firewall", Kind: "firewall", Capability: "hetzner", Category: "network", Languages: emptyList, Lifecycle: "replaceable", Scope: "per-app", Outputs: emptyList},
	{ID: "hetzner.primary-ip", Kind: "ip-address", Capability: "hetzner", Category: "network", Languages: emptyList, Lifecycle: "replaceable", Scope: "per-app", Outputs: []string{"publicIp"}},
	{ID: "hetzner.server", Kind: "host", Capability: "hetzner", Category: "compute", Languages: []string{"any-container"}, Lifecycle: "replaceable", Scope: "per-app", Priced: true, PricingKey: "HetznerCloud", Outputs: []string{"cloud.region", "execTarget", "host.type", "hostId"}},
	{ID: "hetzner.ssh-key", Kind: "ssh-key", Capability: "hetzner", Category: "identity-and-access", Languages: emptyList, Lifecycle: "replaceable", Scope: "per-app", Outputs: emptyList},
	{ID: "hetzner.volume", Kind: "block-volume", Capability: "hetzner", Category: "storage", Languages: emptyList, Lifecycle: "stateful", Scope: "per-app", Outputs: []string{"dataSnapshotId"}},
	{ID: "tracker.api-token", Kind: "api-token", Capability: "tracker", Category: "identity-and-access", Languages: emptyList, Lifecycle: "replaceable", Scope: "per-app", Outputs: emptyList},
}

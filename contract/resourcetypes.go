package contract

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// The resource-type catalogue is the one list of resource types a block
// creates or references. Each type implements one kind, lives in one place
// its id prefix names, and declares its inputs, its lifecycle, how it is
// observed and proven absent, a time budget per resource verb and the
// failures it can meet. Its Outputs name entries of the resource registry, so
// a type's outputs and a block's declared outputs come from one vocabulary.

// Input is one input a resource type takes.
type Input struct {
	// Name is the input's name, unique within the type.
	Name string `json:"name"`
	// Required marks an input the type cannot be created without.
	Required bool `json:"required"`
	// Secret marks an input whose value is sensitive.
	Secret bool `json:"secret"`
	// Accepts says what the input takes, in exactly one of three forms:
	// "value" for a literal value, "resource:<kind>" for a whole resource of
	// a released kind, or "output:<kind>/<output>" for one output that kind
	// names.
	Accepts string `json:"accepts"`
	// Place says where the resource the input takes must live, in exactly
	// one of three forms: "own" for the type's own capability,
	// "capability:<slug>" for a named capability of Capabilities(), or
	// "carrier" for the capability of the resource the type lives on. It is
	// empty exactly when the input accepts "value".
	Place string `json:"place"`
	// Replacement is one of InputReplacements(): whether changing the input
	// updates the resource in place or replaces it.
	Replacement string `json:"replacement"`
}

// Failure is one failure a resource type can meet.
type Failure struct {
	// Class is one of FailureClasses(): whether repeating the verb can
	// succeed.
	Class string `json:"class"`
	// Cause says in one sentence what went wrong.
	Cause string `json:"cause"`
}

// ProviderResource names the OpenTofu provider resource a type maps onto one
// to one. The type's inputs are generated from that resource's schema by
// cmd/catalogue-inputs.
type ProviderResource struct {
	// Provider is the provider's registry address, for example
	// "registry.opentofu.org/hashicorp/aws".
	Provider string `json:"provider"`
	// Resource is the provider's resource name, for example "aws_vpc".
	Resource string `json:"resource"`
	// ReplaceOn names the inputs whose change replaces the resource, as the
	// provider's own schema forces a new resource. Every other input updates
	// it in place. A provider schema carries no replacement rule, so this
	// list is written by hand.
	ReplaceOn []string `json:"replaceOn"`
}

// ResourceType is one entry of the resource-type catalogue.
type ResourceType struct {
	// ID is "<place>.<name>", the name in lowercase kebab case, for example
	// "aws.ecr-repository". The place is a capability or a released kind.
	ID string
	// Kind is the released kind the type implements, for example
	// "container-image".
	Kind string
	// ReliesOn names the capabilities the type uses that no requirement
	// shows. It is never nil, empty when it relies on none, and holds no
	// capability twice.
	ReliesOn []string
	// Hosts is the capability whose API a resource of this type runs, or ""
	// when it runs none. The type's kind publishes how to reach that API.
	Hosts string
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
	// Handover is one of Handovers() for a stateful type, the way its data
	// survives a replacement, and "" for a replaceable one.
	Handover string
	// Upgrade is one of UpgradeBehaviours(): how upgrading the resource
	// affects what it serves.
	Upgrade string
	// Scope is one of ResourceTypeScopes(): whether one resource serves a
	// single app or the whole org.
	Scope string
	// Priced marks a type that costs money while it exists.
	Priced bool
	// PricingKey names the price list a priced type is costed against. It
	// is set exactly when Priced is true.
	PricingKey string
	// Inputs are the inputs the type takes, sorted by name. It is never nil,
	// and empty when the type takes none.
	Inputs []Input
	// ProviderResource is the OpenTofu provider resource the type maps onto
	// one to one, or nil when its inputs are written by hand.
	ProviderResource *ProviderResource
	// Outputs are the canonical resource registry names the type publishes:
	// every required output of its kind and any of its optional ones. It is
	// never nil, empty when the type publishes none, and holds no name twice.
	Outputs []string
	// Observe says how a resource of the type is observed to exist and be
	// usable, the proof that ensure is done.
	Observe string
	// Absence says how a resource of the type is proven gone, the proof that
	// delete is done.
	Absence string
	// TimeBudgets holds a positive time budget for each of ResourceVerbs().
	TimeBudgets map[string]time.Duration
	// Failures are the failures the type can meet. It is never nil and never
	// empty.
	Failures []Failure
}

// Place returns the capability the type lives in, or the kind it lives on:
// the part of its ID before the first dot.
func (t ResourceType) Place() string {
	place, _, _ := strings.Cut(t.ID, ".")
	return place
}

// clone returns a copy of t that shares no list, map or pointer with t. An
// empty list stays empty rather than becoming nil.
func (t ResourceType) clone() ResourceType {
	t.ReliesOn = append([]string{}, t.ReliesOn...)
	t.Languages = append([]string{}, t.Languages...)
	t.Inputs = append([]Input{}, t.Inputs...)
	t.Outputs = append([]string{}, t.Outputs...)
	t.Failures = append([]Failure{}, t.Failures...)
	budgets := make(map[string]time.Duration, len(t.TimeBudgets))
	maps.Copy(budgets, t.TimeBudgets)
	t.TimeBudgets = budgets
	if t.ProviderResource != nil {
		pr := *t.ProviderResource
		pr.ReplaceOn = append([]string{}, pr.ReplaceOn...)
		t.ProviderResource = &pr
	}
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

// Capabilities returns the capabilities a resource type may live in, rely on
// or host, as a fresh slice. Each is an API that exists apart from any one
// app.
func Capabilities() []string {
	return []string{"anthropic", "aws", "crossplane", "github", "hetzner", "kubernetes", "openai", "plain", "ubicloud"}
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

// Handovers returns the ways a stateful type's data may survive a
// replacement, as a fresh slice: from a snapshot the replacement is created
// from, or by copying the data into the replacement.
func Handovers() []string {
	return []string{"snapshot", "copy"}
}

// UpgradeBehaviours returns how upgrading a resource may affect what it
// serves, the values platform ADR 0048 sets, as a fresh slice.
func UpgradeBehaviours() []string {
	return []string{"in-place", "blue-green", "recreate"}
}

// ResourceTypeScopes returns the scopes a resource type may have, as a fresh
// slice.
func ResourceTypeScopes() []string {
	return []string{"per-app", "org-wide"}
}

// ResourceVerbs returns the verbs the platform drives a resource through, as
// a fresh slice. Each type declares a time budget for every one.
func ResourceVerbs() []string {
	return []string{"ensure", "observe", "delete"}
}

// FailureClasses returns the classes a failure may carry, as a fresh slice:
// permanent when repeating the verb cannot succeed, transient when it can.
func FailureClasses() []string {
	return []string{"permanent", "transient"}
}

// InputReplacements returns what changing an input may do to a resource, as a
// fresh slice: update it in place, or replace it.
func InputReplacements() []string {
	return []string{"update", "replace"}
}

// ValidateResourceTypes returns the first defect it finds in ts, or nil. It
// checks every entry's fields, refuses an ID outside the community grammar of
// platform ADR 0080, refuses an ID prefix that is neither a capability nor a
// released kind, refuses an ID listed more than once, and refuses two types
// whose names, the part of the ID after the first dot, are equal but whose
// kinds differ. The error names the defective entry by its ID, or by its index
// when the ID is empty, and a kind mismatch also names the type and kind it
// clashes with.
//
// The catalogue holds community types only, so an ID is a bare
// <place>.<name> with no @<org>/ namespace, and its place is a slug
// ParseCapabilityID accepts.
//
// The shared-name rule is the catalogue's naming rule. A name after the dot
// names one real thing in every place, so every type sharing a name shares
// its kind. A reviewer gives a different real thing a different name rather
// than a different kind.
func ValidateResourceTypes(ts []ResourceType) error {
	seen := make(map[string]bool, len(ts))
	byName := make(map[string]ResourceType, len(ts))
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
		_, name, _ := strings.Cut(t.ID, ".")
		first, ok := byName[name]
		if !ok {
			byName[name] = t
			continue
		}
		if first.Kind != t.Kind {
			return fmt.Errorf("resource type %q: kind %q differs from kind %q of %q, which shares its name", t.ID, t.Kind, first.Kind, first.ID)
		}
	}
	return nil
}

// validateResourceType checks one entry whose ID is non-empty against every
// rule but uniqueness and the shared-name rule.
func validateResourceType(t ResourceType) error {
	kind, err := validatePlaceAndKind(t)
	if err != nil {
		return err
	}
	if err := validateFacets(t); err != nil {
		return err
	}
	if err := validateOutputs(t, kind); err != nil {
		return err
	}
	if err := validateInputs(t); err != nil {
		return err
	}
	return validateDeclarations(t)
}

// validatePlaceAndKind checks the ID's grammar, the place its prefix names,
// the kind, and the capabilities the type relies on and hosts. It returns
// the released kind the type implements.
func validatePlaceAndKind(t ResourceType) (Kind, error) {
	place, name, ok := strings.Cut(t.ID, ".")
	if !ok || !isKebab(name) {
		return Kind{}, fmt.Errorf("ID must be <place>.<name> with the name in lowercase kebab case")
	}
	if !isSlug(place) {
		return Kind{}, fmt.Errorf("ID is not a community type <place>.<name>: the prefix %q must be 1 to 40 lowercase letters, digits and dashes that start and end with a letter or digit, with no @<org>/ namespace", place)
	}
	if _, isKind := LookupKind(place); !isKind && !slices.Contains(Capabilities(), place) {
		return Kind{}, fmt.Errorf("place %q is neither a capability nor a released kind", place)
	}
	if !isKebab(t.Kind) {
		return Kind{}, fmt.Errorf("kind %q is not lowercase kebab case", t.Kind)
	}
	kind, ok := LookupKind(t.Kind)
	if !ok {
		return Kind{}, fmt.Errorf("kind %q is not a released kind", t.Kind)
	}
	if t.ReliesOn == nil {
		return Kind{}, fmt.Errorf("reliesOn is nil, want an empty list when the type relies on no capability")
	}
	if err := checkDistinct("reliesOn capability", t.ReliesOn, isCapability); err != nil {
		return Kind{}, err
	}
	if t.Hosts != "" && !isCapability(t.Hosts) {
		return Kind{}, fmt.Errorf("hosts %q names no capability", t.Hosts)
	}
	return kind, nil
}

// validateFacets checks the catalogue facets, the lifecycle with its
// handover, the upgrade behaviour and the pricing.
func validateFacets(t ResourceType) error {
	if !slices.Contains(ResourceTypeCategories(), t.Category) {
		return fmt.Errorf("category %q is not one of %q", t.Category, ResourceTypeCategories())
	}
	if !slices.Contains(ResourceTypeLifecycles(), t.Lifecycle) {
		return fmt.Errorf("lifecycle %q is not one of %q", t.Lifecycle, ResourceTypeLifecycles())
	}
	switch {
	case t.Lifecycle == "stateful" && t.Handover == "":
		return fmt.Errorf("a stateful lifecycle declares no handover, the way its data survives a replacement")
	case t.Lifecycle != "stateful" && t.Handover != "":
		return fmt.Errorf("handover %q is declared on a %q lifecycle; only a stateful type declares one", t.Handover, t.Lifecycle)
	case t.Handover != "" && !slices.Contains(Handovers(), t.Handover):
		return fmt.Errorf("handover %q is not one of %q", t.Handover, Handovers())
	}
	if !slices.Contains(UpgradeBehaviours(), t.Upgrade) {
		return fmt.Errorf("upgrade %q is not one of %q", t.Upgrade, UpgradeBehaviours())
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
	return nil
}

// validateOutputs checks that the type's outputs are distinct canonical
// registry names, that they include every required output of kind, and that
// kind names each one.
func validateOutputs(t ResourceType, kind Kind) error {
	if t.Outputs == nil {
		return fmt.Errorf("outputs is nil, want an empty list when the type publishes none")
	}
	if err := checkDistinct("output", t.Outputs, isCanonicalResourceName); err != nil {
		return err
	}
	for _, o := range kind.Required {
		if !slices.Contains(t.Outputs, o.Name) {
			return fmt.Errorf("lacks required output %q of kind %q", o.Name, kind.ID)
		}
	}
	for _, name := range t.Outputs {
		if !kind.names(name) {
			return fmt.Errorf("produces output %q, which kind %q does not name", name, kind.ID)
		}
	}
	return nil
}

// validateInputs checks every input and the provider resource the inputs are
// generated from.
func validateInputs(t ResourceType) error {
	if t.Inputs == nil {
		return fmt.Errorf("inputs is nil, want an empty list when the type takes none")
	}
	names := make(map[string]bool, len(t.Inputs))
	for i, in := range t.Inputs {
		if in.Name == "" {
			return fmt.Errorf("input at index %d has no name", i)
		}
		if names[in.Name] {
			return fmt.Errorf("input name %q listed more than once", in.Name)
		}
		names[in.Name] = true
		if !slices.Contains(InputReplacements(), in.Replacement) {
			return fmt.Errorf("input %q: replacement %q is not one of %q", in.Name, in.Replacement, InputReplacements())
		}
		if err := validateAccepts(in.Accepts); err != nil {
			return fmt.Errorf("input %q: %w", in.Name, err)
		}
		if err := validateInputPlace(in, isCapability(t.Place())); err != nil {
			return fmt.Errorf("input %q: %w", in.Name, err)
		}
	}
	pr := t.ProviderResource
	if pr == nil {
		return nil
	}
	if pr.Provider == "" {
		return fmt.Errorf("provider resource names no provider")
	}
	if pr.Resource == "" {
		return fmt.Errorf("provider resource of %q names no resource", pr.Provider)
	}
	for _, name := range pr.ReplaceOn {
		if !names[name] {
			return fmt.Errorf("provider resource %s replaces on %q, which is no declared input", pr.Resource, name)
		}
	}
	return nil
}

// validateAccepts checks one input's accepts form: "value",
// "resource:<kind>" or "output:<kind>/<output>", where the kind is released
// and names the output.
func validateAccepts(accepts string) error {
	if accepts == "value" {
		return nil
	}
	if kindID, ok := strings.CutPrefix(accepts, "resource:"); ok {
		if _, found := LookupKind(kindID); !found {
			return fmt.Errorf("accepts %q names no released kind", accepts)
		}
		return nil
	}
	if ref, ok := strings.CutPrefix(accepts, "output:"); ok {
		kindID, output, _ := strings.Cut(ref, "/")
		kind, found := LookupKind(kindID)
		if !found {
			return fmt.Errorf("accepts %q names no released kind", accepts)
		}
		if !kind.names(output) {
			return fmt.Errorf("accepts %q names an output kind %q does not name", accepts, kindID)
		}
		return nil
	}
	return fmt.Errorf("accepts %q is none of value, resource:<kind> or output:<kind>/<output>", accepts)
}

// validateInputPlace checks where the resource an input takes must live. A
// value input carries no place. A resource-taking input carries "own" when
// its type lives in a capability, "carrier" when its type lives on a
// released kind, or "capability:<slug>" naming one of Capabilities() on
// either.
func validateInputPlace(in Input, livesInCapability bool) error {
	if in.Accepts == "value" {
		if in.Place != "" {
			return fmt.Errorf("place %q is declared on an input accepting a value; only a resource-taking input declares one", in.Place)
		}
		return nil
	}
	switch in.Place {
	case "":
		return fmt.Errorf("accepts %q but declares no place, where the taken resource must live", in.Accepts)
	case "own":
		if !livesInCapability {
			return fmt.Errorf("place %q needs a type living in a capability; a type living on a kind takes \"carrier\" or \"capability:<slug>\"", in.Place)
		}
		return nil
	case "carrier":
		if livesInCapability {
			return fmt.Errorf("place %q needs a type living on a kind; a type living in a capability takes \"own\" or \"capability:<slug>\"", in.Place)
		}
		return nil
	}
	slug, ok := strings.CutPrefix(in.Place, "capability:")
	if !ok || slug == "" {
		return fmt.Errorf("place %q is none of own, carrier or capability:<slug>", in.Place)
	}
	if !isCapability(slug) {
		return fmt.Errorf("place %q names no capability of %q", in.Place, Capabilities())
	}
	return nil
}

// validateDeclarations checks the observe and absence declarations, the time
// budgets and the failures.
func validateDeclarations(t ResourceType) error {
	if t.Observe == "" {
		return fmt.Errorf("declares no observe, how a resource is seen to exist and be usable")
	}
	if t.Absence == "" {
		return fmt.Errorf("declares no absence, how a resource is proven gone")
	}
	if len(t.TimeBudgets) == 0 {
		return fmt.Errorf("declares no time budget, want one for each of %q", ResourceVerbs())
	}
	for _, verb := range ResourceVerbs() {
		if t.TimeBudgets[verb] <= 0 {
			return fmt.Errorf("time budget for %q is missing or not positive", verb)
		}
	}
	for _, verb := range slices.Sorted(maps.Keys(t.TimeBudgets)) {
		if !slices.Contains(ResourceVerbs(), verb) {
			return fmt.Errorf("time budget for %q, which is not one of the resource verbs %q", verb, ResourceVerbs())
		}
	}
	if len(t.Failures) == 0 {
		return fmt.Errorf("declares no failure, want at least one")
	}
	for _, f := range t.Failures {
		if !slices.Contains(FailureClasses(), f.Class) {
			return fmt.Errorf("failure class %q is not one of %q", f.Class, FailureClasses())
		}
		if f.Cause == "" {
			return fmt.Errorf("failure of class %q has no cause", f.Class)
		}
	}
	return nil
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

// isCapability reports whether c is one of Capabilities().
func isCapability(c string) bool {
	return slices.Contains(Capabilities(), c)
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

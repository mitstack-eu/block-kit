package contract

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// Input is one input a resource type takes.
type Input struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Secret   bool   `json:"secret"`
	// Accepts is "value", "resource:<kind>" or "output:<kind>/<output>".
	Accepts string `json:"accepts"`
	// Place is "own", "capability:<slug>" or "carrier", and empty exactly when Accepts is "value".
	Place       string `json:"place"`
	Replacement string `json:"replacement"`
}

// Failure is one failure a resource type can meet.
type Failure struct {
	Class string `json:"class"`
	Cause string `json:"cause"`
}

// ProviderResource names the OpenTofu provider resource a type maps onto one to one.
type ProviderResource struct {
	Provider string `json:"provider"`
	Resource string `json:"resource"`
	// ReplaceOn is written by hand because a provider schema carries no replacement rule.
	ReplaceOn []string `json:"replaceOn"`
}

// ResourceType is one entry of the resource-type catalogue.
type ResourceType struct {
	// ID is "<place>.<name>", the name in lowercase kebab case.
	ID   string
	Kind string
	// ReliesOn is never nil and holds no capability twice.
	ReliesOn []string
	// Hosts is the capability whose API a resource of this type runs, or "".
	Hosts    string
	Category string
	// Languages is never nil and holds no value twice.
	Languages    []string
	ArtifactType string
	Lifecycle    string
	// Handover is set for a stateful type and "" for a replaceable one.
	Handover string
	Upgrade  string
	Scope    string
	Priced   bool
	// PricingKey is set exactly when Priced is true.
	PricingKey string
	// Inputs is never nil and sorted by name.
	Inputs           []Input
	ProviderResource *ProviderResource
	// Outputs is never nil and holds no name twice.
	Outputs     []string
	Observe     string
	Absence     string
	TimeBudgets map[string]time.Duration
	// Failures is never nil and never empty.
	Failures []Failure
}

// Place returns the part of the type's ID before the first dot.
func (t ResourceType) Place() string {
	place, _, _ := strings.Cut(t.ID, ".")
	return place
}

// clone keeps an empty list empty rather than nil.
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

// ResourceTypes returns fresh copies of every catalogue entry, sorted by ID.
func ResourceTypes() []ResourceType {
	out := make([]ResourceType, len(resourceTypeCatalogue))
	for i, t := range resourceTypeCatalogue {
		out[i] = t.clone()
	}
	slices.SortFunc(out, func(a, b ResourceType) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// LookupResourceType returns a copy of the catalogue entry whose ID is id, matched case-sensitively.
func LookupResourceType(id string) (ResourceType, bool) {
	for _, t := range resourceTypeCatalogue {
		if t.ID == id {
			return t.clone(), true
		}
	}
	return ResourceType{}, false
}

// Capabilities returns the APIs, each existing apart from any one app, that a resource type may live in, rely on or host.
func Capabilities() []string {
	return []string{"anthropic", "aws", "crossplane", "github", "hetzner", "kubernetes", "openai", "plain", "ubicloud"}
}

// ResourceTypeCategories returns the categories a resource type may belong to.
func ResourceTypeCategories() []string {
	return []string{
		"compute", "network", "storage", "database", "artifact-registry", "build-and-delivery",
		"identity-and-access", "secrets", "observability", "messaging", "source-and-work", "ai-model",
	}
}

// ResourceTypeLanguages returns the languages a resource type may serve.
func ResourceTypeLanguages() []string {
	return []string{"go", "node", "python", "java", "rust", "static-site", "any-container"}
}

// ArtifactTypes returns the artifact types a resource type may hold or run.
func ArtifactTypes() []string {
	return []string{"container-image", "helm-chart", "language-package", "binary", "static-bundle", "machine-image"}
}

// ResourceTypeLifecycles returns the lifecycles a resource type may have.
func ResourceTypeLifecycles() []string {
	return []string{"stateful", "replaceable"}
}

// Handovers returns the ways a stateful type's data may survive a replacement.
func Handovers() []string {
	return []string{"snapshot", "copy"}
}

// UpgradeBehaviours returns how upgrading a resource may affect what it serves.
func UpgradeBehaviours() []string {
	return []string{"in-place", "blue-green", "recreate"}
}

// ResourceTypeScopes returns the scopes a resource type may have.
func ResourceTypeScopes() []string {
	return []string{"per-app", "org-wide"}
}

// ResourceVerbs returns the verbs the platform drives a resource through.
func ResourceVerbs() []string {
	return []string{"ensure", "observe", "delete"}
}

// FailureClasses returns the classes a failure may carry.
func FailureClasses() []string {
	return []string{"permanent", "transient"}
}

// InputReplacements returns what changing an input may do to a resource.
func InputReplacements() []string {
	return []string{"update", "replace"}
}

// ValidateResourceTypes returns the first defect it finds in ts, or nil.
//
// The catalogue holds community types only, so an ID is a bare <place>.<name>
// with no @<org>/ namespace. A name after the dot names one real thing in every
// place, so every type sharing a name shares its kind.
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

// validateInputPlace takes "own" only for a type living in a capability and "carrier" only for one living on a kind.
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

func isCapability(c string) bool {
	return slices.Contains(Capabilities(), c)
}

// isCanonicalResourceName does not count an alias.
func isCanonicalResourceName(name string) bool {
	r, ok := LookupResource(name)
	return ok && r.Name == name
}

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

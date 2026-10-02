package contract

import (
	"embed"
	"fmt"
	"regexp"
	"strings"
)

// LifecycleHooksField is the one block manifest field that carries lifecycle
// hook declarations. The kit handler and the platform's manifest parser both
// read hooks from this path, so a block declares them in one place only.
const LifecycleHooksField = "spec.lifecycle.hooks"

// AppNameNamingField is the block manifest field that declares how long an
// app name the block accepts, and the provider names it derives from it.
const AppNameNamingField = "spec.naming.appName"

// AppNameNaming is a block's AppNameNamingField declaration: the longest app
// name the block claims to accept, and every name it derives from the app
// name, which together set that limit.
type AppNameNaming struct {
	// MaxLength is the longest app name the block claims to accept.
	MaxLength int
	// DerivedNames lists every provider resource name the block builds from
	// the app name.
	DerivedNames []DerivedName
}

// DerivedName is one provider resource name a block builds from the app
// name.
type DerivedName struct {
	// Template is the name with the app name written as {appName}.
	Template string
	// Limit is the provider's maximum length for the whole name.
	Limit int
}

// LifecycleHookPoints returns the closed set of lifecycle hook points, in the
// order a subscribe and an unsubscribe reach them. The kit handler and the
// platform's manifest validation both refuse a manifest that declares a hook
// outside this set or declares one twice. Each call returns a fresh slice the
// caller may modify.
func LifecycleHookPoints() []string {
	return []string{"beforeEnsure", "afterEnsure", "beforeDelete", "afterDelete"}
}

// ManifestGoldensDir is the directory inside ManifestGoldens holding the
// committed block manifest fixtures.
const ManifestGoldensDir = "manifest-goldens"

// ManifestGoldens embeds the committed block manifest fixtures. Both the kit
// handler and the platform's manifest parser test against these fixtures, so
// a change to the field on either side turns the other side's tests red.
//
//go:embed manifest-goldens/*.yaml
var ManifestGoldens embed.FS

// HooksManifestGolden returns the manifest golden that declares lifecycle
// hooks under LifecycleHooksField. It panics if the embedded file is
// missing, since that can only be a packaging bug in this module.
func HooksManifestGolden() []byte {
	raw, err := ManifestGoldens.ReadFile(ManifestGoldensDir + "/lifecycle-hooks.yaml")
	if err != nil {
		panic("contract: embedded manifest golden missing: " + err.Error())
	}
	return raw
}

// TypeBlockHooksManifestGolden returns the HooksManifestGolden block written
// with a top-level `type: block` and no top-level `kind`, no spec.layer, a
// provided contract typed with `format:` and a continuity unit classified
// with `store:`. It declares the same hooks as HooksManifestGolden. It panics
// if the embedded file is missing, since that can only be a packaging bug in
// this module.
func TypeBlockHooksManifestGolden() []byte {
	raw, err := ManifestGoldens.ReadFile(ManifestGoldensDir + "/lifecycle-hooks-type-block.yaml")
	if err != nil {
		panic("contract: embedded manifest golden missing: " + err.Error())
	}
	return raw
}

// HooksManifestGoldenDeclares returns the hook names HooksManifestGolden
// declares, in manifest order, so a test on either side can assert its
// parser reads exactly these. Each call returns a fresh slice the caller may
// modify.
func HooksManifestGoldenDeclares() []string {
	return []string{"beforeEnsure", "afterDelete"}
}

// slugPattern is the platform's slug: 1 to 40 characters of lowercase
// letters, digits and dashes, starting and ending with a letter or digit.
// Go's $ matches only at the end of the text, so a trailing newline is
// refused.
var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

func isSlug(s string) bool { return slugPattern.MatchString(s) }

// ParseBlockID splits a block id into its namespace and name. It accepts a
// bare slug, a community block, giving namespace "", and @<org slug>/<slug>,
// a private block, giving the org slug without its @. ADR 0080 sets this
// grammar: community names are bare and private names sit under @<org>/.
// Nothing is trimmed, so an id carrying whitespace is refused. On error both
// returned strings are empty.
func ParseBlockID(id string) (namespace, name string, err error) {
	return parseNamespacedName("block", id)
}

// ParseCapabilityID splits a capability id into its namespace and name,
// under the same grammar as ParseBlockID: a bare slug for a community
// capability, @<org slug>/<slug> for a private one. ADR 0080 sets this
// grammar. On error both returned strings are empty.
func ParseCapabilityID(id string) (namespace, name string, err error) {
	return parseNamespacedName("capability", id)
}

// parseNamespacedName accepts <slug> or @<slug>/<slug>; kind names what the
// id identifies in the error.
func parseNamespacedName(kind, id string) (namespace, name string, err error) {
	if org, rest, ok := splitNamespace(id); ok {
		if isSlug(org) && isSlug(rest) {
			return org, rest, nil
		}
	} else if isSlug(id) {
		return "", id, nil
	}
	return "", "", fmt.Errorf(
		"%s id %q is neither a community id <name> nor a private id @<org>/<name>, "+
			"where each of org and name is 1 to 40 lowercase letters, digits and dashes that start and end with a letter or digit",
		kind, id)
}

// ParseResourceTypeID splits a resource-type id into its namespace,
// capability and name. It accepts <capability slug>.<name>, a community
// type, giving namespace "", and @<org slug>/<capability slug>.<name>, a
// private type, giving the org slug without its @. The name is lowercase
// kebab case. ADR 0080 sets this grammar: community names are bare and
// private names sit under @<org>/. Nothing is trimmed, so an id carrying
// whitespace is refused. On error every returned string is empty.
func ParseResourceTypeID(id string) (namespace, capability, name string, err error) {
	rest := id
	if strings.HasPrefix(id, "@") {
		org, after, ok := splitNamespace(id)
		if !ok || !isSlug(org) {
			return "", "", "", resourceTypeIDError(id)
		}
		namespace, rest = org, after
	}
	capability, name, hasDot := strings.Cut(rest, ".")
	if !hasDot || !isSlug(capability) || !isKebab(name) {
		return "", "", "", resourceTypeIDError(id)
	}
	return namespace, capability, name, nil
}

func resourceTypeIDError(id string) error {
	return fmt.Errorf(
		"resource type id %q is neither a community type <capability>.<name> nor a private type @<org>/<capability>.<name>, "+
			"where org and capability are each 1 to 40 lowercase letters, digits and dashes that start and end with a letter or digit, "+
			"and name is lowercase kebab case",
		id)
}

// splitNamespace splits "@<org>/<rest>" at its first slash. ok is false when
// id does not start with @, or has no slash.
func splitNamespace(id string) (org, rest string, ok bool) {
	after, found := strings.CutPrefix(id, "@")
	if !found {
		return "", "", false
	}
	return strings.Cut(after, "/")
}

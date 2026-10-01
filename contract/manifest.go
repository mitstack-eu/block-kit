package contract

import "embed"

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

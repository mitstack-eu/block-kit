package contract

import "embed"

// LifecycleHooksField is the one block manifest field that carries lifecycle
// hook declarations. The kit handler and the platform's manifest parser both
// read hooks from this path, so a block declares them in one place only.
const LifecycleHooksField = "spec.lifecycle.hooks"

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

// HooksManifestGoldenDeclares returns the hook names HooksManifestGolden
// declares, in manifest order, so a test on either side can assert its
// parser reads exactly these. Each call returns a fresh slice the caller may
// modify.
func HooksManifestGoldenDeclares() []string {
	return []string{"beforeEnsure", "afterDelete"}
}

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/kitbinding"
)

// kitV1Contract is the lifecycle contract on which the kit owns the trust
// boundary, the /hook route and the webhook-contract goldens.
const kitV1Contract = "kit/v1"

// kitHandlerPackage is the import that binds a kit/v1 block to the kit.
const kitHandlerPackage = kitbinding.HandlerPackage

// manifestFile is the block manifest every target carries at its root.
const manifestFile = "manifest.yaml"

// contractRule reads the lifecycle contract target declares and returns the
// one blockverify judges it by: "" for the legacy rule, or kitV1Contract.
// When no rule applies it returns ok false and a failing detail: a manifest
// that cannot be read, or a declared value blockverify has no rule for.
// Neither falls through to the legacy rule, which would judge the block by a
// contract it never declared.
func contractRule(target string) (contract, detail string, ok bool) {
	c, err := declaredContract(target)
	if err != nil {
		return "", err.Error(), false
	}
	switch c {
	case "", kitV1Contract:
		return c, "", true
	}
	return "", fmt.Sprintf(
		"%s declares lifecycle contract %q, which blockverify has no rule for (it knows %s, and no declared contract for the legacy rule)",
		filepath.Join(target, manifestFile), c, kitV1Contract), false
}

// declaredContract returns spec.lifecycle.contract from target's manifest,
// which it reads through contract.ParseManifest under either manifest
// version, or "" when the manifest is absent or declares no lifecycle
// contract. A manifest that is present but cannot be opened (a directory, a
// dangling symlink) or that the parser refuses is an error carrying the
// reason, because a read that could not run is never an absence.
func declaredContract(target string) (string, error) {
	path := filepath.Join(target, manifestFile)
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s is present but could not be read, so the lifecycle contract that judges %s is unknown: %v", path, target, err)
	}
	m, err := contract.ParseManifest(raw)
	if err != nil {
		return "", fmt.Errorf("%s: %v, so the lifecycle contract that judges %s is unknown", path, err, target)
	}
	switch {
	case m.Block != nil:
		return m.Block.Spec.Lifecycle.Contract, nil
	case m.V1Alpha != nil && m.V1Alpha.Spec.Lifecycle != nil:
		return m.V1Alpha.Spec.Lifecycle.Contract, nil
	}
	return "", nil
}

// checkKitHandlerImport requires some file under target to import
// kitHandlerPackage in code a default build compiles, as kitbinding defines
// it, and returns the first such file. A scan that could not run fails
// rather than reading as an absent import.
func checkKitHandlerImport(target string) (detail string, ok bool) {
	files, err := kitbinding.HandlerImportFiles(target)
	if err != nil {
		return fmt.Sprintf("%s rule: could not scan %s for a non-test .go file importing %s: %v",
			kitV1Contract, target, kitHandlerPackage, err), false
	}
	if len(files) == 0 {
		return fmt.Sprintf("%s rule: no non-test .go file under %s imports %s, which is what binds a %s block to the kit",
			kitV1Contract, target, kitHandlerPackage, kitV1Contract), false
	}
	return files[0], true
}

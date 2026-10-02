package main

// manifest_steps_suite_test.go pins how the manifest suite judges a manifest
// that declares spec.steps, as ADR 0079 sets it: its steps, resource
// requirements and offers are validated by contract.ValidateBlockSteps, and
// a capability entry, an extension entry or the retired dependsOn key is
// refused, naming the entry. A manifest without spec.steps is judged exactly
// as before.

import (
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

// steppedSpec is a valid stepped spec body, indented two spaces, that a test
// appends to manifestHead and then breaks one line of.
const steppedSpec = `  params:
    - name: appName
      required: true
  requires:
    - resource: image
      kind: container-image
      reads: [artifact]
    - resource: workflows
      type: github.workflow-file
      many: true
  steps:
    - id: registry
      type: aws.ecr-repository
      ownership: created
      inputs:
        name: params.appName
    - id: build-image
      type: aws.ecr-image
      ownership: created
      inputs:
        registry: steps.registry.outputs.ecrRegistry
        base: requires.image.outputs.artifact
  provides:
    - step: build-image
  outputs:
    - artifact
`

func TestManifestSuite_PassesAValidSteppedManifest(t *testing.T) {
	requireManifestPass(t, manifestSuite(manifestDir(t, manifestHead+steppedSpec), ""))
}

func TestManifestSuite_PassesTheKitStepsGolden(t *testing.T) {
	requireManifestPass(t, manifestSuite(manifestDir(t, string(contract.StepsManifestGolden())), ""))
}

func TestManifestSuite_FailsASteppedManifestTheContractRefuses(t *testing.T) {
	cases := []struct {
		name     string
		old, new string
		want     []string
	}{
		{"uncatalogued step type", "type: aws.ecr-repository", "type: aws.not-a-type",
			[]string{`step "registry"`, `"aws.not-a-type"`}},
		{"step without ownership", "      ownership: created\n      inputs:\n        name: params.appName\n",
			"      inputs:\n        name: params.appName\n",
			[]string{`step "registry"`, "ownership"}},
		{"binding to an undeclared param", "name: params.appName", "name: params.region",
			[]string{`step "registry"`, `param "region"`}},
		{"binding to a later step", "name: params.appName", "name: steps.build-image.outputs.artifact",
			[]string{`step "registry"`, "earlier step"}},
		{"read outside the requirement's reads", "base: requires.image.outputs.artifact", "base: requires.image.outputs.imageRepo",
			[]string{`step "build-image"`, `"imageRepo"`}},
		{"offer naming no step", "- step: build-image", "- step: push",
			[]string{"offer", `"push"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(steppedSpec, tc.old) {
				t.Fatalf("steppedSpec has no %q to replace", tc.old)
			}
			spec := strings.Replace(steppedSpec, tc.old, tc.new, 1)
			requireManifestFail(t, manifestSuite(manifestDir(t, manifestHead+spec), ""), tc.want...)
		})
	}
}

func TestManifestSuite_FailsASteppedManifestCarryingACapabilityEntry(t *testing.T) {
	spec := strings.Replace(steppedSpec, "  requires:\n", "  requires:\n    - capability: github\n", 1)
	requireManifestFail(t, manifestSuite(manifestDir(t, manifestHead+spec), ""), "capability", `"github"`)
}

func TestManifestSuite_FailsASteppedManifestCarryingAnExtensionEntry(t *testing.T) {
	spec := strings.Replace(steppedSpec, "  requires:\n", "  requires:\n    - extension: scaffold-contribution\n", 1)
	requireManifestFail(t, manifestSuite(manifestDir(t, manifestHead+spec), ""), "extension", `"scaffold-contribution"`)
}

func TestManifestSuite_FailsASteppedManifestCarryingDependsOn(t *testing.T) {
	spec := steppedSpec + "  dependsOn:\n    - weather-app\n"
	requireManifestFail(t, manifestSuite(manifestDir(t, manifestHead+spec), ""), "dependsOn")
}

func TestManifestSuite_JudgesAManifestWithoutStepsAsBefore(t *testing.T) {
	// An old-form manifest keeps its capability entries, its dependsOn and
	// its provided capability slugs, and none of them is refused.
	spec := `  dependsOn:
    - weather-app
  provides:
    - runner
  requires:
    - capability: repo
      reads: [repo]
    - capability: app-host
  outputs:
    - repo
`
	requireManifestPass(t, manifestSuite(manifestDir(t, manifestHead+spec), ""))
}

func TestManifestSuite_StillFailsAnOldFormReadItsCapabilityDoesNotGive(t *testing.T) {
	// The old-form read check is unchanged by the steps support.
	spec := "  requires:\n    - capability: github\n      reads: [ecrRegistry]\n"
	requireManifestFail(t, manifestSuite(manifestDir(t, manifestHead+spec), ""), `"ecrRegistry"`, `"github"`)
}

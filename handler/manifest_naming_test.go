// manifest_naming_test.go pins handler.ManifestAppNameNaming, the reader for
// a block manifest's spec.naming.appName declaration: the names a block
// derives from the app name, each a template carrying {appName} with the
// provider's length limit for that resource, and the maxLength the block
// claims for the app name.
//
// The reader anchors on indentation the same way manifestHooks anchors
// spec.lifecycle.hooks. A manifest with no spec.naming.appName declares
// nothing, and a value the reader cannot read is an error, never an absence.
package handler_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
)

// namingManifestHead is a block manifest up to and including spec:, with
// one ordinary spec field. A test appends the spec lines it is about.
const namingManifestHead = `apiVersion: mitstack/v1alpha
kind: Block
metadata:
  id: aws-app-infra
  name: AWS App Infra
spec:
  layer: infra
`

// awsAppInfraNaming is aws-app-infra's declaration: the GitHub Actions ECR
// push role name is 37 fixed characters around the app name, inside IAM's
// 64-character role-name limit.
const awsAppInfraNaming = `  naming:
    appName:
      maxLength: 27
      derivedNames:
        - template: "mitstack-app-{appName}-github-actions-ecr-push"
          limit: 64
`

func TestManifestAppNameNaming_ReadsTheDeclaration(t *testing.T) {
	got, err := handler.ManifestAppNameNaming([]byte(namingManifestHead + awsAppInfraNaming))
	if err != nil {
		t.Fatalf("ManifestAppNameNaming: unexpected error: %v", err)
	}
	want := &contract.AppNameNaming{
		MaxLength: 27,
		DerivedNames: []contract.DerivedName{
			{Template: "mitstack-app-{appName}-github-actions-ecr-push", Limit: 64},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ManifestAppNameNaming = %+v, want %+v", got, want)
	}
}

func TestManifestAppNameNaming_AbsentDeclaresNothing(t *testing.T) {
	cases := map[string]string{
		"empty manifest":         "",
		"no spec.naming":         namingManifestHead,
		"naming with no appName": namingManifestHead + "  naming:\n    somethingElse:\n      maxLength: 10\n",
	}
	for label, manifest := range cases {
		t.Run(label, func(t *testing.T) {
			got, err := handler.ManifestAppNameNaming([]byte(manifest))
			if err != nil {
				t.Fatalf("ManifestAppNameNaming: unexpected error on a manifest with no %s: %v", contract.AppNameNamingField, err)
			}
			if got != nil {
				t.Fatalf("ManifestAppNameNaming = %+v, want nil when no %s is declared", got, contract.AppNameNamingField)
			}
		})
	}
}

func TestManifestAppNameNaming_AnUnreadableValueIsAnError(t *testing.T) {
	cases := map[string]string{
		"maxLength is not an integer": strings.Replace(awsAppInfraNaming, "maxLength: 27", "maxLength: twenty-seven", 1),
		"limit is not an integer":     strings.Replace(awsAppInfraNaming, "limit: 64", "limit: sixty-four", 1),
		"appName is a flow mapping":   "  naming:\n    appName: {maxLength: 27}\n",
	}
	for label, spec := range cases {
		t.Run(label, func(t *testing.T) {
			got, err := handler.ManifestAppNameNaming([]byte(namingManifestHead + spec))
			if err == nil {
				t.Fatalf("ManifestAppNameNaming = %+v, nil error; want an error, since a declaration nobody can read must not be mistaken for an absent one", got)
			}
			if !strings.Contains(err.Error(), contract.AppNameNamingField) {
				t.Errorf("error = %q, want it to name %s", err, contract.AppNameNamingField)
			}
		})
	}
}

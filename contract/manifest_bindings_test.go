package contract_test

// manifest_bindings_test.go pins how ParseManifest judges what a resource's
// inputs bind to under mitstack/v1beta. Each refusal names the resource, the
// input and the reason:
//
//   - a binding to resources.<name> or requires.<name> where the block has no
//     resource or requirement of that name, a requirement being referenced
//     by its name when set and by its kind otherwise;
//   - a binding to resources.<name>.outputs.<output> whose resource's type
//     has no such output, or to requires.<name>.outputs.<output> whose
//     requirement's kind names no such output, everything after .outputs.
//     being the output name;
//   - an input the bound resource's type declares as accepting a resource
//     of one kind, bound to a resource of another kind.
//
// Bindings inside a nested input mapping, as env is, are judged too. An
// input the type does not declare, a param, a platform value, a literal and
// every mitstack/v1alpha manifest stay unjudged.
//
// Each refused manifest is one of ADR 0083's worked manifests with one
// edit, so the edit is the only defect.

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

// editGolden returns the manifest golden called name with each old, new
// pair of replacements applied once. It fails the test when an old text is
// not in the manifest, so an edit can never silently become a no-op.
func editGolden(t *testing.T, name string, pairs ...string) []byte {
	t.Helper()
	out := string(readManifestGolden(t, name))
	for i := 0; i+1 < len(pairs); i += 2 {
		if !strings.Contains(out, pairs[i]) {
			t.Fatalf("fixture edit: %q is not in %s", pairs[i], name)
		}
		out = strings.Replace(out, pairs[i], pairs[i+1], 1)
	}
	return []byte(out)
}

const (
	awsAppInfraGolden = "aws-app-infra-v1beta.yaml"
	plainGolden       = "plain-v1beta.yaml"
)

// The binding aws-app-infra's app-hostname gives its target input, and the
// one plain's tracker gives TRACKER_DB_HOST inside its env mapping.
const (
	appHostnameTarget = "inputs: { target: resources.app-instance.outputs.publicIp }"
	trackerDBHost     = "TRACKER_DB_HOST: requires.postgres-database.outputs.host"
)

func TestParseManifest_ParsesEveryManifestGolden(t *testing.T) {
	entries, err := fs.ReadDir(contract.ManifestGoldens, contract.ManifestGoldensDir)
	if err != nil {
		t.Fatalf("fs.ReadDir(contract.ManifestGoldens, contract.ManifestGoldensDir): %v", err)
	}
	read := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		read++
		t.Run(e.Name(), func(t *testing.T) {
			if _, err := contract.ParseManifest(readManifestGolden(t, e.Name())); err != nil {
				t.Errorf("ParseManifest(%s): %v", e.Name(), err)
			}
		})
	}
	if read == 0 {
		t.Fatal("manifest-goldens holds no file, so parsing every golden proves nothing")
	}
}

func TestParseManifest_V1BetaRefusesABindingToAResourceTheBlockDoesNotHave(t *testing.T) {
	cases := []struct {
		label, binding, missing string
	}{
		{"resources.missing", "resources.missing.outputs.publicIp", "missing"},
		{"a misspelt resource name", "resources.app-instanse.outputs.publicIp", "app-instanse"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			raw := editGolden(t, awsAppInfraGolden, appHostnameTarget, "inputs: { target: "+tc.binding+" }")
			requireRefusedNaming(t, raw, "app-hostname", "target", tc.missing)
		})
	}
}

// The binding sits inside tracker's env mapping, so the refusal also pins
// that a nested input mapping is walked.
func TestParseManifest_V1BetaRefusesABindingToARequirementTheBlockDoesNotHave(t *testing.T) {
	cases := []struct {
		label, binding, missing string
	}{
		{"requires.missing", "requires.missing.outputs.host", "missing"},
		{"a misspelt requirement kind", "requires.postgres-databse.outputs.host", "postgres-databse"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			raw := editGolden(t, plainGolden, trackerDBHost, "TRACKER_DB_HOST: "+tc.binding)
			requireRefusedNaming(t, raw, "tracker", "TRACKER_DB_HOST", tc.missing)
		})
	}
}

// app-instance is an aws.ec2-instance, whose type publishes no dbName.
func TestParseManifest_V1BetaRefusesAResourceOutputItsTypeDoesNotHave(t *testing.T) {
	raw := editGolden(t, awsAppInfraGolden, appHostnameTarget, "inputs: { target: resources.app-instance.outputs.dbName }")
	requireRefusedNaming(t, raw, "app-hostname", "target", "dbName")
}

// The postgres-database kind names no repo output. The binding sits inside
// tracker's env mapping.
func TestParseManifest_V1BetaRefusesARequirementOutputItsKindDoesNotName(t *testing.T) {
	raw := editGolden(t, plainGolden, trackerDBHost, "TRACKER_DB_HOST: requires.postgres-database.outputs.repo")
	requireRefusedNaming(t, raw, "tracker", "TRACKER_DB_HOST", "repo")
}

// aws.ec2-instance declares securityGroup as accepting a resource of kind
// firewall. Here it is bound to app-database, a host.postgres-database of
// kind postgres-database, which lives on a required host and is created
// before app-instance.
func TestParseManifest_V1BetaRefusesAnInputBoundToAResourceOfAnotherKind(t *testing.T) {
	raw := editGolden(t, awsAppInfraGolden,
		"    - kind: git-repository\n      in: github\n",
		"    - kind: git-repository\n      in: github\n    - kind: host\n",
		"    - name: app-instance\n",
		"    - name: app-database\n      type: host.postgres-database\n      on: requires.host\n    - name: app-instance\n",
		"        securityGroup: resources.app-security-group\n",
		"        securityGroup: resources.app-database\n")
	requireRefusedNaming(t, raw, "app-instance", "securityGroup", "firewall", "postgres-database")
}

// aws.ecr-image declares repository as accepting the registryHost output of
// a container-repository. Here it is bound to the repo output of the
// git-repository requirement, an output that kind does name, and its other
// required input, tag, takes a literal.
func TestParseManifest_V1BetaRefusesAnInputBoundToAnOutputOfAnotherKind(t *testing.T) {
	raw := editGolden(t, awsAppInfraGolden,
		"    - name: ecr-push-role\n",
		"    - name: app-image\n      type: aws.ecr-image\n"+
			"      inputs: { repository: requires.git-repository.outputs.repo, tag: latest }\n"+
			"    - name: ecr-push-role\n")
	requireRefusedNaming(t, raw, "app-image", "container-repository", "registryHost", "git-repository")

	// The input's name is part of both kind names, so it is looked for in
	// the error with the kind names taken out.
	_, err := contract.ParseManifest(raw)
	if err == nil {
		return
	}
	rest := strings.NewReplacer("container-repository", "", "git-repository", "").Replace(err.Error())
	if !strings.Contains(rest, "repository") {
		t.Errorf("ParseManifest error = %q, want it to name the input %q apart from the kind names", err, "repository")
	}
}

// What the parser leaves unjudged, and the bindings that resolve, parse.
func TestParseManifest_V1BetaAcceptsBindingsItDoesNotJudgeOrThatResolve(t *testing.T) {
	cases := []struct {
		label string
		raw   func(t *testing.T) []byte
	}{
		{"an input the type does not declare", func(t *testing.T) []byte {
			// aws.ebs-volume declares no attachTo input, so its binding is not judged.
			return readManifestGolden(t, awsAppInfraGolden)
		}},
		{"a param, a platform value and a literal, at the top and nested", func(t *testing.T) []byte {
			return editGolden(t, plainGolden,
				"        port: 8080\n",
				"        port: 8080\n        label: app.name\n",
				"        env:\n",
				"        env:\n          TRACKER_IMAGE: params.image\n          TRACKER_APP: app.name\n          TRACKER_PORT: 8080\n")
		}},
		{"an output name holding a dot", func(t *testing.T) []byte {
			return editGolden(t, awsAppInfraGolden, appHostnameTarget,
				"inputs: { target: resources.app-instance.outputs.publicIp, region: resources.app-instance.outputs.cloud.region }")
		}},
		{"a named requirement referenced by its name", func(t *testing.T) []byte {
			return []byte(strings.ReplaceAll(
				string(editGolden(t, plainGolden, "    - kind: postgres-database\n", "    - kind: postgres-database\n      name: tracker-db\n")),
				"requires.postgres-database.outputs.", "requires.tracker-db.outputs."))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			parseBeta(t, tc.raw(t))
		})
	}
}

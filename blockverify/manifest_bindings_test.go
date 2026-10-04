package main

// manifest_bindings_test.go pins that blockverify fails its manifest suite on
// a mitstack/v1beta manifest whose input bindings the kit's parser refuses,
// printing the parser's reason. Each fixture under testdata holds one
// manifest.yaml: ADR 0083's worked aws-app-infra manifest with one edit, so
// the edit is the only defect.
//
//   - manifest-binding-missing-name binds app-hostname's target to a
//     resource the block does not have;
//   - manifest-binding-missing-output binds it to an output app-instance's
//     type does not have;
//   - manifest-binding-wrong-kind binds app-instance's securityGroup, which
//     accepts a firewall, to a resource of kind postgres-database.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

// bindingFixtures are the testdata directories, the edits that turn the
// aws-app-infra golden into each fixture's manifest, and the parts the
// manifest suite's detail must name.
var bindingFixtures = []struct {
	dir   string
	edits []string
	wants []string
}{
	{
		dir: "manifest-binding-missing-name",
		edits: []string{
			"inputs: { target: resources.app-instance.outputs.publicIp }",
			"inputs: { target: resources.missing.outputs.publicIp }",
		},
		wants: []string{"app-hostname", "target", "missing"},
	},
	{
		dir: "manifest-binding-missing-output",
		edits: []string{
			"inputs: { target: resources.app-instance.outputs.publicIp }",
			"inputs: { target: resources.app-instance.outputs.dbName }",
		},
		wants: []string{"app-hostname", "target", "dbName"},
	},
	{
		dir: "manifest-binding-wrong-kind",
		edits: []string{
			"    - kind: git-repository\n      in: github\n",
			"    - kind: git-repository\n      in: github\n    - kind: host\n",
			"    - name: app-instance\n",
			"    - name: app-database\n      type: host.postgres-database\n      on: requires.host\n    - name: app-instance\n",
			"        securityGroup: resources.app-security-group\n",
			"        securityGroup: resources.app-database\n",
		},
		wants: []string{"app-instance", "securityGroup", "firewall", "postgres-database"},
	},
}

// awsAppInfraWithEdits returns the aws-app-infra manifest golden with each
// old, new pair of edits applied once, failing the test when an old text is
// not in it.
func awsAppInfraWithEdits(t *testing.T, edits []string) string {
	t.Helper()
	raw, err := contract.ManifestGoldens.ReadFile(contract.ManifestGoldensDir + "/aws-app-infra-v1beta.yaml")
	if err != nil {
		t.Fatalf("reading aws-app-infra-v1beta.yaml from contract.ManifestGoldens: %v", err)
	}
	out := string(raw)
	for i := 0; i+1 < len(edits); i += 2 {
		if !strings.Contains(out, edits[i]) {
			t.Fatalf("fixture edit: %q is not in aws-app-infra-v1beta.yaml", edits[i])
		}
		out = strings.Replace(out, edits[i], edits[i+1], 1)
	}
	return out
}

func TestRun_FailsTheManifestSuiteOnABindingTheParserRefuses(t *testing.T) {
	for _, fx := range bindingFixtures {
		t.Run(fx.dir, func(t *testing.T) {
			dir := filepath.Join("testdata", fx.dir)
			raw, err := os.ReadFile(filepath.Join(dir, manifestFile))
			if err != nil {
				t.Fatalf("reading the fixture: %v", err)
			}
			if want := awsAppInfraWithEdits(t, fx.edits); string(raw) != want {
				t.Fatalf("%s/%s is not the aws-app-infra golden with its one edit, so it may carry a second defect; want:\n%s",
					dir, manifestFile, want)
			}

			stdout, _ := runBlockverify(t, "--json", "--standalone", dir)
			m := suiteByName(t, decodeReport(t, stdout), "manifest")
			if m.Status != "fail" {
				t.Fatalf("manifest status = %q, want %q; detail: %s", m.Status, "fail", m.Detail)
			}
			for _, want := range append([]string{manifestFile}, fx.wants...) {
				if !strings.Contains(m.Detail, want) {
					t.Errorf("manifest detail = %q, want it to name %q", m.Detail, want)
				}
			}
			_, perr := contract.ParseManifest(raw)
			if perr == nil {
				t.Fatalf("contract.ParseManifest accepts %s/%s, so the suite's failure is not the parser's reason", dir, manifestFile)
			}
			if !strings.Contains(m.Detail, perr.Error()) {
				t.Errorf("manifest detail = %q, want it to carry the parser's reason %q", m.Detail, perr.Error())
			}
		})
	}
}

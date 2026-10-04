package main

// manifest_kubernetes_namespace_test.go pins that blockverify's manifest
// suite judges where a kubernetes.namespace is placed. Both fixtures under
// testdata are one self-contained mitstack/v1beta block that creates an app's
// namespace and offers it by name.
//
//   - manifest-kubernetes-namespace-on-cluster places the namespace on a
//     required kubernetes-cluster and binds its cluster input to it;
//   - manifest-binding-namespace-on-host is the same block requiring a host
//     instead, so its only defect is binding cluster to a resource of kind
//     host.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

const (
	namespaceOnClusterDir = "manifest-kubernetes-namespace-on-cluster"
	namespaceOnHostDir    = "manifest-binding-namespace-on-host"
)

// namespaceOnHostEdits turn the on-cluster fixture into the on-host one, as
// old, new pairs applied once each.
var namespaceOnHostEdits = []string{
	"    - kind: kubernetes-cluster\n", "    - kind: host\n",
	"on: requires.kubernetes-cluster", "on: requires.host",
	"inputs: { cluster: requires.kubernetes-cluster }", "inputs: { cluster: requires.host }",
}

// readFixtureManifest returns the manifest of the testdata fixture dir.
func readFixtureManifest(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", dir, manifestFile))
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	return string(raw)
}

// The suite passing is evidence only when the type's cluster input is judged,
// and the parser judges the inputs of a catalogued type alone, so the test
// first holds the type to its catalogue entry.
func TestRun_PassesTheManifestSuiteOnAKubernetesNamespacePlacedOnACluster(t *testing.T) {
	rt, ok := contract.LookupResourceType("kubernetes.namespace")
	if !ok {
		t.Fatal("the catalogue has no kubernetes.namespace, so the parser judges none of its inputs and a pass proves nothing")
	}
	if len(rt.Inputs) != 1 || rt.Inputs[0].Name != "cluster" || rt.Inputs[0].Accepts != "resource:kubernetes-cluster" {
		t.Fatalf("kubernetes.namespace inputs = %+v, want one cluster input accepting resource:kubernetes-cluster", rt.Inputs)
	}

	raw := readFixtureManifest(t, namespaceOnClusterDir)
	if !strings.HasPrefix(raw, "apiVersion: "+contract.ManifestAPIVersion+"\n") {
		t.Fatalf("the fixture is not a %s manifest", contract.ManifestAPIVersion)
	}
	stdout, _ := runBlockverify(t, "--json", "--standalone", filepath.Join("testdata", namespaceOnClusterDir))
	m := suiteByName(t, decodeReport(t, stdout), "manifest")
	if m.Status != "pass" {
		t.Errorf("manifest status = %q, want %q; detail: %s", m.Status, "pass", m.Detail)
	}
}

func TestRun_FailsTheManifestSuiteOnAKubernetesNamespacePlacedOnAHost(t *testing.T) {
	want := readFixtureManifest(t, namespaceOnClusterDir)
	for i := 0; i+1 < len(namespaceOnHostEdits); i += 2 {
		if !strings.Contains(want, namespaceOnHostEdits[i]) {
			t.Fatalf("fixture edit: %q is not in %s/%s", namespaceOnHostEdits[i], namespaceOnClusterDir, manifestFile)
		}
		want = strings.Replace(want, namespaceOnHostEdits[i], namespaceOnHostEdits[i+1], 1)
	}
	raw := readFixtureManifest(t, namespaceOnHostDir)
	if raw != want {
		t.Fatalf("%s/%s is not the on-cluster fixture with the requirement's kind changed to host, so it may carry a second defect; want:\n%s",
			namespaceOnHostDir, manifestFile, want)
	}

	stdout, _ := runBlockverify(t, "--json", "--standalone", filepath.Join("testdata", namespaceOnHostDir))
	m := suiteByName(t, decodeReport(t, stdout), "manifest")
	if m.Status != "fail" {
		t.Fatalf("manifest status = %q, want %q; detail: %s", m.Status, "fail", m.Detail)
	}
	for _, part := range []string{manifestFile, `"app-namespace"`, `input "cluster"`, "kubernetes-cluster", "requires.host"} {
		if !strings.Contains(m.Detail, part) {
			t.Errorf("manifest detail = %q, want it to name %q", m.Detail, part)
		}
	}
	_, perr := contract.ParseManifest([]byte(raw))
	if perr == nil {
		t.Fatalf("contract.ParseManifest accepts %s/%s, so the suite's failure is not the parser's reason", namespaceOnHostDir, manifestFile)
	}
	if !strings.Contains(m.Detail, perr.Error()) {
		t.Errorf("manifest detail = %q, want it to carry the parser's reason %q", m.Detail, perr.Error())
	}
}

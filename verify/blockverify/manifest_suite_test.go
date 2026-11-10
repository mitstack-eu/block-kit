package main

// The manifest suite and the contract rule read a manifest through contract.ParseManifest and judge what it declares, and the suite compares the block's type snapshot with the one the platform serves.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
)

// manifestHead ends inside spec, so a test appends spec keys to it.
const manifestHead = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: manifest-fixture
  name: Manifest Fixture
  owner: blockverify-tests
spec:
  lifecycle:
    endpoint: https://fixture.example.test/hook
    operations: [subscribe, unsubscribe]
    contract: kit/v1
  resources: [aws.s3-bucket]
  description: A block for the manifest suite.
  version: 1.0.0
`

const oldVersionManifest = `apiVersion: mitstack/v1alpha
kind: Block
metadata:
  id: manifest-fixture
  name: Manifest Fixture
spec:
  version: 1.0.0
`

func withVersion(t *testing.T, v string) string {
	return replaced(t, manifestHead, "version: 1.0.0", "version: "+v)
}

func withID(t *testing.T, idLine string) string {
	return replaced(t, manifestHead, "  id: manifest-fixture\n", "  id: "+idLine+"\n")
}

func plainV1Beta(t *testing.T) string {
	t.Helper()
	raw, err := contract.ManifestGoldens.ReadFile(contract.ManifestGoldensDir + "/plain-v1beta.yaml")
	if err != nil {
		t.Fatalf("reading plain-v1beta.yaml from contract.ManifestGoldens: %v", err)
	}
	return string(raw)
}

func missingBase(t *testing.T) string { return filepath.Join(t.TempDir(), "base-manifest.yaml") }

func dirBase(t *testing.T) string {
	base := missingBase(t)
	mkdir(filepath.Base(base))(t, filepath.Dir(base))
	return base
}

// manifestWithSnapshot holds the two files the manifest suite reads, the manifest and the platform's type snapshot.
func manifestWithSnapshot(content string) dirFunc {
	return fixture(func(t *testing.T) map[string]string {
		files := map[string]string{manifestFile: content}
		withTypeSnapshot(t, files)
		return files
	})
}

func passes(name, content string) suiteCase {
	return suiteCase{name: name, dir: manifestWithSnapshot(content), status: "pass"}
}

func fails(name, content string, detail ...string) suiteCase {
	return suiteCase{name: name, dir: manifestWithSnapshot(content), status: "fail", detail: detail}
}

func TestManifestSuite(t *testing.T) {
	head, v1beta := manifestHead, plainV1Beta(t)
	from100 := baseManifest(withVersion(t, "1.0.0"))
	cases := []suiteCase{
		{name: "a directory with no manifest is skipped", dir: emptyDir, status: "skipped"},
		passes("a manifest that parses passes, naming its version", head),
		passes("a worked v1beta manifest passes", v1beta),
		fails("a description longer than the platform allows fails, naming the limit",
			replaced(t, head, "description: A block for the manifest suite.", "description: "+strings.Repeat("A long description. ", 13)),
			"manifest.yaml", "spec.description must be 10-240 characters"),
		fails("a type the block's snapshot lacks fails, naming it", replaced(t, head, "resources: [aws.s3-bucket]", "resources: [example.no-such-type]"),
			"manifest.yaml", "spec.resources[0]", "example.no-such-type"),
		{name: "a block with no type snapshot fails, naming the file", dir: manifestOnly(head), status: "fail", detail: []string{"type-snapshot.json"}},
		fails("a manifest in the old version fails, naming the version the kit knows", oldVersionManifest,
			"manifest.yaml", `apiVersion "mitstack/v1alpha" is not a manifest version the kit knows; it must be mitstack/v1beta`),
		{name: "an unchanged version passes without a release class or note", dir: manifestWithSnapshot(withVersion(t, "1.0.0")), base: from100, status: "pass"},
		{name: "a changed version with a class and a plain note passes", base: from100, status: "pass",
			dir: manifestWithSnapshot(withVersion(t, "1.1.0") + "  releaseClass: feature\n  releaseNote: Adds the bucket.\n")},
		{name: "a changed version with a class and a folded note passes", base: from100, status: "pass",
			dir: manifestWithSnapshot(withVersion(t, "1.1.0") + "  releaseClass: correctness-fix\n  releaseNote: >\n    Fixes the retry that dropped the second\n    attempt's outputs.\n")},
		{name: "a changed version with a literal note before its class passes", base: from100, status: "pass",
			dir: manifestWithSnapshot(withVersion(t, "1.1.0") + "  releaseNote: |\n    Renames the output.\n  releaseClass: breaking\n")},
		{name: "a changed version with no release class fails, naming it", base: from100, status: "fail", detail: []string{"spec.releaseClass"},
			dir: manifestWithSnapshot(withVersion(t, "1.1.0") + "  releaseNote: Adds the bucket.\n")},
		{name: "a changed version with no release note fails, naming it", base: from100, status: "fail", detail: []string{"spec.releaseNote"},
			dir: manifestWithSnapshot(withVersion(t, "1.1.0") + "  releaseClass: feature\n")},
		{name: "a changed version with a blank release note fails, naming it", base: from100, status: "fail", detail: []string{"spec.releaseNote"},
			dir: manifestWithSnapshot(withVersion(t, "1.1.0") + "  releaseClass: feature\n  releaseNote: \"   \"\n")},
		{name: "a changed version with neither class nor note names both", dir: manifestWithSnapshot(withVersion(t, "2.0.0")), base: from100,
			status: "fail", detail: []string{"spec.releaseClass", "spec.releaseNote"}},
		{name: "a base manifest that does not exist marks the block new and passes", dir: manifestWithSnapshot(withVersion(t, "1.1.0")), base: missingBase, status: "pass"},
		passes("no --base-manifest compares no version", withVersion(t, "1.1.0")),
		{name: "an unreadable base manifest fails, naming it", dir: manifestWithSnapshot(withVersion(t, "1.1.0")), base: dirBase, status: "fail", detail: []string{"$BASE"}},
		{name: "a worked v1beta manifest passes against an older base", dir: manifestWithSnapshot(v1beta), base: baseManifest(replaced(t, v1beta, "version: 3.0.0", "version: 2.4.0")), status: "pass"},
		{name: "a pull request changing resource.go with the base's version fails, naming the missing bump", dir: manifestWithSnapshot(withVersion(t, "1.0.0")), base: from100,
			changed: changedFiles("resource.go"), status: "fail", detail: []string{"resource.go", "spec.version 1.0.0", "version bump"}},
		{name: "a dependency update changing go.mod and go.sum with the base's version fails, naming both", dir: manifestWithSnapshot(withVersion(t, "1.0.0")), base: from100,
			changed: changedFiles("go.mod", "go.sum"), status: "fail", detail: []string{"go.mod", "go.sum", "version bump"}},
		{name: "a pull request changing only README.md and .github/workflows/ci.yml with the base's version passes", dir: manifestWithSnapshot(withVersion(t, "1.0.0")), base: from100,
			changed: changedFiles("README.md", ".github/workflows/ci.yml"), status: "pass"},
		{name: "a pull request changing only nested Markdown and LICENSE with the base's version passes", dir: manifestWithSnapshot(withVersion(t, "1.0.0")), base: from100,
			changed: changedFiles("docs/guide.md", "LICENSE"), status: "pass"},
		{name: "a pull request changing nothing with the base's version passes", dir: manifestWithSnapshot(withVersion(t, "1.0.0")), base: from100,
			changed: changedFiles(), status: "pass"},
		{name: "a pull request changing resource.go with a raised, released version passes", base: from100, changed: changedFiles("resource.go"), status: "pass",
			dir: manifestWithSnapshot(withVersion(t, "1.1.0") + "  releaseClass: correctness-fix\n  releaseNote: Fixes the retry.\n")},
		{name: "a pull request changing resource.go on a block new to the branch passes", dir: manifestWithSnapshot(withVersion(t, "1.0.0")), base: missingBase,
			changed: changedFiles("resource.go"), status: "pass"},
		{name: "a pull request changing five image files names three and counts the rest", dir: manifestWithSnapshot(withVersion(t, "1.0.0")), base: from100,
			changed: changedFiles("a.go", "b.go", "c.go", "d.go", "e.go"), status: "fail", detail: []string{"a.go", "b.go", "c.go", "2 more"}, not: []string{"d.go"}},
		{name: "a changed-files list that cannot be read fails, naming it", dir: manifestWithSnapshot(withVersion(t, "1.0.0")), base: from100,
			changed: func(t *testing.T) string { return filepath.Join(t.TempDir(), "changed-files") }, status: "fail", detail: []string{"$CHANGED"}},
		passes("a bare slug metadata.id passes", withID(t, "billing-db")),
		passes("a double-quoted private metadata.id passes", withID(t, `"@blockverify-tests/billing-db"`)),
		passes("a single-quoted private metadata.id passes", withID(t, `'@blockverify-tests/billing-db'`)),
		passes("a private metadata.id with a comment passes", withID(t, `"@blockverify-tests/api" # the org's own api block`)),
		passes("a forty-character bare slug passes", withID(t, "a"+strings.Repeat("b", 38)+"c")),
		fails("metadata as a flow mapping is a failure", replaced(t, head, "metadata:\n  id: manifest-fixture\n  name: Manifest Fixture\n  owner: blockverify-tests\n",
			"metadata: {id: \"@Acme/x\", name: Manifest Fixture}\n"), "manifest.yaml", "metadata"),
		fails("metadata.id as an alias is a failure", replaced(t, head, "  id: manifest-fixture\n  name: Manifest Fixture\n", "  name: &bad \"@Acme/x\"\n  id: *bad\n"),
			"manifest.yaml", "metadata"),
		fails("metadata.id as a mapping is a failure", withID(t, "\n    org: Acme"), "manifest.yaml", "metadata"),
		{name: "a manifest.yaml that is a directory is a failure", dir: then(emptyDir, mkdir(manifestFile)), status: "fail", detail: []string{"manifest.yaml"}},
		fails("a v1beta manifest carrying spec.outputs fails as the parser refuses it", v1beta+"  outputs: [host]\n", "manifest.yaml", "spec.outputs"),
		fails("an unknown apiVersion fails, naming the known version", replaced(t, head, "apiVersion: mitstack/v1beta", "apiVersion: mitstack/v2"),
			"manifest.yaml", "mitstack/v2", contract.ManifestAPIVersion),
		fails("no apiVersion fails, naming the known version", replaced(t, head, "apiVersion: mitstack/v1beta\n", ""),
			"manifest.yaml", contract.ManifestAPIVersion),
	}
	for _, idLine := range []string{`"@Acme/x"`, `"@acme/"`, "acme/x", `"@acme/x/y"`, "Billing-DB", "billing-", "a" + strings.Repeat("b", 39) + "c"} {
		cases = append(cases, fails("metadata.id "+idLine+" outside both forms fails, naming it", withID(t, idLine), "manifest.yaml", "metadata.id", "@<org>/"))
	}
	for _, idLine := range []string{"@acme/billing-db", "@acme/billing-db # the billing database", "`billing-db`"} {
		cases = append(cases, fails("unquoted metadata.id "+idLine+" fails, asking for quotes", withID(t, idLine), "manifest.yaml", "metadata.id", "quote"))
	}
	for path, spec := range map[string]string{
		"spec.frobnicate":             "  frobnicate: true\n",
		"spec.futureSection":          "  futureSection:\n    outputs: not-the-outputs\n",
		"spec.requires[0].frobnicate": "  requires:\n    - type: aws.vpc\n      frobnicate: true\n",
		"spec.dependsOn":              "  dependsOn:\n    - weather-app\n",
	} {
		cases = append(cases, fails("an unknown field "+path+" fails with the parser's message", head+spec, "manifest.yaml", path, refusal(t, head+spec, path)))
	}
	for label, content := range map[string]string{
		"a base under mitstack/v2":           replaced(t, withVersion(t, "1.0.0"), "apiVersion: mitstack/v1beta", "apiVersion: mitstack/v2"),
		"a base in the old version":          oldVersionManifest,
		"a base with an unquoted private id": withID(t, "@acme/billing-db"),
	} {
		_, parseErr := contract.ParseManifest([]byte(content))
		if parseErr == nil {
			t.Fatalf("the %s fixture parses, so it proves nothing:\n%s", label, content)
		}
		cases = append(cases,
			suiteCase{name: label + " leaves its version unknown, so a released change passes", base: baseManifest(content), status: "pass",
				dir: manifestWithSnapshot(withVersion(t, "1.0.0") + "  releaseClass: feature\n  releaseNote: Fixes the base manifest.\n")},
			suiteCase{name: label + " leaves its version unknown, so an unreleased change names the base, its error and both fields", base: baseManifest(content),
				dir: manifestWithSnapshot(withVersion(t, "1.0.0")), status: "fail", detail: []string{"$BASE", parseErr.Error(), "spec.releaseClass", "spec.releaseNote"}})
	}

	runSuites(t, []string{"manifest"}, cases)
}

const servedRoute = "/v1/resource-types/snapshots/latest"

func snapshotType(id string, outputs ...string) string {
	capability, _, _ := strings.Cut(id, ".")
	return fmt.Sprintf(`{"id": %q, "capability": %q, "kind": "fixture", "scope": "per-app", "version": 1, "outputs": [%s]}`, id, capability, strings.Join(outputs, ", "))
}

func snapshotOf(types ...string) string {
	return `{"version": 84, "types": [` + strings.Join(types, ", ") + `]}`
}

func serve(snapshot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != servedRoute {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, snapshot)
	}
}

// Only the types the manifest names are compared, each field and each output, but for a type's own version, which no check reads, and a route that does not answer fails rather than passes unread.
func TestManifestSuite_ComparesTheCopyWithTheServedSnapshotInEachTypeTheManifestNames(t *testing.T) {
	const cnpg = "kubernetes.cloudnativepg-database"
	host, password := `{"name": "host", "required": true, "schema": {"type": "string"}}`, `{"name": "password", "secret": true, "required": true}`
	bucket, vpc := snapshotType("aws.s3-bucket"), snapshotType("aws.vpc", `{"name": "vpcId", "required": true}`)
	served := snapshotOf(bucket, vpc, snapshotType(cnpg, host, password))
	down := func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "the catalogue is down", http.StatusServiceUnavailable)
	}
	cut := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = io.WriteString(w, "{")
	}
	cnpgWith := func(old, new string) string { return replaced(t, snapshotType(cnpg, host, password), old, new) }
	cases := []struct {
		name, copy string
		answer     http.HandlerFunc
		status     string
		detail     []string
	}{
		{"a copy adding connectionSecret to a type served without it fails, naming the type, the output and the refresh", snapshotOf(bucket, vpc,
			snapshotType(cnpg, host, password, `{"name": "connectionSecret", "secret": true}`)), serve(served), "fail",
			[]string{"type " + cnpg + " output connectionSecret is in type-snapshot.json but not served", "curl -fsS $URL" + servedRoute + " -o type-snapshot.json"}},
		{"a copy matching the served snapshot in each type the manifest names passes", served, serve(served), "pass",
			[]string{"type-snapshot.json matches the snapshot version 84 that $URL" + servedRoute + " serves"}},
		{"a type the manifest does not name may differ", snapshotOf(bucket, snapshotType("aws.vpc"), snapshotType(cnpg, host, password)), serve(served), "pass", nil},
		{"a copy differing in a type the manifest's resources declare fails, naming it", snapshotOf(snapshotType("aws.s3-bucket", `{"name": "bucketName"}`), vpc,
			snapshotType(cnpg, host, password)), serve(served), "fail", []string{"type aws.s3-bucket output bucketName is in type-snapshot.json but not served"}},
		{"an output the platform changed fails, naming both forms", snapshotOf(bucket, vpc, snapshotType(cnpg, `{"name": "host", "required": true}`, password)), serve(served), "fail",
			[]string{"type " + cnpg + ` output host is secret false, required true, schema none in type-snapshot.json and secret false, required true, schema {"type":"string"} as served`}},
		{"an output the platform added fails until the copy is refreshed", snapshotOf(bucket, vpc, snapshotType(cnpg, host)), serve(served), "fail",
			[]string{"type " + cnpg + " output password is served but not in type-snapshot.json"}},
		{"a type the platform no longer serves fails, naming it", served, serve(snapshotOf(bucket, vpc)), "fail", []string{"type " + cnpg + " is not in the served snapshot"}},
		{"a route answering an error fails, naming the route and its answer", served, down, "fail", []string{"GET $URL" + servedRoute, "HTTP 503", "the catalogue is down"}},
		{"a route that does not answer fails, naming the route", served, nil, "fail", []string{"GET $URL" + servedRoute + " did not answer"}},
		{"a field the platform changed fails, naming the field and both values", snapshotOf(bucket, vpc, cnpgWith(`"kind": "fixture"`, `"kind": "fixture", "providerHoldsNothing": true`)),
			serve(served), "fail", []string{"type " + cnpg + " field providerHoldsNothing is true in type-snapshot.json and false as served"}},
		{"a type whose own version alone differs passes", snapshotOf(bucket, vpc, cnpgWith(`"version": 1,`, `"version": 3,`)), serve(served), "pass", nil},
		{"an empty list and an absent one read the same", snapshotOf(bucket, vpc, cnpgWith(`"kind": "fixture"`, `"kind": "fixture", "languages": []`)), serve(served), "pass", nil},
		{"a route answering a snapshot the kit cannot read fails, naming the route and the reason", served, serve(`{"version": 84, "types": []}`), "fail",
			[]string{"GET $URL" + servedRoute + " answered a snapshot the kit cannot read", "holds no type"}},
		{"a route cutting its answer short fails, naming the route", served, cut, "fail", []string{"GET $URL" + servedRoute + " answered HTTP 200 with a body that could not be read"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(c.answer)
			t.Cleanup(srv.Close)
			if c.answer == nil {
				srv.Close()
			}
			dir := fixture(func(*testing.T) map[string]string {
				return map[string]string{manifestFile: manifestHead + "  requires: [" + cnpg + "]\n", typeSnapshotFile: c.copy}
			})(t)

			got := manifestSuite(dir, "", "", srv.URL)

			if got.Status != c.status {
				t.Errorf("manifestSuite with --api-url %s = %q, detail %q; want %q", srv.URL, got.Status, got.Detail, c.status)
			}
			for _, want := range c.detail {
				if want = strings.ReplaceAll(want, "$URL", srv.URL); !strings.Contains(got.Detail, want) {
					t.Errorf("manifestSuite detail = %q; want it to contain %q", got.Detail, want)
				}
			}
		})
	}
}

func TestRequireKitV1Contract(t *testing.T) {
	cases := []struct {
		name        string
		dir         dirFunc
		wantRefusal []string
	}{
		{name: "the worked v1beta block declares kit/v1", dir: manifestOnly(plainV1Beta(t))},
		{name: "a flow-mapped lifecycle declaring kit/v1 passes", dir: manifestOnly(replaced(t, manifestHead,
			"  lifecycle:\n    endpoint: https://fixture.example.test/hook\n    operations: [subscribe, unsubscribe]\n    contract: kit/v1\n",
			"  lifecycle: {endpoint: https://fixture.example.test/hook, operations: [subscribe, unsubscribe], contract: kit/v1}\n"))},
		{name: "another contract fails, naming it", dir: manifestOnly(replaced(t, manifestHead, "contract: kit/v1", "contract: kit/v2")),
			wantRefusal: []string{"manifest.yaml", `"kit/v2"`}},
		{name: "no manifest fails, naming manifest.yaml", dir: emptyDir, wantRefusal: []string{"manifest.yaml"}},
		{name: "a manifest in the old version fails with the parser's message", dir: manifestOnly(oldVersionManifest),
			wantRefusal: []string{"manifest.yaml", refusal(t, oldVersionManifest, "apiVersion")}},
	}
	for path, content := range map[string]string{
		"spec.frobnicate":           manifestHead + "  frobnicate: true\n",
		"spec.lifecycle.frobnicate": replaced(t, manifestHead, "    contract: kit/v1\n", "    contract: kit/v1\n    frobnicate: true\n"),
		"metadata.frobnicate":       replaced(t, manifestHead, "  owner: blockverify-tests\n", "  owner: blockverify-tests\n  frobnicate: true\n"),
		"spec.lifecycle.hooks":      replaced(t, manifestHead, "    contract: kit/v1\n", "    contract: kit/v1\n    hooks: [beforeEnsure]\n"),
	} {
		cases = append(cases, struct {
			name        string
			dir         dirFunc
			wantRefusal []string
		}{name: "a manifest the parser refuses at " + path + " fails with its message", dir: manifestOnly(content),
			wantRefusal: []string{"manifest.yaml", refusal(t, content, path)}})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := c.dir(t)

			detail, ok := requireKitV1Contract(dir)

			if c.wantRefusal == nil && !ok {
				t.Errorf("requireKitV1Contract(%s) = %q, false; want it to pass", dir, detail)
			}
			if c.wantRefusal != nil && ok {
				t.Errorf("requireKitV1Contract(%s) passed; want a failure carrying %q", dir, c.wantRefusal)
			}
			for _, want := range c.wantRefusal {
				if !strings.Contains(detail, want) {
					t.Errorf("requireKitV1Contract(%s) detail %q; want it to carry %q", dir, detail, want)
				}
			}
		})
	}
}

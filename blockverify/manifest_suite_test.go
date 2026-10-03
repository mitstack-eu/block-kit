package main

// manifest_suite_test.go pins the manifest suite: blockverify reads the
// target's manifest.yaml and checks it against the resource registry the kit
// owns (contract.LookupResource) and against the base branch's manifest.
//
//   - every spec.outputs name resolves through the registry, or the suite
//     fails naming the output and the nearest registered name;
//   - every spec.requires[].reads name resolves, and its entry lists that
//     requirement's capability, with the platform's own refusal messages;
//   - a changed spec.version against --base-manifest needs a release class
//     and a release note;
//   - metadata.id, when present, parses through contract.ParseBlockID: a
//     bare community slug or @<org>/<name>, or the suite fails naming
//     metadata.id and both forms; a manifest with no metadata.id keeps the
//     verdict its spec earns;
//   - both manifest spellings (type/format/store and kind in all three
//     places) pass, and a manifest carrying an unknown spec field fails,
//     naming the field;
//   - a block that declares no reads passes, since operators add their
//     declarations one at a time;
//   - no manifest is a skip (trust-plumbing fails that), and a manifest the
//     reader cannot follow is a failure, never a pass.
//
// The suite is called directly as manifestSuite(target, baseManifest), where
// baseManifest is the --base-manifest path or "" when the flag is absent.
// The Run-level tests at the end pin the flag and the whole-command verdicts.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

// manifestHead is a manifest's header and spec up to its description. A test
// appends the spec lines it is about, indented two spaces.
const manifestHead = `apiVersion: mitstack/v1alpha
kind: Block
metadata:
  id: reads-consumer
  name: Reads Consumer
  owner: blockverify-tests
spec:
  layer: backend
  version: 1.0.0
  description: A block for the manifest suite.
`

// manifestDir writes content as manifest.yaml into a fresh directory and
// returns the directory.
func manifestDir(t *testing.T, content string) string {
	t.Helper()
	return newFixtureFrom(t, map[string]string{"manifest.yaml": content})
}

// baseManifestFile writes content as a base-branch manifest and returns its
// path.
func baseManifestFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "base-manifest.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// withVersion returns manifestHead with spec.version set to v.
func withVersion(v string) string {
	return strings.Replace(manifestHead, "version: 1.0.0", "version: "+v, 1)
}

func requireManifestPass(t *testing.T, got SuiteResult) {
	t.Helper()
	if got.Name != "manifest" {
		t.Errorf("suite name = %q, want %q", got.Name, "manifest")
	}
	if got.Status != "pass" {
		t.Errorf("manifest status = %q, want %q; detail: %s", got.Status, "pass", got.Detail)
	}
}

func requireManifestFail(t *testing.T, got SuiteResult, wants ...string) {
	t.Helper()
	if got.Name != "manifest" {
		t.Errorf("suite name = %q, want %q", got.Name, "manifest")
	}
	if got.Status != "fail" {
		t.Errorf("manifest status = %q, want %q; detail: %s", got.Status, "fail", got.Detail)
		return
	}
	for _, want := range wants {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("manifest detail = %q, want it to contain %q", got.Detail, want)
		}
	}
}

// --- presence -------------------------------------------------------------

func TestManifestSuite_NoManifestIsSkipped(t *testing.T) {
	got := manifestSuite(t.TempDir(), "")
	if got.Name != "manifest" {
		t.Errorf("suite name = %q, want %q", got.Name, "manifest")
	}
	if got.Status != "skipped" {
		t.Errorf("manifest status = %q, want %q with no manifest.yaml (trust-plumbing fails that); detail: %s",
			got.Status, "skipped", got.Detail)
	}
	if got.Detail == "" {
		t.Error("a skipped manifest suite must say why in detail")
	}
}

// --- reads ----------------------------------------------------------------

func TestManifestSuite_ABlockThatDeclaresNoReadsPasses(t *testing.T) {
	cases := map[string]string{
		"the golden fixture's requires: []": manifestYAML,
		"no requires at all":                manifestHead,
		"requirements with no reads": manifestHead + `  requires:
    - capability: database
      hint: a postgres database
    - capability: no-registry-entry-lists-this
`,
	}
	for label, content := range cases {
		t.Run(label, func(t *testing.T) {
			requireManifestPass(t, manifestSuite(manifestDir(t, content), ""))
		})
	}
}

func TestManifestSuite_ReadsTheRegistryGivesTheirCapabilityPass(t *testing.T) {
	cases := map[string]string{
		"flow list": `  requires:
    - capability: database
      reads: [appName, dbName]
`,
		"block list naming an alias": `  requires:
    - capability: app-host
      reads:
        - region
`,
		"reads before capability": `  requires:
    - reads: [host, port]
      capability: database
`,
		"quoted names and comments": `  requires:
    # the database this block connects to
    - capability: database # postgres
      reads: ["dbName", 'username'] # both from the database
`,
		"a name two capabilities give": `  requires:
    - capability: aws-topology
      reads:
        - appHostname
`,
		"one requirement with reads beside one without": `  requires:
    - capability: github
    - capability: llm
      reads: [llmEndpoint, model]
    - capability: database
`,
	}
	for label, spec := range cases {
		t.Run(label, func(t *testing.T) {
			requireManifestPass(t, manifestSuite(manifestDir(t, manifestHead+spec), ""))
		})
	}
}

func TestManifestSuite_AReadTheRegistryDoesNotHoldFails(t *testing.T) {
	dir := manifestDir(t, manifestHead+`  requires:
    - capability: database
      reads: [appName, dbNme]
`)
	requireManifestFail(t, manifestSuite(dir, ""),
		`spec.requires[database].reads: "dbNme" is not in the resource registry`,
		`"dbName"`)
}

func TestManifestSuite_AReadItsCapabilityDoesNotGiveFails(t *testing.T) {
	cases := []struct {
		label, spec, want string
	}{
		{
			label: "database reads appHostname",
			spec: `  requires:
    - capability: database
      reads: [appHostname]
`,
			want: `spec.requires[database].reads: "appHostname" is not an output of capability "database"`,
		},
		{
			label: "database reads an alias of a host entry",
			spec: `  requires:
    - capability: database
      reads:
        - instanceType
`,
			want: `spec.requires[database].reads: "instanceType" is not an output of capability "database"`,
		},
		{
			label: "the offending requirement follows a valid one",
			spec: `  requires:
    - capability: app-host
      reads: [appHostname]
    - capability: llm
      reads: [dbName]
`,
			want: `spec.requires[llm].reads: "dbName" is not an output of capability "llm"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			requireManifestFail(t, manifestSuite(manifestDir(t, manifestHead+tc.spec), ""), tc.want)
		})
	}
}

func TestManifestSuite_AnEmptyReadNameFails(t *testing.T) {
	dir := manifestDir(t, manifestHead+`  requires:
    - capability: database
      reads: [appName, ""]
`)
	requireManifestFail(t, manifestSuite(dir, ""), `spec.requires[database].reads: a name must not be empty`)
}

// --- outputs --------------------------------------------------------------

func TestManifestSuite_OutputsTheRegistryHoldsPass(t *testing.T) {
	cases := map[string]string{
		"block list with dotted names and an alias": `  outputs:
    - dbName
    - host.type
    - region
`,
		"flow list": `  outputs: [host, port, username]
`,
		"empty flow list": `  outputs: []
`,
	}
	for label, spec := range cases {
		t.Run(label, func(t *testing.T) {
			requireManifestPass(t, manifestSuite(manifestDir(t, manifestHead+spec), ""))
		})
	}
}

// The nearest registered name is the smallest edit distance over entry names
// and aliases, ties broken alphabetically: hostnme is 3 from both host and
// hostId, runsOnP is 1 from both runsOn and runsOnPr, and regon is nearest
// the alias region.
func TestManifestSuite_AnOutputOutsideTheRegistryFailsNamingTheNearestName(t *testing.T) {
	cases := []struct {
		label, spec, output, nearest, notNearest string
	}{
		{"block list, tie broken alphabetically", "  outputs:\n    - url\n    - hostnme\n", "hostnme", "host", "hostId"},
		{"flow list, tie broken alphabetically", "  outputs: [runsOnP]\n", "runsOnP", "runsOn", "runsOnPr"},
		{"nearest is an alias", "  outputs:\n    - regon\n", "regon", "region", "repo"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got := manifestSuite(manifestDir(t, manifestHead+tc.spec), "")
			requireManifestFail(t, got, "spec.outputs", `"`+tc.output+`"`, `"`+tc.nearest+`"`)
			if strings.Contains(got.Detail, `"`+tc.notNearest+`"`) {
				t.Errorf("manifest detail = %q names %q, want only the nearest name %q", got.Detail, tc.notNearest, tc.nearest)
			}
		})
	}
}

// --- release class and note on a version change ---------------------------

func TestManifestSuite_UnchangedVersionPassesWithoutAReleaseClassOrNote(t *testing.T) {
	base := baseManifestFile(t, withVersion("1.0.0"))
	requireManifestPass(t, manifestSuite(manifestDir(t, withVersion("1.0.0")), base))
}

func TestManifestSuite_ChangedVersionWithAReleaseClassAndNotePasses(t *testing.T) {
	cases := map[string]string{
		"plain note": `  releaseClass: feature
  releaseNote: Adds the reads declaration.
`,
		"folded note": `  releaseClass: correctness-fix
  releaseNote: >
    Fixes the retry that dropped the second
    attempt's outputs.
  requires: []
`,
		"literal note": `  releaseNote: |
    Renames the output.
  releaseClass: breaking
`,
	}
	base := baseManifestFile(t, withVersion("1.0.0"))
	for label, spec := range cases {
		t.Run(label, func(t *testing.T) {
			requireManifestPass(t, manifestSuite(manifestDir(t, withVersion("1.1.0")+spec), base))
		})
	}
}

func TestManifestSuite_ChangedVersionWithNoReleaseClassFails(t *testing.T) {
	base := baseManifestFile(t, withVersion("1.0.0"))
	target := withVersion("1.1.0") + "  releaseNote: Adds the reads declaration.\n"
	requireManifestFail(t, manifestSuite(manifestDir(t, target), base), "spec.releaseClass")
}

func TestManifestSuite_ChangedVersionWithNoReleaseNoteFails(t *testing.T) {
	cases := map[string]string{
		"no note":           "  releaseClass: feature\n",
		"blank note":        "  releaseClass: feature\n  releaseNote: \"   \"\n",
		"empty folded note": "  releaseClass: feature\n  releaseNote: >\n  requires: []\n",
	}
	base := baseManifestFile(t, withVersion("1.0.0"))
	for label, spec := range cases {
		t.Run(label, func(t *testing.T) {
			requireManifestFail(t, manifestSuite(manifestDir(t, withVersion("1.1.0")+spec), base), "spec.releaseNote")
		})
	}
}

func TestManifestSuite_ChangedVersionWithNeitherNamesBoth(t *testing.T) {
	base := baseManifestFile(t, withVersion("1.0.0"))
	requireManifestFail(t, manifestSuite(manifestDir(t, withVersion("2.0.0")), base),
		"spec.releaseClass", "spec.releaseNote")
}

// A block new on this branch has no base manifest to compare against.
func TestManifestSuite_BaseManifestAbsentPasses(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "base-manifest.yaml")
	requireManifestPass(t, manifestSuite(manifestDir(t, withVersion("1.1.0")), missing))
}

func TestManifestSuite_NoBaseManifestFlagComparesNoVersion(t *testing.T) {
	requireManifestPass(t, manifestSuite(manifestDir(t, withVersion("1.1.0")), ""))
}

// A base manifest that is present but cannot be read is not an absent one.
func TestManifestSuite_UnreadableBaseManifestFailsNamingIt(t *testing.T) {
	base := filepath.Join(t.TempDir(), "base-manifest.yaml")
	if err := os.Mkdir(base, 0o755); err != nil {
		t.Fatal(err)
	}
	requireManifestFail(t, manifestSuite(manifestDir(t, withVersion("1.1.0")), base), base)
}

// --- metadata.id ----------------------------------------------------------

// withID returns manifestHead with its metadata.id line replaced by idLine,
// written as it appears after "id:", quotes included.
func withID(idLine string) string {
	return strings.Replace(manifestHead, "  id: reads-consumer\n", "  id: "+idLine+"\n", 1)
}

func TestManifestSuite_ACommunityOrPrivateMetadataIDPasses(t *testing.T) {
	cases := map[string]string{
		"bare slug":                 "billing-db",
		"double-quoted private id":  `"@acme/billing-db"`,
		"single-quoted private id":  `'@acme/billing-db'`,
		"private id with a comment": `"@org-a/api" # the org's own api block`,
		"forty-character bare slug": "a" + strings.Repeat("b", 38) + "c",
	}
	for label, idLine := range cases {
		t.Run(label, func(t *testing.T) {
			requireManifestPass(t, manifestSuite(manifestDir(t, withID(idLine)), ""))
		})
	}
}

func TestManifestSuite_AMetadataIDOutsideBothFormsFailsNamingIt(t *testing.T) {
	cases := map[string]string{
		"uppercase namespace":     `"@Acme/x"`,
		"namespace with no name":  `"@acme/"`,
		"namespace without its @": "acme/x",
		"two slashes":             `"@acme/x/y"`,
		"uppercase bare id":       "Billing-DB",
		"trailing dash":           "billing-",
		"41-character bare id":    "a" + strings.Repeat("b", 39) + "c",
	}
	for label, idLine := range cases {
		t.Run(label, func(t *testing.T) {
			requireManifestFail(t, manifestSuite(manifestDir(t, withID(idLine)), ""),
				"manifest.yaml", "metadata.id", "@<org>/")
		})
	}
}

// YAML reserves @ and ` at the start of a plain value, so a parser such as
// the platform's refuses a private id written without quotes. The suite fails
// it too, and says to quote it, rather than passing a manifest the platform
// will not read.
func TestManifestSuite_AnUnquotedPrivateIDFailsAskingForQuotes(t *testing.T) {
	for label, idLine := range map[string]string{
		"unquoted private id":          "@acme/billing-db",
		"unquoted private id, comment": "@acme/billing-db # the billing database",
		"backtick-led id":              "`billing-db`",
	} {
		t.Run(label, func(t *testing.T) {
			requireManifestFail(t, manifestSuite(manifestDir(t, withID(idLine)), ""),
				"manifest.yaml", "metadata.id", "quote")
		})
	}
}

// A bad metadata.id is reported beside the spec's own problems, so one run
// shows every defect.
func TestManifestSuite_ABadMetadataIDIsReportedBesideASpecProblem(t *testing.T) {
	content := withID(`"@Acme/x"`) + "  outputs: [hostnme]\n"
	requireManifestFail(t, manifestSuite(manifestDir(t, content), ""),
		"metadata.id", "@<org>/", `spec.outputs: "hostnme"`)
}

// A manifest with no metadata.id earns the verdict its spec earns.
func TestManifestSuite_NoMetadataIDKeepsTheSpecVerdict(t *testing.T) {
	noID := strings.Replace(manifestHead, "  id: reads-consumer\n", "", 1)
	noMetadata := strings.Replace(manifestHead,
		"metadata:\n  id: reads-consumer\n  name: Reads Consumer\n  owner: blockverify-tests\n", "", 1)
	if noID == manifestHead || noMetadata == manifestHead {
		t.Fatal("manifestHead changed shape; the metadata.id fixtures no longer remove the id")
	}

	for label, content := range map[string]string{
		"metadata without an id": noID,
		"no metadata at all":     noMetadata,
	} {
		t.Run(label+" passes with a valid spec", func(t *testing.T) {
			requireManifestPass(t, manifestSuite(manifestDir(t, content), ""))
		})
		t.Run(label+" fails with a bad spec", func(t *testing.T) {
			got := manifestSuite(manifestDir(t, content+"  outputs: [hostnme]\n"), "")
			requireManifestFail(t, got, `spec.outputs: "hostnme"`)
			if strings.Contains(got.Detail, "metadata.id") {
				t.Errorf("manifest detail = %q names metadata.id, want no id problem when the id is absent", got.Detail)
			}
		})
	}
}

// metadata the reader cannot follow could hide a bad id, so it fails as an
// unfollowable spec does, never passing on an id the suite could not read.
func TestManifestSuite_AMetadataTheReaderCannotFollowIsAFailure(t *testing.T) {
	head := "apiVersion: mitstack/v1alpha\nkind: Block\n"
	spec := "spec:\n  layer: backend\n  version: 1.0.0\n"
	cases := map[string]string{
		"metadata is a flow mapping": head + "metadata: {id: \"@Acme/x\", owner: blockverify-tests}\n" + spec,
		"metadata.id is an alias":    head + "metadata:\n  name: &bad \"@Acme/x\"\n  id: *bad\n" + spec,
		"metadata.id is a mapping":   head + "metadata:\n  id:\n    org: Acme\n" + spec,
	}
	for label, content := range cases {
		t.Run(label, func(t *testing.T) {
			requireManifestFail(t, manifestSuite(manifestDir(t, content), ""), "manifest.yaml", "metadata")
		})
	}
}

// --- spellings and unknown fields -----------------------------------------

// typeBlockSpellingManifest is the fixture block written with type: block, a
// providedContracts entry typed by format, a continuity unit typed by store,
// and no spec.layer, declaring reads and outputs.
const typeBlockSpellingManifest = `apiVersion: mitstack/v1alpha
type: block
metadata:
  id: blockverify-fixture
  name: blockverify fixture block
  owner: blockverify-tests
spec:
  version: 1.0.0
  description: >
    The blockverify fixture block, written with type, format and store.
  provides:
    - blockverify-fixture-capability
  providedContracts:
    - capability: blockverify-fixture-capability
      format: config
  continuity:
    - name: fixture-state
      store: volume
      stateful: false
  requires:
    - capability: database
      reads: [host, port, dbName]
  outputs:
    - url
    - region
`

// kindSpellingManifest is the same block written with kind: Block, kind on
// the providedContracts entry and the continuity unit, and a layer.
const kindSpellingManifest = `apiVersion: mitstack/v1alpha
kind: Block
metadata:
  id: blockverify-fixture
  name: blockverify fixture block
  owner: blockverify-tests
spec:
  layer: backend
  version: 1.0.0
  description: >
    The blockverify fixture block, written with kind in all three places.
  provides:
    - blockverify-fixture-capability
  providedContracts:
    - capability: blockverify-fixture-capability
      kind: config
  continuity:
    - name: fixture-state
      kind: volume
      stateful: false
  requires:
    - capability: database
      reads: [host, port, dbName]
  outputs:
    - url
    - region
`

func TestManifestSuite_BothManifestSpellingsPass(t *testing.T) {
	for label, content := range map[string]string{
		"type, format and store": typeBlockSpellingManifest,
		"kind in all three":      kindSpellingManifest,
	} {
		t.Run(label, func(t *testing.T) {
			requireManifestPass(t, manifestSuite(manifestDir(t, content), ""))
		})
	}
}

// unknownSpecFields are spec lines carrying a field no manifest field
// names, at several depths, by the path the suite must name.
var unknownSpecFields = map[string]string{
	"spec.frobnicate":    "  frobnicate: true\n",
	"spec.futureSection": "  futureSection:\n    outputs: not-the-outputs\n    reads: [not, the, reads]\n",
	"spec.requires[0].frobnicate": "  requires:\n    - capability: database\n      reads: [host]\n" +
		"      frobnicate: true\n",
	"spec.lifecycle.frobnicate": "  lifecycle:\n    contract: kit/v1\n    frobnicate: true\n",
}

// A manifest carrying a spec field no manifest field names fails the suite,
// and the detail names the manifest, the field's path and the parser's
// message.
func TestManifestSuite_AnUnknownSpecFieldFailsNamingIt(t *testing.T) {
	for path, spec := range unknownSpecFields {
		t.Run(path, func(t *testing.T) {
			content := manifestHead + spec
			_, perr := contract.ParseManifest([]byte(content))
			if perr == nil {
				t.Fatalf("contract.ParseManifest accepts the manifest, which must be refused naming %s:\n%s", path, content)
			}
			requireManifestFail(t, manifestSuite(manifestDir(t, content), ""), "manifest.yaml", path, perr.Error())
		})
	}
}

func TestManifestSuite_PassesAnOldFormManifestWithDependsOnAndProvidedSlugs(t *testing.T) {
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

// A manifest carrying the retired spec.dependsOn still passes, and the
// suite prints the parser's warning, naming spec.dependsOn.
func TestManifestSuite_PassesAManifestWithDependsOnAndPrintsItsWarning(t *testing.T) {
	spec := `  dependsOn:
    - weather-app
  requires:
    - capability: repo
      reads: [repo]
`
	got := manifestSuite(manifestDir(t, manifestHead+spec), "")
	requireManifestPass(t, got)
	if !strings.Contains(got.Detail, "spec.dependsOn") {
		t.Errorf("manifest detail = %q, want it to print the warning naming %q", got.Detail, "spec.dependsOn")
	}
}

// --- a manifest the reader cannot follow ----------------------------------

func TestManifestSuite_AManifestTheReaderCannotFollowIsAFailure(t *testing.T) {
	cases := map[string]string{
		"spec is a flow mapping":          "apiVersion: mitstack/v1alpha\nkind: Block\nspec: {version: 1.0.0, outputs: [nope]}\n",
		"spec.outputs is a flow mapping":  manifestHead + "  outputs: {first: nope}\n",
		"spec.outputs is an alias":        manifestHead + "  tags: &outs [nope]\n  outputs: *outs\n",
		"spec.requires is a flow mapping": manifestHead + "  requires: {capability: database, reads: [nope]}\n",
		"spec.requires is an alias":       manifestHead + "  resources: &reqs\n    - name: database\n  requires: *reqs\n",
	}
	for label, content := range cases {
		t.Run(label, func(t *testing.T) {
			requireManifestFail(t, manifestSuite(manifestDir(t, content), ""), "manifest.yaml")
		})
	}

	t.Run("manifest.yaml is a directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "manifest.yaml"), 0o755); err != nil {
			t.Fatal(err)
		}
		requireManifestFail(t, manifestSuite(dir, ""), "manifest.yaml")
	})
}

// --- the whole command ----------------------------------------------------

func TestRun_BothManifestSpellingsPassBlockverify(t *testing.T) {
	for label, content := range map[string]string{
		"type, format and store": typeBlockSpellingManifest,
		"kind in all three":      kindSpellingManifest,
	} {
		t.Run(label, func(t *testing.T) {
			files := goldenFixtureFiles(t)
			files["manifest.yaml"] = content
			stdout, exit := runBlockverify(t, "--json", "--standalone", newFixtureFrom(t, files))
			r := decodeReport(t, stdout)
			if exit != 0 || r.Verdict != "pass" {
				t.Errorf("exit %d, verdict %q; want 0 and pass\nstdout:\n%s", exit, r.Verdict, stdout)
			}
			if s := suiteByName(t, r, "manifest"); s.Status != "pass" {
				t.Errorf("manifest status = %q, want %q; detail: %s", s.Status, "pass", s.Detail)
			}
		})
	}
}

// blockverify fails a manifest carrying an unknown spec field, and its
// output names the field: every suite that reads the manifest fails with
// the parser's message, the verdict is fail, and the human-readable report
// names the field too.
func TestRun_AnUnknownSpecFieldFailsBlockverifyNamingIt(t *testing.T) {
	const field = "spec.frobnicate"
	content := manifestYAML + unknownSpecFields[field]
	_, perr := contract.ParseManifest([]byte(content))
	if perr == nil {
		t.Fatalf("contract.ParseManifest accepts the manifest, which must be refused naming %s:\n%s", field, content)
	}
	files := goldenFixtureFiles(t)
	files["manifest.yaml"] = content
	dir := newFixtureFrom(t, files)

	stdout, exit := runBlockverify(t, "--json", "--standalone", dir)
	r := decodeReport(t, stdout)
	if exit != 1 || r.Verdict != "fail" {
		t.Errorf("exit %d, verdict %q; want 1 and fail for a manifest carrying %s\nstdout:\n%s", exit, r.Verdict, field, stdout)
	}
	for _, name := range []string{"trust-plumbing", "contract", "manifest", "naming"} {
		s := suiteByName(t, r, name)
		if s.Status != "fail" {
			t.Errorf("%s status = %q, want %q: a manifest the parser refuses fails every suite that reads it; detail: %s",
				name, s.Status, "fail", s.Detail)
			continue
		}
		for _, want := range []string{field, perr.Error()} {
			if !strings.Contains(s.Detail, want) {
				t.Errorf("%s detail = %q, want it to contain %q", name, s.Detail, want)
			}
		}
	}

	human, exit := runBlockverify(t, "--standalone", dir)
	if exit != 1 {
		t.Errorf("exit = %d without --json, want 1\nstdout:\n%s", exit, human)
	}
	if !strings.Contains(human, field) {
		t.Errorf("the human-readable report does not name %s:\n%s", field, human)
	}
}

// A metadata.id outside both forms fails the whole command through the
// manifest suite, and a private id passes it.
func TestRun_MetadataIDIsJudgedByTheManifestSuite(t *testing.T) {
	cases := []struct {
		label, idLine, status string
		exit                  int
	}{
		{"private id passes", `"@acme/billing-db"`, "pass", 0},
		{"uppercase namespace fails", `"@Acme/x"`, "fail", 1},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			files := goldenFixtureFiles(t)
			content := strings.Replace(manifestYAML, "  id: blockverify-fixture\n", "  id: "+tc.idLine+"\n", 1)
			if content == manifestYAML {
				t.Fatal("manifestYAML changed shape; the fixture no longer carries id: blockverify-fixture")
			}
			files["manifest.yaml"] = content
			stdout, exit := runBlockverify(t, "--json", "--standalone", newFixtureFrom(t, files))
			if exit != tc.exit {
				t.Errorf("exit = %d, want %d\nstdout:\n%s", exit, tc.exit, stdout)
			}
			r := decodeReport(t, stdout)
			if s := suiteByName(t, r, "manifest"); s.Status != tc.status {
				t.Errorf("manifest status = %q, want %q; detail: %s", s.Status, tc.status, s.Detail)
			} else if tc.status == "fail" && !strings.Contains(s.Detail, "metadata.id") {
				t.Errorf("manifest detail = %q, want it to name metadata.id", s.Detail)
			}
		})
	}
}

// --base-manifest takes the next argument as its path, in any position, and
// never as the target: the target stays judged (trust-plumbing passes) and
// the manifest suite compares against the base's version.
func TestRun_BaseManifestFlagNamesTheBaseAndNotTheTarget(t *testing.T) {
	base := baseManifestFile(t, strings.Replace(manifestYAML, "version: 1.0.0", "version: 0.9.0", 1))
	dir := newGoldenFixture(t)

	for label, args := range map[string][]string{
		"flag after the target":  {"--json", "--standalone", dir, "--base-manifest", base},
		"flag before the target": {"--base-manifest", base, "--json", "--standalone", dir},
	} {
		t.Run(label, func(t *testing.T) {
			stdout, exit := runBlockverify(t, args...)
			if exit != 1 {
				t.Errorf("exit = %d, want 1: the version moved from 0.9.0 with no release class\nstdout:\n%s", exit, stdout)
			}
			r := decodeReport(t, stdout)
			if tp := suiteByName(t, r, "trust-plumbing"); tp.Status != "pass" {
				t.Errorf("trust-plumbing status = %q, want %q: the base path must not be taken for the target; detail: %s",
					tp.Status, "pass", tp.Detail)
			}
			m := suiteByName(t, r, "manifest")
			if m.Status != "fail" || !strings.Contains(m.Detail, "spec.releaseClass") {
				t.Errorf("manifest = %q %q, want fail naming spec.releaseClass", m.Status, m.Detail)
			}
		})
	}
}

func TestRun_BaseManifestFlagWithNoValueIsAUsageError(t *testing.T) {
	dir := newGoldenFixture(t)
	stdout, exit := runBlockverify(t, "--json", "--standalone", dir, "--base-manifest")
	if exit != 2 {
		t.Errorf("exit = %d, want 2 for --base-manifest with no path\nstdout:\n%s", exit, stdout)
	}
}

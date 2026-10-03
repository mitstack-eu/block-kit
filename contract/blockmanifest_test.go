package contract_test

// blockmanifest_test.go pins contract.ParseManifest, the one manifest parser
// the kit ships. It switches on apiVersion: under mitstack/v1beta it reads
// the whole manifest strictly into BlockManifest, and under mitstack/v1alpha
// it reads the whole manifest strictly into V1AlphaManifest. The two worked
// manifests of ADR 0083 are read from the embedded manifest goldens and
// checked field by field.

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

// readManifestGolden returns the embedded manifest golden called name.
func readManifestGolden(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := contract.ManifestGoldens.ReadFile(contract.ManifestGoldensDir + "/" + name)
	if err != nil {
		t.Fatalf("reading %s from contract.ManifestGoldens: %v", name, err)
	}
	return raw
}

// scalar and inputs build the expected InputValue of a resource input.
func scalar(s string) contract.InputValue { return contract.InputValue{Scalar: s} }

func inputs(m map[string]contract.InputValue) contract.InputValue {
	return contract.InputValue{Map: m}
}

// sameInputs reports whether got and want hold the same inputs, nested
// mappings included. A nil and an empty mapping are the same.
func sameInputs(got, want map[string]contract.InputValue) bool {
	if len(got) != len(want) {
		return false
	}
	for k, w := range want {
		g, ok := got[k]
		if !ok || g.Scalar != w.Scalar || !sameInputs(g.Map, w.Map) {
			return false
		}
	}
	return true
}

type wantWorkedManifest struct {
	file         string
	id           string
	version      string
	releaseClass string
	releaseNote  string
	// rest is the manifest with Spec.Requires and Spec.Resources left out
	// and Spec.Description written without its trailing line break; those
	// are compared on their own.
	rest      contract.BlockManifest
	requires  []contract.ManifestRequirement
	resources []contract.ManifestResource
}

var workedManifests = []wantWorkedManifest{
	{
		file:         "plain-v1beta.yaml",
		id:           "plain",
		version:      "3.0.0",
		releaseClass: "breaking",
		releaseNote:  "Plain runs on any host and takes its database from whichever block offers one.",
		rest: contract.BlockManifest{
			APIVersion: "mitstack/v1beta",
			Type:       "block",
			Metadata:   contract.ManifestMetadata{ID: "plain", Name: "Plain Tracker", Owner: "web-app-platform"},
			Spec: contract.ManifestSpec{
				Version:      "3.0.0",
				ReleaseClass: "breaking",
				ReleaseNote:  "Plain runs on any host and takes its database from whichever block offers one.",
				Description:  "Runs the Plain tracker container on the app's host, connected to the app's Postgres database.",
				Params: []contract.ManifestParam{
					{Name: "image", Label: "Plain tracker container image", Required: true},
				},
				Provides: []string{"tracker"},
				Lifecycle: contract.ManifestLifecycle{
					Endpoint:   "https://api.mitstack.dev/operators/plain/hook",
					Operations: []string{"subscribe", "unsubscribe", "upgrade", "validate"},
					Contract:   "kit/v1",
				},
			},
		},
		requires: []contract.ManifestRequirement{
			{Kind: "host"},
			{Kind: "postgres-database"},
		},
		resources: []contract.ManifestResource{
			{
				Name: "tracker",
				Type: "host.container",
				On:   "requires.host",
				Inputs: map[string]contract.InputValue{
					"image": scalar("params.image"),
					"port":  scalar("8080"),
					"env": inputs(map[string]contract.InputValue{
						"TRACKER_DB_HOST":     scalar("requires.postgres-database.outputs.host"),
						"TRACKER_DB_PORT":     scalar("requires.postgres-database.outputs.port"),
						"TRACKER_DB_NAME":     scalar("requires.postgres-database.outputs.dbName"),
						"TRACKER_DB_USER":     scalar("requires.postgres-database.outputs.username"),
						"TRACKER_DB_PASSWORD": scalar("requires.postgres-database.outputs.password"),
					}),
				},
			},
		},
	},
	{
		file:         "aws-app-infra-v1beta.yaml",
		id:           "aws-app-infra",
		version:      "2.0.0",
		releaseClass: "breaking",
		releaseNote:  "AWS App Infrastructure offers its host, data volume, registry, deploy role and address as separate resources.",
		rest: contract.BlockManifest{
			APIVersion: "mitstack/v1beta",
			Type:       "block",
			Metadata:   contract.ManifestMetadata{ID: "aws-app-infra", Name: "AWS App Infrastructure"},
			Spec: contract.ManifestSpec{
				Version:      "2.0.0",
				ReleaseClass: "breaking",
				ReleaseNote:  "AWS App Infrastructure offers its host, data volume, registry, deploy role and address as separate resources.",
				Description:  "Creates the app's host, data volume and image registry in the consumer's own AWS account, and the app's public address.",
				Params: []contract.ManifestParam{
					{Name: "instanceType", Label: "EC2 instance type"},
					{Name: "domain", Label: "Your own domain pointed at the app"},
					{Name: "customDomain", Label: "Extra hostname served alongside the app's address"},
				},
				Provides: []string{"app-instance", "app-data", "image-repo", "deploy-role", "app-hostname"},
				Lifecycle: contract.ManifestLifecycle{
					Endpoint:   "https://api.mitstack.dev/operators/aws-app-infra/hook",
					Operations: []string{"subscribe", "unsubscribe", "upgrade", "validate"},
					Contract:   "kit/v1",
				},
			},
		},
		requires: []contract.ManifestRequirement{
			{Kind: "git-repository", In: []string{"github"}},
		},
		resources: []contract.ManifestResource{
			{Name: "fallback-network", Type: "aws.vpc"},
			{Name: "app-security-group", Type: "aws.security-group", Inputs: map[string]contract.InputValue{
				"network": scalar("resources.fallback-network"),
			}},
			{Name: "app-instance", Type: "aws.ec2-instance", Inputs: map[string]contract.InputValue{
				"namePrefix":    scalar("app.name"),
				"instanceType":  scalar("params.instanceType"),
				"securityGroup": scalar("resources.app-security-group"),
			}},
			{Name: "app-data", Type: "aws.ebs-volume", Inputs: map[string]contract.InputValue{
				"attachTo": scalar("resources.app-instance"),
			}},
			{Name: "image-repo", Type: "aws.ecr-repository", Inputs: map[string]contract.InputValue{
				"namePrefix": scalar("app.name"),
			}},
			{Name: "ecr-push-role", Type: "aws.iam-role", Inputs: map[string]contract.InputValue{
				"trustedRepository": scalar("requires.git-repository.outputs.repo"),
			}},
			{Name: "deploy-role", Type: "aws.iam-role"},
			{Name: "egress-proxy", Type: "aws.egress-proxy", Inputs: map[string]contract.InputValue{
				"network": scalar("resources.fallback-network"),
			}},
			{Name: "box-configuration", Type: "aws.ssm-association", Inputs: map[string]contract.InputValue{
				"host":         scalar("resources.app-instance"),
				"dataVolume":   scalar("resources.app-data"),
				"domain":       scalar("params.domain"),
				"customDomain": scalar("params.customDomain"),
			}},
			{Name: "app-hostname", Type: "aws.route53-record", Inputs: map[string]contract.InputValue{
				"target": scalar("resources.app-instance.outputs.publicIp"),
			}},
		},
	},
}

func TestManifestAPIVersions_AreV1AlphaAndV1Beta(t *testing.T) {
	if contract.ManifestAPIVersionV1Alpha != "mitstack/v1alpha" {
		t.Errorf("contract.ManifestAPIVersionV1Alpha = %q, want %q", contract.ManifestAPIVersionV1Alpha, "mitstack/v1alpha")
	}
	if contract.ManifestAPIVersion != "mitstack/v1beta" {
		t.Errorf("contract.ManifestAPIVersion = %q, want %q", contract.ManifestAPIVersion, "mitstack/v1beta")
	}
}

// TestParseManifest_ReadsTheWorkedV1BetaManifests reads ADR 0083's worked
// plain and aws-app-infra manifests: every resource's name, type, inputs and
// on, in manifest order, every requirement's kind, in, name and many, and
// the params, provides and lifecycle beside them.
func TestParseManifest_ReadsTheWorkedV1BetaManifests(t *testing.T) {
	for _, w := range workedManifests {
		t.Run(w.file, func(t *testing.T) {
			m, err := contract.ParseManifest(readManifestGolden(t, w.file))
			if err != nil {
				t.Fatalf("ParseManifest(%s): %v", w.file, err)
			}
			if m.APIVersion != contract.ManifestAPIVersion {
				t.Errorf("APIVersion = %q, want %q", m.APIVersion, contract.ManifestAPIVersion)
			}
			if m.V1Alpha != nil {
				t.Errorf("V1Alpha = %+v, want nil under %s", *m.V1Alpha, contract.ManifestAPIVersion)
			}
			if m.Block == nil {
				t.Fatalf("Block is nil under %s", contract.ManifestAPIVersion)
			}
			if m.ID() != w.id || m.Version() != w.version || m.ReleaseClass() != w.releaseClass || m.ReleaseNote() != w.releaseNote {
				t.Errorf("ID, Version, ReleaseClass, ReleaseNote = %q, %q, %q, %q; want %q, %q, %q, %q",
					m.ID(), m.Version(), m.ReleaseClass(), m.ReleaseNote(), w.id, w.version, w.releaseClass, w.releaseNote)
			}

			got := *m.Block
			gotRequires, gotResources := got.Spec.Requires, got.Spec.Resources
			got.Spec.Requires, got.Spec.Resources = nil, nil
			got.Spec.Description = strings.TrimRight(got.Spec.Description, "\n")
			if !reflect.DeepEqual(got, w.rest) {
				t.Errorf("manifest outside requires and resources =\n%+v\nwant\n%+v", got, w.rest)
			}

			if len(gotRequires) != len(w.requires) {
				t.Fatalf("spec.requires has %d entries %+v, want %d %+v", len(gotRequires), gotRequires, len(w.requires), w.requires)
			}
			for i, want := range w.requires {
				g := gotRequires[i]
				if g.Kind != want.Kind || !slices.Equal(g.In, want.In) || g.Name != want.Name || g.Many != want.Many {
					t.Errorf("spec.requires[%d] = %+v, want %+v", i, g, want)
				}
			}

			if len(gotResources) != len(w.resources) {
				t.Fatalf("spec.resources has %d entries, want %d", len(gotResources), len(w.resources))
			}
			for i, want := range w.resources {
				g := gotResources[i]
				if g.Name != want.Name || g.Type != want.Type || g.On != want.On {
					t.Errorf("spec.resources[%d] name, type, on = %q, %q, %q; want %q, %q, %q",
						i, g.Name, g.Type, g.On, want.Name, want.Type, want.On)
				}
				if !sameInputs(g.Inputs, want.Inputs) {
					t.Errorf("spec.resources[%d] (%s) inputs = %+v, want %+v", i, want.Name, g.Inputs, want.Inputs)
				}
			}
		})
	}
}

// betaManifest is a small valid v1beta manifest the behaviour tests edit.
const betaManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: fixture-block
  name: Fixture Block
spec:
  version: 1.0.0
  params:
    - name: image
      label: Container image
  requires:
    - kind: host
  resources:
    - name: fallback-network
      type: aws.vpc
    - name: app-security-group
      type: aws.security-group
      inputs: { network: resources.fallback-network }
  provides: [app-security-group]
  lifecycle:
    endpoint: https://example.test/hook
    operations: [subscribe, unsubscribe]
    contract: kit/v1
`

// editBeta returns betaManifest with each old, new pair of replacements
// applied once. It fails the test when an old text is not in the manifest,
// so a fixture edit can never silently become a no-op.
func editBeta(t *testing.T, pairs ...string) []byte {
	t.Helper()
	out := betaManifest
	for i := 0; i+1 < len(pairs); i += 2 {
		if !strings.Contains(out, pairs[i]) {
			t.Fatalf("fixture edit: %q is not in the manifest", pairs[i])
		}
		out = strings.Replace(out, pairs[i], pairs[i+1], 1)
	}
	return []byte(out)
}

// parseBeta parses raw and returns its Block, failing the test when raw is
// refused or is not read as a v1beta manifest.
func parseBeta(t *testing.T, raw []byte) *contract.BlockManifest {
	t.Helper()
	m, err := contract.ParseManifest(raw)
	if err != nil {
		t.Fatalf("ParseManifest: %v\n%s", err, raw)
	}
	if m.Block == nil {
		t.Fatalf("ParseManifest gave no Block for a %s manifest: %+v", contract.ManifestAPIVersion, m)
	}
	return m.Block
}

// requireRefusedNaming fails the test unless ParseManifest refuses raw with
// an error containing every one of wants.
func requireRefusedNaming(t *testing.T, raw []byte, wants ...string) {
	t.Helper()
	m, err := contract.ParseManifest(raw)
	if err == nil {
		t.Fatalf("ParseManifest accepted the manifest (%+v), want an error naming %q:\n%s", m, wants, raw)
	}
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ParseManifest error = %q, want it to name %q", err, want)
		}
	}
}

func TestParseManifest_V1BetaAcceptsTheBaseFixture(t *testing.T) {
	if b := parseBeta(t, []byte(betaManifest)); len(b.Spec.Resources) != 2 {
		t.Fatalf("betaManifest gave resources %+v, want two", b.Spec.Resources)
	}
}

// The keys ADR 0083 takes out of the manifest stay out because v1beta is
// strict: a key the types do not name is refused, naming its path.
func TestParseManifest_V1BetaRefusesAnUnknownKeyNamingItsPath(t *testing.T) {
	cases := []struct {
		path  string
		edits []string
	}{
		{"metadata.labels", []string{"  name: Fixture Block\n", "  name: Fixture Block\n  labels: none\n"}},
		{"spec.outputs", []string{"  requires:\n", "  outputs: [host]\n  requires:\n"}},
		{"spec.providedContracts", []string{"  provides: [app-security-group]\n",
			"  provides: [app-security-group]\n  providedContracts:\n    - capability: app-host\n      format: config\n"}},
		{"spec.dependsOn", []string{"  requires:\n", "  dependsOn:\n    - weather-app\n  requires:\n"}},
		{"spec.params[0].type", []string{"      label: Container image\n", "      label: Container image\n      type: string\n"}},
		{"spec.requires[0].capability", []string{"    - kind: host\n", "    - kind: host\n      capability: app-host\n"}},
		{"spec.requires[0].reads", []string{"    - kind: host\n", "    - kind: host\n      reads: [region]\n"}},
		{"spec.resources[1].ownership", []string{"      type: aws.security-group\n", "      type: aws.security-group\n      ownership: created\n"}},
		{"spec.lifecycle.upgradeStrategy", []string{"    contract: kit/v1\n", "    contract: kit/v1\n    upgradeStrategy: rolling\n"}},
		{"spec.observability.resources", []string{"  lifecycle:\n", "  observability:\n    resources:\n      - name: fallback-network\n  lifecycle:\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			requireRefusedNaming(t, editBeta(t, tc.edits...), tc.path)
		})
	}
}

func TestParseManifest_V1BetaRefusesAMissingRequiredFieldNamingItsPath(t *testing.T) {
	cases := []struct {
		label string
		wants []string
		edits []string
	}{
		{"type absent", []string{"type"}, []string{"type: block\n", ""}},
		{"type not block", []string{"type", "block"}, []string{"type: block\n", "type: component\n"}},
		{"metadata.id absent", []string{"metadata.id"}, []string{"  id: fixture-block\n", ""}},
		{"spec.version absent", []string{"spec.version"}, []string{"  version: 1.0.0\n", ""}},
		{"a requirement without a kind", []string{"spec.requires[0].kind"}, []string{"    - kind: host\n", "    - name: primary\n"}},
		{"a resource without a name", []string{"spec.resources[0].name"}, []string{
			"    - name: fallback-network\n      type: aws.vpc\n", "    - type: aws.vpc\n"}},
		{"a resource without a type", []string{"spec.resources[1].type"}, []string{"      type: aws.security-group\n", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			requireRefusedNaming(t, editBeta(t, tc.edits...), tc.wants...)
		})
	}
}

func TestParseManifest_V1BetaManyTakesTrueOrFalseOnly(t *testing.T) {
	for _, tc := range []struct {
		line string
		want bool
	}{
		{"", false},
		{"      many: true\n", true},
		{"      many: false\n", false},
	} {
		t.Run("accepts "+strings.TrimSpace(tc.line), func(t *testing.T) {
			b := parseBeta(t, editBeta(t, "    - kind: host\n", "    - kind: host\n"+tc.line))
			if got := b.Spec.Requires[0].Many; got != tc.want {
				t.Errorf("spec.requires[0].many = %v, want %v", got, tc.want)
			}
		})
	}
	for _, value := range []string{"yes", "1", `"true"`, "sometimes"} {
		t.Run("refuses "+value, func(t *testing.T) {
			requireRefusedNaming(t, editBeta(t, "    - kind: host\n", "    - kind: host\n      many: "+value+"\n"),
				"spec.requires[0].many")
		})
	}
}

func TestParseManifest_V1BetaRefusesADuplicateResourceName(t *testing.T) {
	raw := editBeta(t,
		"    - name: app-security-group\n", "    - name: fallback-network\n",
		"  provides: [app-security-group]\n", "  provides: [fallback-network]\n")
	requireRefusedNaming(t, raw, "fallback-network")
}

func TestParseManifest_V1BetaRefusesAProvidesEntryNamingNoResource(t *testing.T) {
	raw := editBeta(t, "  provides: [app-security-group]\n", "  provides: [app-security-group, no-such-resource]\n")
	requireRefusedNaming(t, raw, "spec.provides", "no-such-resource")
}

func TestParseManifest_V1BetaReadsInAsOneCapabilityOrAList(t *testing.T) {
	cases := []struct {
		label, lines string
		want         []string
	}{
		{"absent", "", nil},
		{"one capability", "      in: github\n", []string{"github"}},
		{"a flow list", "      in: [github, gitlab]\n", []string{"github", "gitlab"}},
		{"a block list", "      in:\n        - github\n        - gitlab\n", []string{"github", "gitlab"}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			b := parseBeta(t, editBeta(t, "    - kind: host\n", "    - kind: host\n"+tc.lines))
			if got := b.Spec.Requires[0].In; !slices.Equal(got, tc.want) {
				t.Errorf("spec.requires[0].in = %q, want %q", got, tc.want)
			}
		})
	}
}

// An alias or a merge key brings in text from elsewhere in the document,
// which the reader does not follow, so it refuses the manifest rather than
// guessing what it declares, and says which it found and where.
func TestParseManifest_RefusesAnAliasOrAMergeKey(t *testing.T) {
	parseBeta(t, []byte(betaManifest))
	cases := []struct {
		label string
		raw   []byte
		wants []string
	}{
		{"an alias as an input", editBeta(t,
			"    - name: fallback-network\n", "    - name: &net fallback-network\n",
			"inputs: { network: resources.fallback-network }", "inputs:\n        network: *net"),
			[]string{"spec.resources[1].inputs.network", "alias"}},
		{"a merge key in a resource", editBeta(t,
			"      type: aws.vpc\n", "      type: aws.vpc\n      inputs: &shared\n        zone: a\n",
			"      inputs: { network: resources.fallback-network }\n", "      inputs:\n        <<: *shared\n"),
			[]string{"spec.resources[1].inputs", "merge key"}},
		{"a merge key written as a flow mapping", editBeta(t,
			"      inputs: { network: resources.fallback-network }\n", "      inputs:\n        <<: { zone: a }\n"),
			[]string{"spec.resources[1].inputs", "merge key"}},
		{"an alias as a list", editBeta(t,
			"  provides: [app-security-group]\n", "  provides: &provided [app-security-group]\n",
			"    operations: [subscribe, unsubscribe]\n", "    operations: *provided\n"),
			[]string{"spec.lifecycle.operations", "alias"}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			requireRefusedNaming(t, tc.raw, tc.wants...)
		})
	}
}

// The parser accepts nothing manifest.schema.json refuses: a null value, a
// plain number or boolean where the schema wants a string, and an empty
// list where a block must declare at least one entry. Each refusal names
// its path.
func TestParseManifest_V1BetaRefusesWhatTheSchemaRefusesNamingItsPath(t *testing.T) {
	cases := []struct {
		label, path string
		edits       []string
	}{
		{"a null input", "spec.resources[1].inputs.network", []string{
			"inputs: { network: resources.fallback-network }", "inputs: { network: }"}},
		{"a null optional field", "metadata.owner", []string{
			"  name: Fixture Block\n", "  name: Fixture Block\n  owner:\n"}},
		{"a plain number in a string field", "metadata.name", []string{
			"  name: Fixture Block\n", "  name: 2048\n"}},
		{"a plain number as a param default", "spec.params[0].default", []string{
			"      label: Container image\n", "      label: Container image\n      default: 8080\n"}},
		{"a plain boolean in a string field", "spec.params[0].label", []string{
			"      label: Container image\n", "      label: true\n"}},
		{"no resources", "spec.resources", []string{
			"  resources:\n    - name: fallback-network\n      type: aws.vpc\n    - name: app-security-group\n" +
				"      type: aws.security-group\n      inputs: { network: resources.fallback-network }\n" +
				"  provides: [app-security-group]\n",
			"  resources: []\n"}},
		{"no lifecycle operations", "spec.lifecycle.operations", []string{
			"    operations: [subscribe, unsubscribe]\n", "    operations: []\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			requireRefusedNaming(t, editBeta(t, tc.edits...), tc.path)
		})
	}
}

// A quoted value is a string wherever it is written, and an input takes a
// number or a boolean, since the schema allows one there.
func TestParseManifest_V1BetaAcceptsAQuotedStringAndATypedInput(t *testing.T) {
	t.Run("a quoted number as a param default", func(t *testing.T) {
		b := parseBeta(t, editBeta(t, "      label: Container image\n", "      label: Container image\n      default: \"8080\"\n"))
		if got := b.Spec.Params[0].Default; got != "8080" {
			t.Errorf("spec.params[0].default = %q, want %q", got, "8080")
		}
	})
	t.Run("a number and a boolean as inputs", func(t *testing.T) {
		b := parseBeta(t, editBeta(t, "inputs: { network: resources.fallback-network }",
			"inputs: { network: resources.fallback-network, port: 8080, public: true }"))
		want := map[string]contract.InputValue{
			"network": scalar("resources.fallback-network"), "port": scalar("8080"), "public": scalar("true"),
		}
		if got := b.Spec.Resources[1].Inputs; !sameInputs(got, want) {
			t.Errorf("spec.resources[1].inputs = %+v, want %+v", got, want)
		}
	})
}

// v1alphaManifest carries a padded quoted id and version, a folded release
// note, and outputs and reads in both list forms.
const v1alphaManifest = `apiVersion: mitstack/v1alpha
kind: Block
metadata:
  id: " reads-consumer"
  name: Reads Consumer
spec:
  layer: backend
  version: " 1.2.0 "
  releaseClass: feature
  releaseNote: >
    Adds the reads.
  outputs:
    - dbName
    - host.type
  requires:
    - capability: database
      reads: [host, port]
    - capability: github
    - capability: app-host
      reads:
        - region
`

// Under v1alpha the parser reads the fields blockverify's manifest suite
// checks into V1AlphaManifest's own fields, under either top-level spelling.
func TestParseManifest_V1AlphaReadsOutputsAndRequiresReads(t *testing.T) {
	for label, raw := range map[string]string{
		"kind: Block": v1alphaManifest,
		"type: block": strings.Replace(v1alphaManifest, "kind: Block\n", "type: block\n", 1),
	} {
		t.Run(label, func(t *testing.T) {
			m, err := contract.ParseManifest([]byte(raw))
			if err != nil {
				t.Fatalf("ParseManifest: %v", err)
			}
			if m.APIVersion != contract.ManifestAPIVersionV1Alpha {
				t.Errorf("APIVersion = %q, want %q", m.APIVersion, contract.ManifestAPIVersionV1Alpha)
			}
			if m.Block != nil {
				t.Errorf("Block = %+v, want nil under %s", *m.Block, contract.ManifestAPIVersionV1Alpha)
			}
			if m.V1Alpha == nil {
				t.Fatalf("V1Alpha is nil under %s", contract.ManifestAPIVersionV1Alpha)
			}
			a := m.V1Alpha
			if a.ID != " reads-consumer" || m.ID() != " reads-consumer" {
				t.Errorf("V1Alpha.ID, ID() = %q, %q; want the untrimmed %q", a.ID, m.ID(), " reads-consumer")
			}
			if a.Version != "1.2.0" || m.Version() != "1.2.0" {
				t.Errorf("V1Alpha.Version, Version() = %q, %q; want the trimmed %q", a.Version, m.Version(), "1.2.0")
			}
			if a.ReleaseClass != "feature" || m.ReleaseClass() != "feature" {
				t.Errorf("V1Alpha.ReleaseClass, ReleaseClass() = %q, %q; want %q", a.ReleaseClass, m.ReleaseClass(), "feature")
			}
			if strings.TrimSpace(a.ReleaseNote) != "Adds the reads." || strings.TrimSpace(m.ReleaseNote()) != "Adds the reads." {
				t.Errorf("V1Alpha.ReleaseNote, ReleaseNote() = %q, %q; want %q", a.ReleaseNote, m.ReleaseNote(), "Adds the reads.")
			}
			if want := []string{"dbName", "host.type"}; !slices.Equal(a.Outputs, want) {
				t.Errorf("V1Alpha.Outputs = %q, want %q", a.Outputs, want)
			}
			wantRequires := []contract.V1AlphaRequirement{
				{Capability: "database", Reads: []string{"host", "port"}},
				{Capability: "github"},
				{Capability: "app-host", Reads: []string{"region"}},
			}
			if len(a.Requires) != len(wantRequires) {
				t.Fatalf("V1Alpha.Requires = %+v, want %+v", a.Requires, wantRequires)
			}
			for i, want := range wantRequires {
				if a.Requires[i].Capability != want.Capability || !slices.Equal(a.Requires[i].Reads, want.Reads) {
					t.Errorf("V1Alpha.Requires[%d] = %+v, want %+v", i, a.Requires[i], want)
				}
			}
		})
	}
}

func TestParseManifest_RefusesAnUnknownOrMissingAPIVersionNamingBothKnownVersions(t *testing.T) {
	body := "type: block\nmetadata:\n  id: fixture-block\nspec:\n  version: 1.0.0\n"
	cases := []struct {
		label, raw string
		wants      []string
	}{
		{"absent", body, nil},
		{"empty", "apiVersion: \"\"\n" + body, nil},
		{"a later version", "apiVersion: mitstack/v2\n" + body, []string{"mitstack/v2"}},
		{"another group", "apiVersion: example.test/v1beta\n" + body, []string{"example.test/v1beta"}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			wants := append([]string{contract.ManifestAPIVersionV1Alpha, contract.ManifestAPIVersion}, tc.wants...)
			requireRefusedNaming(t, []byte(tc.raw), wants...)
		})
	}
}

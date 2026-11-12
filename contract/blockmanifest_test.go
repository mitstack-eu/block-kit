package contract_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
)

type wantRequirement struct {
	name      string
	types     []string
	many      bool
	reference string
}

type wantResource struct {
	name  string
	typ   string
	types []string
}

func oneType(id string) []string { return []string{id} }

func requireRequirements(t *testing.T, got []contract.ManifestRequirement, want []wantRequirement) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("spec.requires has %d entries %+v, want %d %+v", len(got), got, len(want), want)
	}
	for i, w := range want {
		g := got[i]
		if g.Name != w.name || !slices.Equal(g.Types, w.types) || g.Many != w.many {
			t.Errorf("spec.requires[%d] name, types, many = %q, %q, %v; want %q, %q, %v", i, g.Name, g.Types, g.Many, w.name, w.types, w.many)
		}
		if ref := g.Reference(); ref != w.reference {
			t.Errorf("spec.requires[%d].Reference() = %q, want %q", i, ref, w.reference)
		}
	}
}

func requireResources(t *testing.T, got []contract.ManifestResource, want []wantResource) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("spec.resources has %d entries %+v, want %d %+v", len(got), got, len(want), want)
	}
	for i, w := range want {
		g := got[i]
		if g.Name != w.name || g.Type != w.typ || !slices.Equal(g.Types, w.types) {
			t.Errorf("spec.resources[%d] name, type, types = %q, %q, %q; want %q, %q, %q", i, g.Name, g.Type, g.Types, w.name, w.typ, w.types)
		}
	}
}

func TestParseManifest_ReadsTheWorkedV1BetaManifests(t *testing.T) {
	cases := []struct {
		file                                   string
		id, version, releaseClass, releaseNote string
		rest                                   contract.BlockManifest
		requires                               []wantRequirement
		resources                              []wantResource
	}{
		{
			file: "plain-v1beta.yaml", id: "plain", version: "3.0.0", releaseClass: "breaking",
			releaseNote: "Plain runs its container on the app's EC2 host, against the Postgres database offered there.",
			rest: contract.BlockManifest{
				APIVersion: "mitstack/v1beta", Type: "block",
				Metadata: contract.ManifestMetadata{ID: "plain", Name: "Plain Tracker", Owner: "web-app-platform"},
				Spec: contract.ManifestSpec{
					Version: "3.0.0", ReleaseClass: "breaking",
					ReleaseNote: "Plain runs its container on the app's EC2 host, against the Postgres database offered there.",
					Description: "Runs the Plain tracker container on the app's host, connected to the app's Postgres database.",
					Provides:    []string{"aws.ec2-container"},
					Lifecycle: contract.ManifestLifecycle{Endpoint: "https://api.mitstack.dev/operators/plain/hook",
						Operations: []string{"subscribe", "unsubscribe", "upgrade", "validate"}, Contract: "kit/v1"},
				},
			},
			requires: []wantRequirement{
				{types: oneType("aws.ec2-instance"), reference: "aws.ec2-instance"},
				{types: oneType("aws.ec2-postgres-database"), reference: "aws.ec2-postgres-database"},
			},
			resources: []wantResource{{name: "aws.ec2-container", typ: "aws.ec2-container", types: oneType("aws.ec2-container")}},
		},
		{
			file: "aws-app-infra-v1beta.yaml", id: "aws-app-infra", version: "2.0.0", releaseClass: "breaking",
			releaseNote: "AWS App Infrastructure offers its host, data volume, registry, deploy role and address as separate resources.",
			rest: contract.BlockManifest{
				APIVersion: "mitstack/v1beta", Type: "block",
				Metadata: contract.ManifestMetadata{ID: "aws-app-infra", Name: "AWS App Infrastructure"},
				Spec: contract.ManifestSpec{
					Version: "2.0.0", ReleaseClass: "breaking",
					ReleaseNote: "AWS App Infrastructure offers its host, data volume, registry, deploy role and address as separate resources.",
					Description: "Creates the app's host, data volume and image registry in the consumer's own AWS account, and the app's public address.",
					Provides:    []string{"aws.ec2-instance", "aws.ebs-volume", "aws.ecr-repository", "deploy-role", "aws.route53-record"},
					Lifecycle: contract.ManifestLifecycle{Endpoint: "https://api.mitstack.dev/operators/aws-app-infra/hook",
						Operations: []string{"subscribe", "unsubscribe", "upgrade", "validate"}, Contract: "kit/v1"},
				},
			},
			requires: []wantRequirement{{types: oneType("github.repository"), reference: "github.repository"}},
			resources: []wantResource{
				{name: "aws.s3-bucket", typ: "aws.s3-bucket", types: oneType("aws.s3-bucket")},
				{name: "aws.vpc", typ: "aws.vpc", types: oneType("aws.vpc")},
				{name: "aws.security-group", typ: "aws.security-group", types: oneType("aws.security-group")},
				{name: "aws.ec2-instance", typ: "aws.ec2-instance", types: oneType("aws.ec2-instance")},
				{name: "aws.ebs-volume", typ: "aws.ebs-volume", types: oneType("aws.ebs-volume")},
				{name: "aws.ecr-repository", typ: "aws.ecr-repository", types: oneType("aws.ecr-repository")},
				{name: "ecr-push-role", typ: "aws.iam-role", types: oneType("aws.iam-role")},
				{name: "deploy-role", typ: "aws.iam-role", types: oneType("aws.iam-role")},
				{name: "aws.egress-proxy", typ: "aws.egress-proxy", types: oneType("aws.egress-proxy")},
				{name: "aws.ssm-association", typ: "aws.ssm-association", types: oneType("aws.ssm-association")},
				{name: "aws.route53-record", typ: "aws.route53-record", types: oneType("aws.route53-record")},
			},
		},
	}
	for _, w := range cases {
		t.Run(w.file, func(t *testing.T) {
			m, err := contract.ParseManifest(readManifestGolden(t, w.file))

			if err != nil {
				t.Fatalf("ParseManifest(%s): %v", w.file, err)
			}
			if m.APIVersion != contract.ManifestAPIVersion || m.Block == nil {
				t.Fatalf("APIVersion, Block = %q, %v; want %q and a block", m.APIVersion, m.Block, contract.ManifestAPIVersion)
			}
			if m.ID() != w.id || m.Version() != w.version || m.ReleaseClass() != w.releaseClass || m.ReleaseNote() != w.releaseNote {
				t.Errorf("ID, Version, ReleaseClass, ReleaseNote = %q, %q, %q, %q; want %q, %q, %q, %q",
					m.ID(), m.Version(), m.ReleaseClass(), m.ReleaseNote(), w.id, w.version, w.releaseClass, w.releaseNote)
			}
			got := *m.Block
			gotRequires, gotResources := got.Spec.Requires, got.Spec.Resources
			got.Spec.Requires, got.Spec.Resources = nil, nil
			got.Spec.Description = strings.TrimRight(got.Spec.Description, "\n")
			wantField(t, "the manifest outside requires and resources", got, w.rest)
			requireRequirements(t, gotRequires, w.requires)
			requireResources(t, gotResources, w.resources)
		})
	}
}

const typedManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: typed-block
  name: Typed Block
spec:
  version: 1.0.0
  requires:
    - github.repository
    - { name: host, type: [aws.ec2-instance, hetzner.server] }
    - { name: primary, type: aws.iam-role }
    - name: secondary
      type: aws.iam-role
      many: true
  resources:
    - aws.vpc
    - { name: app-container, type: [aws.ec2-container, hetzner.server-container] }
    - name: state
      type: aws.s3-bucket
    - name: worker
      type:
        - aws.ec2-container
        - hetzner.server-container
  provides: [app-container, aws.vpc]
  lifecycle:
    endpoint: https://typed-block.example/hook
    operations: [subscribe, unsubscribe, upgrade, validate]
    contract: kit/v1
`

func TestParseManifest_V1BetaReadsRequirementsAndResourcesByExactType(t *testing.T) {
	b := parseBeta(t, []byte(typedManifest))

	requireRequirements(t, b.Spec.Requires, []wantRequirement{
		{types: oneType("github.repository"), reference: "github.repository"},
		{name: "host", types: []string{"aws.ec2-instance", "hetzner.server"}, reference: "host"},
		{name: "primary", types: oneType("aws.iam-role"), reference: "primary"},
		{name: "secondary", types: oneType("aws.iam-role"), many: true, reference: "secondary"},
	})
	requireResources(t, b.Spec.Resources, []wantResource{
		{name: "aws.vpc", typ: "aws.vpc", types: oneType("aws.vpc")},
		{name: "app-container", types: []string{"aws.ec2-container", "hetzner.server-container"}},
		{name: "state", typ: "aws.s3-bucket", types: oneType("aws.s3-bucket")},
		{name: "worker", types: []string{"aws.ec2-container", "hetzner.server-container"}},
	})
	wantField(t, "spec.provides", b.Spec.Provides, []string{"app-container", "aws.vpc"})
}

func TestParseManifest_V1BetaReadsTheShortRequiresListOfTypeIDs(t *testing.T) {
	raw := edit(t, typedManifest,
		"  requires:\n    - github.repository\n    - { name: host, type: [aws.ec2-instance, hetzner.server] }\n"+
			"    - { name: primary, type: aws.iam-role }\n    - name: secondary\n      type: aws.iam-role\n      many: true\n",
		"  requires: [github.repository]\n")

	b := parseBeta(t, raw)

	requireRequirements(t, b.Spec.Requires, []wantRequirement{{types: oneType("github.repository"), reference: "github.repository"}})
}

const retiredKeyManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: retired-keys
  name: Retired Keys
spec:
  version: 1.0.0
  requires:
    - github.repository
    - name: host
      type: aws.ec2-instance
  resources:
    - aws.vpc
    - name: app-container
      type: aws.ec2-container
  provides: [app-container]
  lifecycle:
    endpoint: https://retired-keys.example/hook
    operations: [subscribe, unsubscribe, upgrade, validate]
    contract: kit/v1
`

const betaManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: fixture-block
  name: Fixture Block
spec:
  version: 1.0.0
  requires:
    - type: aws.ec2-instance
  resources:
    - name: fallback-network
      type: aws.vpc
    - name: app-security-group
      type: aws.security-group
  provides: [app-security-group]
  lifecycle:
    endpoint: https://example.test/hook
    operations: [subscribe, unsubscribe]
    contract: kit/v1
`

func TestParseManifest_V1BetaReadsEachFieldAsWritten(t *testing.T) {
	cluster, err := os.ReadFile("../verify/blockverify/testdata/capability-without-issue-handler/manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	requires := func(line string) []byte {
		return edit(t, betaManifest, "    - type: aws.ec2-instance\n", "    - type: aws.ec2-instance\n"+line)
	}
	spec := func(line string) []byte {
		return edit(t, betaManifest, "  version: 1.0.0\n", "  version: 1.0.0\n"+line)
	}
	lifecycle := func(lines string) []byte {
		return edit(t, betaManifest, "    contract: kit/v1\n", "    contract: kit/v1\n"+lines)
	}
	cases := []struct {
		label string
		raw   []byte
		got   func(b *contract.BlockManifest) any
		want  any
	}{
		{"the base fixture declares two resources", []byte(betaManifest), func(b *contract.BlockManifest) any { return len(b.Spec.Resources) }, 2},
		{"the retired-key fixture parses", []byte(retiredKeyManifest), func(b *contract.BlockManifest) any { return b.Metadata.ID }, "retired-keys"},
		{"the base fixture is typed block", []byte(betaManifest), func(b *contract.BlockManifest) any { return b.Type }, contract.ManifestTypeBlock},
		{"the base fixture typed capability", edit(t, betaManifest, "type: block\n", "type: capability\n"),
			func(b *contract.BlockManifest) any { return b.Type }, contract.ManifestTypeCapability},
		{"hcloud-k3s-cluster's manifest typed capability", cluster, func(b *contract.BlockManifest) any { return b.Type }, contract.ManifestTypeCapability},
		{"a resource with no name is named by its type", edit(t, betaManifest, "    - name: fallback-network\n      type: aws.vpc\n", "    - type: aws.vpc\n"),
			func(b *contract.BlockManifest) any { return b.Spec.Resources[0].Name }, "aws.vpc"},
		{"a type the platform's catalogue lacks needs no snapshot to parse",
			edit(t, typedManifest, "    - aws.vpc\n", "    - aws.not-in-any-catalogue\n",
				"  provides: [app-container, aws.vpc]\n", "  provides: [app-container, aws.not-in-any-catalogue]\n"),
			func(b *contract.BlockManifest) any { return b.Spec.Resources[0].Name }, "aws.not-in-any-catalogue"},
		{"many absent reads false", requires(""), func(b *contract.BlockManifest) any { return b.Spec.Requires[0].Many }, false},
		{"many: true reads true", requires("      many: true\n"), func(b *contract.BlockManifest) any { return b.Spec.Requires[0].Many }, true},
		{"many: false reads false", requires("      many: false\n"), func(b *contract.BlockManifest) any { return b.Spec.Requires[0].Many }, false},
		{"optional absent reads false", requires(""), func(b *contract.BlockManifest) any { return b.Spec.Requires[0].Optional }, false},
		{"optional: true reads true", requires("      optional: true\n"), func(b *contract.BlockManifest) any { return b.Spec.Requires[0].Optional }, true},
		{"optional: false reads false", requires("      optional: false\n"), func(b *contract.BlockManifest) any { return b.Spec.Requires[0].Optional }, false},
		{"optional: true keeps the requirement's reference", requires("      optional: true\n"),
			func(b *contract.BlockManifest) any { return b.Spec.Requires[0].Reference() }, "aws.ec2-instance"},
		{"a quoted number is a string", spec("  description: \"8080\"\n"), func(b *contract.BlockManifest) any { return b.Spec.Description }, "8080"},
		{"receivesAppSecrets absent reads false", spec(""), func(b *contract.BlockManifest) any { return b.Spec.ReceivesAppSecrets }, false},
		{"receivesAppSecrets: true reads true", spec("  receivesAppSecrets: true\n"),
			func(b *contract.BlockManifest) any { return b.Spec.ReceivesAppSecrets }, true},
		{"receivesAppSecrets: false reads false", spec("  receivesAppSecrets: false\n"),
			func(b *contract.BlockManifest) any { return b.Spec.ReceivesAppSecrets }, false},
		{"needsCredentialRefresh absent reads false", lifecycle(""),
			func(b *contract.BlockManifest) any { return b.Spec.Lifecycle.NeedsCredentialRefresh }, false},
		{"needsCredentialRefresh: true reads true", lifecycle("    needsCredentialRefresh: true\n"),
			func(b *contract.BlockManifest) any { return b.Spec.Lifecycle.NeedsCredentialRefresh }, true},
		{"needsCredentialRefresh: false reads false", lifecycle("    needsCredentialRefresh: false\n"),
			func(b *contract.BlockManifest) any { return b.Spec.Lifecycle.NeedsCredentialRefresh }, false},
		{"no verbConnections reads no declaration", lifecycle(""),
			func(b *contract.BlockManifest) any { return b.Spec.Lifecycle.VerbConnections }, (*contract.ManifestVerbConnections)(nil)},
		{"verbConnections reads both lists", lifecycle("    verbConnections:\n      validate: [github]\n      unsubscribe: [github, hetzner]\n"),
			func(b *contract.BlockManifest) any { return b.Spec.Lifecycle.VerbConnections },
			&contract.ManifestVerbConnections{Validate: []string{"github"}, Unsubscribe: []string{"github", "hetzner"}}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			b := parseBeta(t, tc.raw)

			got := tc.got(b)

			wantField(t, tc.label, got, tc.want)
		})
	}
}

func TestParseManifest_V1BetaRefusesAManifestNamingWhatIsWrong(t *testing.T) {
	retired := func(pairs ...string) []byte { return edit(t, retiredKeyManifest, pairs...) }
	beta := func(pairs ...string) []byte { return edit(t, betaManifest, pairs...) }
	requires := func(line string) []byte {
		return beta("    - type: aws.ec2-instance\n", "    - type: aws.ec2-instance\n"+line)
	}
	spec := func(line string) []byte { return beta("  version: 1.0.0\n", "  version: 1.0.0\n"+line) }
	lifecycle := func(lines string) []byte { return beta("    contract: kit/v1\n", "    contract: kit/v1\n"+lines) }
	cases := []struct {
		label string
		raw   []byte
		wants []string
	}{
		{"a requirement listing types without a name", retired("    - name: host\n      type: aws.ec2-instance\n", "    - type: [aws.ec2-instance, hetzner.server]\n"),
			[]string{"spec.requires[1]", "name"}},
		{"a resource listing types without a name", retired("    - name: app-container\n      type: aws.ec2-container\n", "    - type: [aws.ec2-container, hetzner.server-container]\n"),
			[]string{"spec.resources[1]", "name"}},
		{"a requirement list naming one type twice", retired("      type: aws.ec2-instance\n", "      type: [aws.ec2-instance, aws.ec2-instance]\n"),
			[]string{"spec.requires[1]", "aws.ec2-instance"}},
		{"a resource list naming one type twice", retired("      type: aws.ec2-container\n", "      type: [aws.ec2-container, aws.ec2-container]\n"),
			[]string{"spec.resources[1]", "aws.ec2-container"}},
		{"two unnamed resources of one type", retired("    - aws.vpc\n", "    - aws.vpc\n    - aws.vpc\n"), []string{"spec.resources[1]", "aws.vpc"}},
		{"two unnamed requirements of one type", retired("    - github.repository\n", "    - github.repository\n    - github.repository\n"),
			[]string{"spec.requires[1]", "github.repository"}},
		{"two resources of one name", retired("    - aws.vpc\n", "    - { name: app-container, type: aws.vpc }\n"), []string{"app-container"}},
		{"two resources of one name written as mappings", beta("    - name: app-security-group\n", "    - name: fallback-network\n",
			"  provides: [app-security-group]\n", "  provides: [fallback-network]\n"), []string{"fallback-network"}},
		{"a requirement mapping with no type", retired("      type: aws.ec2-instance\n", ""), []string{"spec.requires[1].type"}},
		{"a resource mapping with no type", retired("      type: aws.ec2-container\n", ""), []string{"spec.resources[1].type"}},
		{"an empty list of types", retired("      type: aws.ec2-container\n", "      type: []\n"), []string{"spec.resources[1].type"}},
		{"a provides entry naming no resource", retired("  provides: [app-container]\n", "  provides: [app-container, no-such-resource]\n"),
			[]string{"spec.provides", "no-such-resource"}},
		{"a provides entry naming the type of a named resource", retired("  provides: [app-container]\n", "  provides: [aws.ec2-container]\n"),
			[]string{"spec.provides", "aws.ec2-container"}},
		{"a resource type id outside the grammar", retired("    - aws.vpc\n", "    - AWS.VPC\n"), []string{"AWS.VPC", "spec.resources[0]"}},
		{"a resource type id with no capability", retired("    - aws.vpc\n", "    - vpc\n"), []string{"vpc", "spec.resources[0]"}},
		{"a requirement type id outside the grammar", retired("    - github.repository\n", "    - github_repository\n"),
			[]string{"github_repository", "spec.requires[0]"}},
		{"a type id inside a list outside the grammar", retired("      type: aws.ec2-container\n", "      type: [aws.ec2-container, hetzner.Server_Container]\n"),
			[]string{"hetzner.Server_Container", "spec.resources[1]"}},
		{"type absent", beta("type: block\n", ""), []string{"type"}},
		{"type neither block nor capability", beta("type: block\n", "type: component\n"), []string{`type is "component"`, `"block"`, `"capability"`}},
		{"metadata.id absent", beta("  id: fixture-block\n", ""), []string{"metadata.id"}},
		{"spec.version absent", beta("  version: 1.0.0\n", ""), []string{"spec.version"}},
		{"a requirement without a type", beta("    - type: aws.ec2-instance\n", "    - name: primary\n"), []string{"spec.requires[0].type"}},
		{"a resource without a type", beta("      type: aws.security-group\n", ""), []string{"spec.resources[1].type"}},
		{"many: yes", requires("      many: yes\n"), []string{"spec.requires[0].many"}},
		{"many: 1", requires("      many: 1\n"), []string{"spec.requires[0].many"}},
		{`many: "true"`, requires("      many: \"true\"\n"), []string{"spec.requires[0].many"}},
		{"many: sometimes", requires("      many: sometimes\n"), []string{"spec.requires[0].many"}},
		{"optional: yes", requires("      optional: yes\n"), []string{"spec.requires[0].optional"}},
		{`optional: "true"`, requires("      optional: \"true\"\n"), []string{"spec.requires[0].optional"}},
		{"optional: null", requires("      optional: null\n"), []string{"spec.requires[0].optional"}},
		{"a requirement both optional and many", requires("      optional: true\n      many: true\n"),
			[]string{"spec.requires[0]", "optional", "many"}},
		{"a resource's many: yes", beta("      type: aws.vpc\n", "      type: aws.vpc\n      many: yes\n"), []string{"spec.resources[0].many"}},
		{"a resource name holding a slash", beta("    - name: fallback-network\n", "    - name: fallback/network\n"),
			[]string{"spec.resources[0].name", `"fallback/network"`, "slash"}},
		{"a provides entry naming a resource marked many", beta("      type: aws.security-group\n", "      type: aws.security-group\n      many: true\n"),
			[]string{"spec.provides[0]", `"app-security-group"`, "many"}},
		{"an alias as a resource type", beta("      type: aws.vpc\n", "      type: &net aws.vpc\n", "      type: aws.security-group\n", "      type: *net\n"),
			[]string{"spec.resources[1].type", "alias"}},
		{"a merge key in a resource", beta("    - name: app-security-group\n      type: aws.security-group\n",
			"    - name: app-security-group\n      <<: { type: aws.security-group }\n"), []string{"spec.resources[1]", "merge key"}},
		{"an alias as a list", beta("  provides: [app-security-group]\n", "  provides: &provided [app-security-group]\n",
			"    operations: [subscribe, unsubscribe]\n", "    operations: *provided\n"), []string{"spec.lifecycle.operations", "alias"}},
		{"a null resource type", beta("      type: aws.security-group\n", "      type:\n"), []string{"spec.resources[1].type"}},
		{"a null optional field", beta("  name: Fixture Block\n", "  name: Fixture Block\n  owner:\n"), []string{"metadata.owner"}},
		{"a plain number in a string field", beta("  name: Fixture Block\n", "  name: 2048\n"), []string{"metadata.name"}},
		{"a plain number as an entry's name", beta("    - name: app-security-group\n", "    - name: 8080\n"), []string{"spec.resources[1].name"}},
		{"a plain boolean in a string field", spec("  description: true\n"), []string{"spec.description"}},
		{"no resources", beta("  resources:\n    - name: fallback-network\n      type: aws.vpc\n    - name: app-security-group\n"+
			"      type: aws.security-group\n  provides: [app-security-group]\n", "  resources: []\n"), []string{"spec.resources"}},
		{"no lifecycle operations", beta("    operations: [subscribe, unsubscribe]\n", "    operations: []\n"), []string{"spec.lifecycle.operations"}},
		{"one connection name in place of a list", lifecycle("    verbConnections:\n      validate: github\n"),
			[]string{"spec.lifecycle.verbConnections.validate must be a list"}},
		{"a plain number as a connection name", lifecycle("    verbConnections:\n      unsubscribe: [1]\n"),
			[]string{"spec.lifecycle.verbConnections.unsubscribe[0]"}},
		{"receivesAppSecrets: yes", spec("  receivesAppSecrets: yes\n"), []string{"spec.receivesAppSecrets"}},
		{"receivesAppSecrets: 1", spec("  receivesAppSecrets: 1\n"), []string{"spec.receivesAppSecrets"}},
		{`receivesAppSecrets: "true"`, spec("  receivesAppSecrets: \"true\"\n"), []string{"spec.receivesAppSecrets"}},
		{"receivesAppSecrets: sometimes", spec("  receivesAppSecrets: sometimes\n"), []string{"spec.receivesAppSecrets"}},
		{"receivesAppSecrets: null", spec("  receivesAppSecrets: null\n"), []string{"spec.receivesAppSecrets"}},
		{"needsCredentialRefresh: yes", lifecycle("    needsCredentialRefresh: yes\n"), []string{"spec.lifecycle.needsCredentialRefresh"}},
		{"needsCredentialRefresh: 1", lifecycle("    needsCredentialRefresh: 1\n"), []string{"spec.lifecycle.needsCredentialRefresh"}},
		{`needsCredentialRefresh: "true"`, lifecycle("    needsCredentialRefresh: \"true\"\n"), []string{"spec.lifecycle.needsCredentialRefresh"}},
		{"needsCredentialRefresh: sometimes", lifecycle("    needsCredentialRefresh: sometimes\n"), []string{"spec.lifecycle.needsCredentialRefresh"}},
		{"needsCredentialRefresh: null", lifecycle("    needsCredentialRefresh: null\n"), []string{"spec.lifecycle.needsCredentialRefresh"}},
		{"an unknown lifecycle key beside needsCredentialRefresh", lifecycle("    needsCredentialRefresh: true\n    bogus: 1\n"),
			[]string{"spec.lifecycle.bogus"}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			requireRefusedNaming(t, tc.raw, tc.wants...)
		})
	}
}

func TestParseManifest_SettleBudget(t *testing.T) {
	beta := func(line string) []byte {
		return edit(t, betaManifest, "    contract: kit/v1\n", "    contract: kit/v1\n"+line)
	}
	for _, tc := range []struct {
		label   string
		raw     []byte
		want    time.Duration
		refusal []string
	}{
		{"absent under v1beta reads zero", beta(""), 0, nil},
		{"75m under v1beta reads 75 minutes", beta("    settleBudget: 75m\n"), 75 * time.Minute, nil},
		{"0s is refused", beta("    settleBudget: 0s\n"), 0, []string{"spec.lifecycle.settleBudget", "positive duration"}},
		{"-5m is refused", beta("    settleBudget: -5m\n"), 0, []string{"spec.lifecycle.settleBudget", "positive duration"}},
		{"soon is refused", beta("    settleBudget: soon\n"), 0, []string{"spec.lifecycle.settleBudget", "positive duration"}},
		{"30 is refused", beta("    settleBudget: 30\n"), 0, []string{"spec.lifecycle.settleBudget", "positive duration"}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			if tc.refusal != nil {
				requireRefusedNaming(t, tc.raw, tc.refusal...)
				return
			}

			lc := parseBeta(t, tc.raw).Spec.Lifecycle
			raw, err := json.Marshal(lc)
			var back contract.ManifestLifecycle
			var fields map[string]any
			if err == nil {
				err = errors.Join(json.Unmarshal(raw, &back), json.Unmarshal(raw, &fields))
			}

			if err != nil {
				t.Fatalf("JSON round trip of %+v: %v", lc, err)
			}
			if lc.SettleBudget != tc.want {
				t.Errorf("spec.lifecycle.settleBudget = %v, want %v", lc.SettleBudget, tc.want)
			}
			if !reflect.DeepEqual(back, lc) {
				t.Errorf("ManifestLifecycle after a JSON round trip through %s = %+v, want %+v", raw, back, lc)
			}
			written, _ := fields["settleBudget"].(string)
			if tc.want == 0 {
				return
			}
			if again := parseBeta(t, beta("    settleBudget: "+written+"\n")).Spec.Lifecycle.SettleBudget; again != tc.want {
				t.Errorf("JSON settleBudget %#v re-parses as %v, want %v (%s)", fields["settleBudget"], again, tc.want, raw)
			}
		})
	}
}

func TestManifestTypes_EncodeEachOptionalFlagUnderItsKeyOnlyWhenSet(t *testing.T) {
	cases := []struct {
		label string
		set   any
		unset any
		key   string
	}{
		{"spec.receivesAppSecrets", contract.ManifestSpec{ReceivesAppSecrets: true}, contract.ManifestSpec{}, "receivesAppSecrets"},
		{"spec.lifecycle.needsCredentialRefresh", contract.ManifestLifecycle{NeedsCredentialRefresh: true}, contract.ManifestLifecycle{}, "needsCredentialRefresh"},
		{"spec.requires[].optional", contract.ManifestRequirement{Types: []string{"aws.ec2-instance"}, Optional: true},
			contract.ManifestRequirement{Types: []string{"aws.ec2-instance"}}, "optional"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			set, unset := marshalObject(t, tc.set), marshalObject(t, tc.unset)

			if set[tc.key] != true {
				t.Errorf("json.Marshal(%+v)[%q] = %v, want true", tc.set, tc.key, set[tc.key])
			}
			if _, ok := unset[tc.key]; ok {
				t.Errorf("json.Marshal(%+v) carries %q, want the key omitted when false", tc.unset, tc.key)
			}
		})
	}
}

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

func continuityOf(t *testing.T, entries string) []byte {
	t.Helper()
	return edit(t, betaManifest, "  version: 1.0.0\n", "  version: 1.0.0\n  continuity:\n"+entries)
}

func TestParseManifest_V1BetaReadsEachPieceOfStateUnderContinuity(t *testing.T) {
	cases := []struct {
		label string
		raw   []byte
		want  []contract.ManifestContinuity
	}{
		{"a state with a name, a kind, stateful and a snapshot handover",
			continuityOf(t, "    - name: app-data\n      kind: postgres\n      stateful: true\n      handover: snapshot\n"),
			[]contract.ManifestContinuity{{Name: "app-data", Kind: "postgres", Stateful: true, Handover: contract.HandoverSnapshot}}},
		{"a state whose handover is stated as none",
			continuityOf(t, "    - name: gateway\n      kind: server\n      stateful: false\n      handover: none\n"),
			[]contract.ManifestContinuity{{Name: "gateway", Kind: "server", Stateful: false, Handover: contract.HandoverNone}}},
		{"two states read in the order written",
			continuityOf(t, "    - name: vault\n      kind: openbao\n      stateful: true\n      handover: snapshot\n"+
				"    - name: cache\n      kind: volume\n      stateful: false\n      handover: none\n"),
			[]contract.ManifestContinuity{
				{Name: "vault", Kind: "openbao", Stateful: true, Handover: contract.HandoverSnapshot},
				{Name: "cache", Kind: "volume", Stateful: false, Handover: contract.HandoverNone}}},
		{"an empty list, which declares that the block keeps no state", edit(t, betaManifest, "  version: 1.0.0\n", "  version: 1.0.0\n  continuity: []\n"), nil},
		{"a manifest without the section", []byte(betaManifest), nil},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			b := parseBeta(t, tc.raw)

			wantField(t, "spec.continuity", len(b.Spec.Continuity), len(tc.want))
			for i, w := range tc.want {
				wantField(t, fmt.Sprintf("spec.continuity[%d]", i), b.Spec.Continuity[i], w)
			}
		})
	}
}

func TestParseManifest_V1BetaRefusesAStateEntryNamingWhatIsWrong(t *testing.T) {
	entry := func(lines ...string) []byte { return continuityOf(t, "    - "+strings.Join(lines, "\n      ")+"\n") }
	cases := []struct {
		label string
		raw   []byte
		wants []string
	}{
		{"an entry that leaves out its handover", entry("name: app-data", "kind: postgres", "stateful: true"),
			[]string{"spec.continuity[0].handover", "required"}},
		{"an entry that leaves out its name", entry("kind: postgres", "stateful: true", "handover: snapshot"),
			[]string{"spec.continuity[0].name", "required"}},
		{"an entry that leaves out its kind", entry("name: app-data", "stateful: true", "handover: snapshot"),
			[]string{"spec.continuity[0].kind", "required"}},
		{"an entry that leaves out stateful", entry("name: app-data", "kind: postgres", "handover: snapshot"),
			[]string{"spec.continuity[0].stateful", "required"}},
		{"an entry with an empty handover", entry("name: app-data", "kind: postgres", "stateful: true", `handover: ""`),
			[]string{"spec.continuity[0].handover", "must not be empty"}},
		{"an entry with a handover the kit does not know", entry("name: app-data", "kind: postgres", "stateful: true", "handover: detach"),
			[]string{"spec.continuity[0].handover", `"detach"`, "none", "snapshot"}},
		{"an entry with a stateful that is not a boolean", entry("name: app-data", "kind: postgres", `stateful: "yes"`, "handover: none"),
			[]string{"spec.continuity[0].stateful"}},
		{"an entry with a field no entry has", entry("name: app-data", "kind: postgres", "stateful: true", "handover: none", "handoverOutput: dataSnapshotId"),
			[]string{"spec.continuity[0].handoverOutput", "is not a field"}},
		{"an entry whose name holds a space", entry(`name: "app data"`, "kind: postgres", "stateful: true", "handover: snapshot"),
			[]string{"spec.continuity[0].name", `"app data"`, "lowercase kebab case"}},
		{"an entry whose name holds a capital", entry("name: App-Data", "kind: postgres", "stateful: true", "handover: snapshot"),
			[]string{"spec.continuity[0].name", `"App-Data"`, "lowercase kebab case"}},
		{"an entry whose name holds a dot", entry("name: app-data.v2", "kind: postgres", "stateful: true", "handover: none"),
			[]string{"spec.continuity[0].name", `"app-data.v2"`, "lowercase kebab case"}},
		{"an entry whose name ends in a hyphen", entry("name: app-data-", "kind: postgres", "stateful: true", "handover: none"),
			[]string{"spec.continuity[0].name", `"app-data-"`, "lowercase kebab case"}},
		{"a second entry that repeats a name",
			continuityOf(t, "    - name: app-data\n      kind: postgres\n      stateful: true\n      handover: snapshot\n"+
				"    - name: app-data\n      kind: volume\n      stateful: true\n      handover: none\n"),
			[]string{"spec.continuity[1].name", `"app-data"`, "spec.continuity[0]"}},
		{"a section that is not a list", edit(t, betaManifest, "  version: 1.0.0\n", "  version: 1.0.0\n  continuity: postgres\n"),
			[]string{"spec.continuity", "list"}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			requireRefusedNaming(t, tc.raw, tc.wants...)
		})
	}
}

func TestParseManifest_RefusesAManifestInTheOldVersion(t *testing.T) {
	for label, raw := range map[string]string{
		"kind: Block": v1alphaManifest,
		"type: block": string(edit(t, v1alphaManifest, "kind: Block\n", "type: block\n")),
	} {
		t.Run(label, func(t *testing.T) {
			m, err := contract.ParseManifest([]byte(raw))

			want := `apiVersion "mitstack/v1alpha" is not a manifest version the kit knows; it must be mitstack/v1beta`
			if err == nil || err.Error() != want {
				t.Fatalf("ParseManifest = %+v, %v; want the error %q", m, err, want)
			}
		})
	}
}

func TestParseManifest_RefusesAnUnknownOrMissingAPIVersionNamingTheKnownVersion(t *testing.T) {
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
			requireRefusedNaming(t, []byte(tc.raw), append([]string{contract.ManifestAPIVersion}, tc.wants...)...)
		})
	}
}

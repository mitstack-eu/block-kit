package contract_test

import (
	"regexp"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
)

const privateTypeManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: private-type-block
  name: Private Type Block
spec:
  version: 1.0.0
  requires:
    - "@acme/aws.special-bucket"
    - { name: store, type: ["@acme/aws.special-bucket", aws.s3-bucket] }
  resources:
    - { name: bucket, type: "@acme/aws.special-bucket" }
  provides: [bucket]
  lifecycle:
    endpoint: https://private-type-block.example/hook
    operations: [subscribe, unsubscribe]
    contract: kit/v1
`

func TestParseManifest_V1BetaReadsAnOrgsPrivateTypeIDByExactType(t *testing.T) {
	b := parseBeta(t, []byte(privateTypeManifest))

	requireRequirements(t, b.Spec.Requires, []wantRequirement{
		{types: oneType("@acme/aws.special-bucket"), reference: "@acme/aws.special-bucket"},
		{name: "store", types: []string{"@acme/aws.special-bucket", "aws.s3-bucket"}, reference: "store"},
	})
	requireResources(t, b.Spec.Resources, []wantResource{
		{name: "bucket", typ: "@acme/aws.special-bucket", types: oneType("@acme/aws.special-bucket")},
	})
}

func TestParseManifest_V1BetaAcceptsOnlyTypeIDsParseResourceTypeIDAccepts(t *testing.T) {
	for _, id := range []string{"aws.s3-bucket", "@acme/aws.special-bucket", "@org-a/github.repository", "@a/b.c", "@" + slug40 + "/aws.x", slug40 + ".x"} {
		t.Run(id, func(t *testing.T) {
			b := parseBeta(t, edit(t, privateTypeManifest, `type: "@acme/aws.special-bucket" }`, `type: "`+id+`" }`))

			if got := b.Spec.Resources[0].Type; got != id {
				t.Errorf("spec.resources[0].type = %q, want %q", got, id)
			}
			if _, _, _, err := contract.ParseResourceTypeID(id); err != nil {
				t.Errorf("the manifest accepted %q, which ParseResourceTypeID refuses: %v", id, err)
			}
		})
	}
}

func TestParseManifest_V1BetaRefusesAPrivateTypeIDOutsideTheGrammarNamingBothForms(t *testing.T) {
	refused := map[string]string{
		"uppercase namespace": "@Acme/aws.x", "private capability with no name": "@acme/aws", "empty namespace": "@/aws.x",
		"namespace without its @": "acme/aws.x", "two namespaces": "@acme/x/aws.y", "slash after the name": "@acme/aws.x/y",
		"41-character namespace": "@" + slug41 + "/aws.x", "41-character capability": slug41 + ".x",
		"private capability starting with a digit": "@acme/1aws.x", "private name starting with a digit": "@acme/aws.1x",
	}
	for label, id := range refused {
		t.Run(label, func(t *testing.T) {
			raw := edit(t, privateTypeManifest, `type: "@acme/aws.special-bucket" }`, `type: "`+id+`" }`)

			requireRefusedNaming(t, raw, "spec.resources[0].type", `"`+id+`"`, "<capability>.<name>", "@<org>/<capability>.<name>")
		})
	}
}

func TestParseManifest_V1BetaRefusesAnUnnamedResourceOfAPrivateTypeAskingForAName(t *testing.T) {
	raw := edit(t, privateTypeManifest, `    - { name: bucket, type: "@acme/aws.special-bucket" }`, `    - "@acme/aws.special-bucket"`,
		"  provides: [bucket]\n", "")

	requireRefusedNaming(t, raw, "spec.resources[0].name", "@acme/aws.special-bucket", "name the resource without a slash")
}

func TestParseManifest_V1BetaRefusesAResourceNamedByAPrivateTypeIDAskingForANameWithoutASlash(t *testing.T) {
	raw := edit(t, privateTypeManifest, `{ name: bucket, type:`, `{ name: "@acme/aws.special-bucket", type:`, "  provides: [bucket]\n", "")

	requireRefusedNaming(t, raw, "spec.resources[0].name", "@acme/aws.special-bucket", "name the resource without a slash")
}

func TestResourceTypeIDGrammar_TheSchemaTheManifestAndParseResourceTypeIDAcceptTheSameIDs(t *testing.T) {
	doc := jsonObject(t, contract.ManifestSchema)
	pattern := regexp.MustCompile(schemaAt(t, doc, doc, "$defs", "typeId")["pattern"].(string))
	cases := map[string]bool{
		"aws.s3-bucket": true, "@acme/aws.special-bucket": true, slug40 + ".x": true, "@acme/" + slug40 + ".x": true, "a--b.x": true,
		slug41 + ".x": false, "@acme/" + slug41 + ".x": false, "1aws.x": false, "aws.1x": false, "@acme/1aws.x": false,
		"aws.x--y": false, "@" + slug41 + "/aws.x": false, "aws-.x": false,
	}
	for id, want := range cases {
		t.Run(id, func(t *testing.T) {
			_, _, _, err := contract.ParseResourceTypeID(id)
			_, parseErr := contract.ParseManifest(edit(t, privateTypeManifest, `type: "@acme/aws.special-bucket" }`, `type: "`+id+`" }`))

			if got := [3]bool{pattern.MatchString(id), err == nil, parseErr == nil}; got != [3]bool{want, want, want} {
				t.Errorf("schema pattern, ParseResourceTypeID, ParseManifest accept %q = %v, want all %v (%v; %v)", id, got, want, err, parseErr)
			}
		})
	}
}

func TestManifestSchema_TypeIDPatternIsTheGrammarTheParserHolds(t *testing.T) {
	doc := jsonObject(t, contract.ManifestSchema)
	typeID := schemaAt(t, doc, doc, "$defs", "typeId")
	pattern, _ := typeID["pattern"].(string)
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("$defs.typeId.pattern %q does not compile: %v", pattern, err)
	}
	for _, id := range []string{"aws.s3-bucket", "@acme/aws.special-bucket", "@" + slug40 + "/aws.x"} {
		if !re.MatchString(id) {
			t.Errorf("$defs.typeId.pattern %q refuses %q, which ParseManifest accepts", pattern, id)
		}
	}
	for _, id := range []string{"@Acme/aws.x", "acme/aws.x", "@/aws.x", "@acme/aws", "@acme/x/aws.y", "@" + slug41 + "/aws.x", slug41 + ".x"} {
		if re.MatchString(id) {
			t.Errorf("$defs.typeId.pattern %q accepts %q, which ParseManifest refuses", pattern, id)
		}
	}
	if desc, _ := typeID["description"].(string); !regexp.MustCompile(`@<org>/<capability>\.<name>`).MatchString(desc) {
		t.Errorf("$defs.typeId.description = %q, want it to name the private form @<org>/<capability>.<name>", desc)
	}
}

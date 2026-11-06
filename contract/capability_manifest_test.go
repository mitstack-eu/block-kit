package contract_test

// A capability manifest holds the presentation, connection, kinds and reach sections, and only a capability may.

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
)

func sendcloudManifest(t *testing.T) []byte {
	t.Helper()
	for _, row := range verdictRows(t) {
		if row.Name == "capability-sendcloud.yaml" {
			return row.Manifest
		}
	}
	t.Fatal("the verdict table has no capability-sendcloud.yaml row")
	return nil
}

func TestParseManifest_ReadsEverySectionOfTheSendcloudCapability(t *testing.T) {
	m, err := contract.ParseManifest(sendcloudManifest(t))
	if err != nil {
		t.Fatalf("ParseManifest(sendcloud): %v", err)
	}

	spec := m.Block.Spec

	rx := 1.0
	wantMark := []contract.ManifestMarkShape{
		{Rect: &contract.MarkRect{X: 3.5, Y: 9, Width: 17, Height: 11, Rx: &rx}},
		{Path: &contract.MarkPath{D: "M3.5 9L6 5h12l2.5 4"}},
		{Path: &contract.MarkPath{D: "M10.5 9v4.5h3V9"}},
	}
	wantConnection := &contract.ManifestConnection{Mechanism: "stored-token", TokenSchema: []contract.ManifestTokenField{
		{Name: "public_key", Secret: true, Description: "Sendcloud public key (Integrations -> Sendcloud API)"},
		{Name: "private_key", Secret: true, Description: "Sendcloud secret key of the same integration"},
	}}
	wantDocs := &contract.ManifestDocs{
		Source: &contract.ManifestDocsSource{Git: "https://github.com/mitstack-eu/sendcloud-capability", Ref: "v1.0.0", Path: "docs"},
		Index:  "connect.md",
	}
	for _, c := range []struct {
		section   string
		got, want any
	}{
		{"type", m.Block.Type, contract.ManifestTypeCapability},
		{"metadata.id", m.ID(), "sendcloud"},
		{"presentation.vendor and category", [2]string{spec.Presentation.Vendor, spec.Presentation.Category}, [2]string{"Sendcloud", "Delivery"}},
		{"presentation.mark", spec.Presentation.Mark, wantMark},
		{"connection", spec.Connection, wantConnection},
		{"lifecycle.operations", spec.Lifecycle.Operations, []string{"verify-connection"}},
		{"docs", spec.Docs, wantDocs},
		{"resources", len(spec.Resources), 0},
	} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("sendcloud %s = %+v, want %+v", c.section, c.got, c.want)
		}
	}
}

func TestParseManifest_ReadsKindsAndReach(t *testing.T) {
	raw := edit(t, string(sendcloudManifest(t)), "  connection:\n", "  kinds: [parcel, label-printer]\n  reach: { capability: github }\n  connection:\n")

	m, err := contract.ParseManifest(raw)

	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if want := []string{"parcel", "label-printer"}; !slices.Equal(m.Block.Spec.Kinds, want) {
		t.Errorf("spec.kinds = %q, want %q", m.Block.Spec.Kinds, want)
	}
	if got := m.Block.Spec.Reach; got == nil || got.Capability != "github" {
		t.Errorf("spec.reach = %+v, want capability github", got)
	}
}

func TestParseManifest_ReadsEachMarkShapeAndAnOptionalCornerRadius(t *testing.T) {
	marks := `    mark:
      - circle: { cx: 12, cy: 12, r: 4.5 }
      - ellipse: { cx: 12, cy: 12, rx: 8, ry: 3 }
      - line: { x1: 1, y1: 2, x2: 3, y2: 4 }
      - polyline: { points: [1, 2, 3, 4.5] }
      - polygon: { points: [0, 0, 4, 0, 2, 3] }
      - rect: { x: 1, y: 2, width: 3, height: 4 }
`
	raw := edit(t, string(sendcloudManifest(t)), "    mark:\n      - rect: { x: 3.5, y: 9, width: 17, height: 11, rx: 1 }\n      - path: { d: \"M3.5 9L6 5h12l2.5 4\" }\n      - path: { d: \"M10.5 9v4.5h3V9\" }\n", marks)

	m, err := contract.ParseManifest(raw)

	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	mark := m.Block.Spec.Presentation.Mark
	if len(mark) != 6 || mark[0].Circle == nil || mark[1].Ellipse == nil || mark[2].Line == nil || mark[3].Polyline == nil || mark[4].Polygon == nil || mark[5].Rect == nil {
		t.Fatalf("spec.presentation.mark = %+v, want a circle, ellipse, line, polyline, polygon and rect in order", mark)
	}
	if got, want := mark[3].Polyline.Points, []float64{1, 2, 3, 4.5}; !slices.Equal(got, want) {
		t.Errorf("polyline points = %v, want %v", got, want)
	}
	if mark[5].Rect.Rx != nil || mark[0].Circle.R != 4.5 {
		t.Errorf("rect rx = %v and circle r = %v, want no rx and 4.5", mark[5].Rect.Rx, mark[0].Circle.R)
	}
}

func TestParseManifest_ASectionSurvivesAJSONRoundTrip(t *testing.T) {
	m, err := contract.ParseManifest(sendcloudManifest(t))
	if err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(m.Block)
	if err != nil {
		t.Fatal(err)
	}
	var back contract.BlockManifest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("json.Unmarshal(%s): %v", raw, err)
	}

	if !reflect.DeepEqual(&back, m.Block) {
		t.Errorf("the manifest re-read from its JSON is\n%+v\nwant\n%+v", back, *m.Block)
	}
}

func TestParseManifest_RefusesWhatACapabilityManifestGetsWrong(t *testing.T) {
	sendcloud := string(sendcloudManifest(t))
	cases := []struct {
		name string
		raw  []byte
		want []string
	}{
		{"a section the manifest does not know", edit(t, sendcloud, "  connection:\n", "  shipping: { carrier: dhl }\n  connection:\n"), []string{"spec.shipping"}},
		{"an unknown presentation key", edit(t, sendcloud, "    category: Delivery\n", "    category: Delivery\n    logo: x\n"), []string{"spec.presentation.logo"}},
		{"a presentation without a vendor", edit(t, sendcloud, "    vendor: Sendcloud\n", ""), []string{"spec.presentation.vendor", "required"}},
		{"a mark shape holding two shapes", edit(t, sendcloud, "      - path: { d: \"M10.5 9v4.5h3V9\" }\n", "      - path: { d: \"M10.5 9v4.5h3V9\" }\n        circle: { cx: 1, cy: 1, r: 1 }\n"), []string{"spec.presentation.mark[2]", "exactly one shape"}},
		{"a mark shape holding none", edit(t, sendcloud, "      - path: { d: \"M10.5 9v4.5h3V9\" }\n", "      - path: { d: \"M10.5 9v4.5h3V9\" }\n      - {}\n"), []string{"spec.presentation.mark[3]", "exactly one shape"}},
		{"a mark shape of an unknown kind", edit(t, sendcloud, "      - path: { d: \"M10.5 9v4.5h3V9\" }\n", "      - path: { d: \"M10.5 9v4.5h3V9\" }\n      - text: { x: 1 }\n"), []string{"spec.presentation.mark[3].text"}},
		{"a rect missing its width", edit(t, sendcloud, "width: 17, ", ""), []string{"spec.presentation.mark[0].rect.width", "required"}},
		{"a number written as a string", edit(t, sendcloud, "x: 3.5", "x: \"3.5\""), []string{"spec.presentation.mark[0].rect.x", "number"}},
		{"a number written as a word", edit(t, sendcloud, "x: 3.5", "x: wide"), []string{"spec.presentation.mark[0].rect.x", "number"}},
		{"a token field without secret", edit(t, sendcloud, "name: public_key, secret: true, ", "name: public_key, "), []string{"spec.connection.tokenSchema[0].secret", "required"}},
		{"a connection without a mechanism", edit(t, sendcloud, "    mechanism: stored-token\n", ""), []string{"spec.connection.mechanism", "required"}},
		{"an unknown connection key", edit(t, sendcloud, "    mechanism: stored-token\n", "    mechanism: stored-token\n    probe: GET /\n"), []string{"spec.connection.probe"}},
		{"a reach without a capability", edit(t, sendcloud, "  connection:\n", "  reach: {}\n  connection:\n"), []string{"spec.reach.capability", "required"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := contract.ParseManifest(tc.raw)

			if err == nil {
				t.Fatal("ParseManifest accepts the manifest, want it refused")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("ParseManifest error = %q, want it to contain %q", err, want)
				}
			}
		})
	}
}

func TestParseManifest_ABlockCreatingNothingIsRefusedAndACapabilityMay(t *testing.T) {
	block := edit(t, string(sendcloudManifest(t)), "type: capability\n", "type: block\n")

	_, err := contract.ParseManifest(block)

	if err == nil || !strings.Contains(err.Error(), "spec.resources") {
		t.Errorf("ParseManifest of a block with no resources = %v, want a refusal naming spec.resources", err)
	}
}

func TestManifestSchema_DeclaresTheCapabilitySectionsAndNoRequiredResources(t *testing.T) {
	doc := jsonObject(t, contract.ManifestSchema)
	spec := schemaAt(t, doc, doc, "properties", "spec")
	props, _ := spec["properties"].(map[string]any)

	for _, section := range []string{"presentation", "connection", "kinds", "reach", "docs"} {
		if _, ok := props[section]; !ok {
			t.Errorf("the manifest schema's spec properties = %q, want %s among them", sortedKeys(props), section)
		}
		if slices.Contains(sortedStrings(spec["required"]), section) {
			t.Errorf("the manifest schema requires spec.%s, but a block declares none", section)
		}
	}
	if slices.Contains(sortedStrings(spec["required"]), "resources") {
		t.Errorf("the manifest schema requires spec.resources, but a capability creates none")
	}
	rect := schemaAt(t, doc, doc, "properties", "spec", "properties", "presentation", "properties", "mark", "items", "properties", "rect", "properties", "width")
	if rect["type"] != "number" {
		t.Errorf("the schema's rect width = %v, want type number", rect)
	}
}

func TestCheckManifest_AcceptsAConnectionOfEachMechanism(t *testing.T) {
	sendcloud := string(sendcloudManifest(t))
	oidc := edit(t, sendcloud, "operations: [verify-connection]", "operations: [validate]", "    mechanism: stored-token\n    tokenSchema:\n      - { name: public_key, secret: true, description: \"Sendcloud public key (Integrations -> Sendcloud API)\" }\n      - { name: private_key, secret: true, description: \"Sendcloud secret key of the same integration\" }\n", "    mechanism: oidc\n    audience: sts.example.com\n")
	oauth := edit(t, sendcloud, "operations: [verify-connection]", "operations: [validate]", "    mechanism: stored-token\n    tokenSchema:\n      - { name: public_key, secret: true, description: \"Sendcloud public key (Integrations -> Sendcloud API)\" }\n      - { name: private_key, secret: true, description: \"Sendcloud secret key of the same integration\" }\n", "    mechanism: oauth-app\n    installHint: Install the app from the vendor's marketplace.\n    heldOnlyByGrant: true\n")
	for name, raw := range map[string][]byte{"oidc": oidc, "oauth-app": oauth} {
		t.Run(name, func(t *testing.T) {
			m, err := contract.ParseManifest(raw)
			if err != nil {
				t.Fatalf("ParseManifest: %v", err)
			}

			if err := contract.CheckManifest(m.Block, platformSnapshot(t).Holds, contract.CheckOptions{}); err != nil {
				t.Errorf("CheckManifest refused a %s connection: %v", name, err)
			}
		})
	}
}

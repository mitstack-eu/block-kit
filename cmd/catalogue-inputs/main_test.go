package main

// main_test.go pins the catalogue-inputs command: it reads the JSON that
// `tofu providers schema -json` prints and turns one provider resource's
// schema into the input definitions of the catalogue type that maps onto it,
// one to one. The schema gives each input's name, whether it is required and
// whether it is sensitive. It carries no replacement rule, so the type's
// ProviderResource names the inputs whose change replaces the resource, and
// every other input updates it in place.
//
// The seams are GenerateInputs(schemaJSON, providerResource) and
// Run(args, stdout, stderr), so the whole command is testable without a
// process. The committed extract under testdata holds only the provider
// resources the one-to-one types map to, cut from the schema OpenTofu v1.9.1
// printed for hashicorp/aws 6.55.0, integrations/github 6.13.0 and
// hetznercloud/hcloud 1.69.0.

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

const fixtureProvider = "registry.opentofu.org/acme/widgets"

// fixtureSchema is a small provider schema in the shape OpenTofu prints. Its
// one resource has a required input, a sensitive optional input, an input
// whose change forces replacement, a computed-only attribute, the provider
// framework's id, a deprecated attribute, a timeouts block, a required nested
// block and an optional nested block holding a sensitive attribute.
const fixtureSchema = `{
  "format_version": "1.0",
  "provider_schemas": {
    "registry.opentofu.org/acme/widgets": {
      "resource_schemas": {
        "widgets_widget": {
          "version": 0,
          "block": {
            "attributes": {
              "name":        {"type": "string", "required": true, "description": "The name."},
              "size":        {"type": "number", "optional": true, "computed": true},
              "zone":        {"type": "string", "required": true},
              "api_key":     {"type": "string", "optional": true, "sensitive": true},
              "arn":         {"type": "string", "computed": true},
              "id":          {"type": "string", "optional": true, "computed": true},
              "legacy_mode": {"type": "bool", "optional": true, "deprecated": true}
            },
            "block_types": {
              "timeouts": {"nesting_mode": "single", "block": {"attributes": {"create": {"type": "string", "optional": true}}}},
              "placement": {"nesting_mode": "list", "min_items": 1, "max_items": 1,
                "block": {"attributes": {"rack": {"type": "string", "required": true}}}},
              "auth": {"nesting_mode": "list", "max_items": 1,
                "block": {"attributes": {"password": {"type": "string", "optional": true, "sensitive": true}}}}
            }
          }
        },
        "widgets_other": {"version": 0, "block": {"attributes": {"label": {"type": "string", "required": true}}}}
      }
    }
  }
}`

func widget(replaceOn ...string) contract.ProviderResource {
	return contract.ProviderResource{Provider: fixtureProvider, Resource: "widgets_widget", ReplaceOn: append([]string{}, replaceOn...)}
}

func TestGenerateInputs_ReadsEachInputFromTheProviderSchema(t *testing.T) {
	got, err := GenerateInputs([]byte(fixtureSchema), widget("zone"))
	if err != nil {
		t.Fatalf("GenerateInputs: %v", err)
	}
	want := []contract.Input{
		{Name: "api_key", Required: false, Secret: true, Accepts: "value", Replacement: "update"},
		{Name: "auth", Required: false, Secret: true, Accepts: "value", Replacement: "update"},
		{Name: "name", Required: true, Secret: false, Accepts: "value", Replacement: "update"},
		{Name: "placement", Required: true, Secret: false, Accepts: "value", Replacement: "update"},
		{Name: "size", Required: false, Secret: false, Accepts: "value", Replacement: "update"},
		{Name: "zone", Required: true, Secret: false, Accepts: "value", Replacement: "replace"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GenerateInputs =\n%+v\nwant\n%+v", got, want)
	}
}

func TestGenerateInputs_LeavesOutComputedOnlyAttributesTheIDDeprecatedAttributesAndTimeouts(t *testing.T) {
	got, err := GenerateInputs([]byte(fixtureSchema), widget())
	if err != nil {
		t.Fatalf("GenerateInputs: %v", err)
	}
	for _, in := range got {
		switch in.Name {
		case "arn", "id", "legacy_mode", "timeouts":
			t.Errorf("GenerateInputs emits %q, which is not an input a type declares", in.Name)
		}
	}
}

func TestGenerateInputs_MarksOnlyTheReplaceOnInputsAsReplacing(t *testing.T) {
	got, err := GenerateInputs([]byte(fixtureSchema), widget("name", "placement"))
	if err != nil {
		t.Fatalf("GenerateInputs: %v", err)
	}
	for _, in := range got {
		want := "update"
		if in.Name == "name" || in.Name == "placement" {
			want = "replace"
		}
		if in.Replacement != want {
			t.Errorf("input %q: replacement %q, want %q", in.Name, in.Replacement, want)
		}
	}
}

func TestGenerateInputs_SortsTheInputsByName(t *testing.T) {
	got, err := GenerateInputs([]byte(fixtureSchema), widget())
	if err != nil {
		t.Fatalf("GenerateInputs: %v", err)
	}
	names := make([]string, len(got))
	for i, in := range got {
		names[i] = in.Name
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("inputs come out as %q, want them sorted by name", names)
	}
}

func TestGenerateInputs_RefusesWhatItCannotRead(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		pr     contract.ProviderResource
		want   []string
	}{
		{"a provider the schema does not hold", fixtureSchema,
			contract.ProviderResource{Provider: "registry.opentofu.org/acme/nothing", Resource: "widgets_widget"},
			[]string{"registry.opentofu.org/acme/nothing"}},
		{"a resource the provider does not hold", fixtureSchema,
			contract.ProviderResource{Provider: fixtureProvider, Resource: "widgets_gadget"},
			[]string{"widgets_gadget"}},
		{"a replaceOn name that is no input", fixtureSchema, widget("arn"),
			[]string{`"arn"`, "replace"}},
		{"input that is not JSON", "not json", widget(), []string{"schema"}},
		{"JSON that is not a provider schema", `{"resource_schemas": {}}`, widget(), []string{"provider_schemas"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := GenerateInputs([]byte(c.schema), c.pr)
			if err == nil {
				t.Fatalf("GenerateInputs = %+v, nil; want an error", got)
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not name %q", err, w)
				}
			}
		})
	}
}

// oneToOne pins which released types map one to one onto an OpenTofu
// provider resource, and onto which. Every other type is a composite or has
// no provider resource, and carries hand-written inputs.
var oneToOne = map[string]contract.ProviderResource{
	"aws.ebs-volume":            {Provider: "registry.opentofu.org/hashicorp/aws", Resource: "aws_ebs_volume"},
	"aws.ecr-repository":        {Provider: "registry.opentofu.org/hashicorp/aws", Resource: "aws_ecr_repository"},
	"aws.iam-oidc-provider":     {Provider: "registry.opentofu.org/hashicorp/aws", Resource: "aws_iam_openid_connect_provider"},
	"aws.iam-role":              {Provider: "registry.opentofu.org/hashicorp/aws", Resource: "aws_iam_role"},
	"aws.route53-record":        {Provider: "registry.opentofu.org/hashicorp/aws", Resource: "aws_route53_record"},
	"aws.security-group":        {Provider: "registry.opentofu.org/hashicorp/aws", Resource: "aws_security_group"},
	"aws.ssm-association":       {Provider: "registry.opentofu.org/hashicorp/aws", Resource: "aws_ssm_association"},
	"aws.ssm-parameter":         {Provider: "registry.opentofu.org/hashicorp/aws", Resource: "aws_ssm_parameter"},
	"aws.vpc":                   {Provider: "registry.opentofu.org/hashicorp/aws", Resource: "aws_vpc"},
	"github.actions-secret":     {Provider: "registry.opentofu.org/integrations/github", Resource: "github_actions_secret"},
	"github.actions-variable":   {Provider: "registry.opentofu.org/integrations/github", Resource: "github_actions_variable"},
	"github.pull-request":       {Provider: "registry.opentofu.org/integrations/github", Resource: "github_repository_pull_request"},
	"github.repository":         {Provider: "registry.opentofu.org/integrations/github", Resource: "github_repository"},
	"github.repository-webhook": {Provider: "registry.opentofu.org/integrations/github", Resource: "github_repository_webhook"},
	"github.runner-group":       {Provider: "registry.opentofu.org/integrations/github", Resource: "github_actions_runner_group"},
	"github.workflow-file":      {Provider: "registry.opentofu.org/integrations/github", Resource: "github_repository_file"},
	"hetzner.firewall":          {Provider: "registry.opentofu.org/hetznercloud/hcloud", Resource: "hcloud_firewall"},
	"hetzner.primary-ip":        {Provider: "registry.opentofu.org/hetznercloud/hcloud", Resource: "hcloud_primary_ip"},
	"hetzner.server":            {Provider: "registry.opentofu.org/hetznercloud/hcloud", Resource: "hcloud_server"},
	"hetzner.ssh-key":           {Provider: "registry.opentofu.org/hetznercloud/hcloud", Resource: "hcloud_ssh_key"},
	"hetzner.volume":            {Provider: "registry.opentofu.org/hetznercloud/hcloud", Resource: "hcloud_volume"},
}

const extractPath = "testdata/provider-schema-extract.json"

func readExtract(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(extractPath)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCatalogue_TheOneToOneTypesAreThePinnedSet(t *testing.T) {
	got := map[string]contract.ProviderResource{}
	for _, rt := range contract.ResourceTypes() {
		if rt.ProviderResource != nil {
			got[rt.ID] = contract.ProviderResource{Provider: rt.ProviderResource.Provider, Resource: rt.ProviderResource.Resource}
		}
	}
	if !reflect.DeepEqual(got, oneToOne) {
		t.Errorf("types mapped onto a provider resource =\n%v\nwant\n%v", got, oneToOne)
	}
}

// The checked-in inputs of every one-to-one type are exactly what the command
// generates from the committed schema extract, so a hand edit to them, or a
// schema change nobody regenerated for, fails here.
func TestCatalogue_EachOneToOneTypeCarriesTheInputsGeneratedFromItsProviderSchema(t *testing.T) {
	raw := readExtract(t)
	mapped := 0
	for _, rt := range contract.ResourceTypes() {
		if rt.ProviderResource == nil {
			continue
		}
		mapped++
		t.Run(rt.ID, func(t *testing.T) {
			want, err := GenerateInputs(raw, *rt.ProviderResource)
			if err != nil {
				t.Fatalf("GenerateInputs(%+v): %v", *rt.ProviderResource, err)
			}
			if len(want) == 0 {
				t.Fatalf("the extract gives %s no inputs", rt.ID)
			}
			if !reflect.DeepEqual(rt.Inputs, want) {
				t.Errorf("checked-in inputs =\n%+v\ngenerated =\n%+v", rt.Inputs, want)
			}
		})
	}
	if mapped == 0 {
		t.Fatal("no released type maps onto a provider resource")
	}
}

// The extract holds the provider resources the one-to-one types map onto and
// nothing else, so it stays a small cut of the provider schemas.
func TestSchemaExtract_HoldsOnlyTheMappedProviderResources(t *testing.T) {
	var doc struct {
		FormatVersion   string `json:"format_version"`
		ProviderSchemas map[string]struct {
			ResourceSchemas map[string]json.RawMessage `json:"resource_schemas"`
			DataSources     map[string]json.RawMessage `json:"data_source_schemas"`
			Provider        json.RawMessage            `json:"provider"`
		} `json:"provider_schemas"`
	}
	if err := json.Unmarshal(readExtract(t), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.FormatVersion == "" {
		t.Error("the extract carries no format_version, so it is not in the shape tofu prints")
	}
	got := map[string]bool{}
	for p, ps := range doc.ProviderSchemas {
		if len(ps.DataSources) != 0 || len(ps.Provider) != 0 {
			t.Errorf("provider %s carries data sources or provider configuration; the extract holds resources only", p)
		}
		for r := range ps.ResourceSchemas {
			got[p+" "+r] = true
		}
	}
	want := map[string]bool{}
	for _, pr := range oneToOne {
		want[pr.Provider+" "+pr.Resource] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extract resources = %v, want exactly %v", got, want)
	}
}

func TestRun_PrintsTheGeneratedInputsOfEveryOneToOneType(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"-schema", extractPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("Run exit %d, want 0; stderr: %s", code, stderr.String())
	}
	var got map[string][]contract.Input
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not a JSON object of type id to inputs: %v\n%s", err, stdout.String())
	}
	if len(got) != len(oneToOne) {
		t.Errorf("Run printed %d types, want %d", len(got), len(oneToOne))
	}
	raw := readExtract(t)
	for id, pr := range oneToOne {
		rt, ok := contract.LookupResourceType(id)
		if !ok || rt.ProviderResource == nil {
			t.Errorf("%s is not a released one-to-one type", id)
			continue
		}
		want, err := GenerateInputs(raw, *rt.ProviderResource)
		if err != nil {
			t.Errorf("GenerateInputs(%+v): %v", pr, err)
			continue
		}
		if !reflect.DeepEqual(got[id], want) {
			t.Errorf("Run printed for %s\n%+v\nwant\n%+v", id, got[id], want)
		}
	}
}

func TestRun_RefusesASchemaMissingAMappedResourceNamingIt(t *testing.T) {
	path := t.TempDir() + "/schema.json"
	if err := os.WriteFile(path, []byte(fixtureSchema), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"-schema", path}, &stdout, &stderr); code != 1 {
		t.Fatalf("Run exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "aws_ebs_volume") || !strings.Contains(stderr.String(), "aws.ebs-volume") {
		t.Errorf("stderr %q does not name the missing resource aws_ebs_volume and its type aws.ebs-volume", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("Run printed %q despite failing", stdout.String())
	}
}

func TestRun_WithoutASchemaIsAUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(nil, &stdout, &stderr); code != 2 {
		t.Errorf("Run with no arguments exit %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "-schema") {
		t.Errorf("stderr %q does not name the -schema flag", stderr.String())
	}
}

func TestRun_AnUnreadableSchemaFileFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"-schema", t.TempDir() + "/absent.json"}, &stdout, &stderr); code != 1 {
		t.Errorf("Run with a missing schema file exit %d, want 1", code)
	}
}

package contract_test

// blocksteps_test.go pins the block-steps contract ADR 0079 fixes: a block
// lists the resources it creates as ordered steps, requires only resources,
// each matched by kind or narrowed to one type and met by one provider or
// many, offers a resource by naming the step that creates it, and derives its
// capabilities from the types of its steps and narrowed requirements.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

// validBlockSteps returns a fresh declaration every rule accepts: a param,
// steps binding a param, an earlier step's output and a required resource's
// output, a kind requirement, a narrowed many-provider requirement and an
// offer.
func validBlockSteps() contract.BlockSteps {
	return contract.BlockSteps{
		Params: []string{"appName"},
		Requires: []contract.ResourceRequirement{
			{Resource: "image", Kind: "container-image", Reads: []string{"artifact"}},
			{Resource: "workflows", Type: "github.workflow-file", Many: true},
		},
		Steps: []contract.Step{
			{ID: "registry", Type: "aws.ecr-repository", Ownership: contract.OwnershipCreated,
				Inputs: map[string]string{"name": "params.appName"}},
			{ID: "build-image", Type: "aws.ecr-image", Ownership: contract.OwnershipCreated,
				Inputs: map[string]string{
					"registry": "steps.registry.outputs.ecrRegistry",
					"base":     "requires.image.outputs.artifact",
				}},
		},
		Offers: []contract.Offer{{Step: "build-image"}},
	}
}

func TestValidateBlockSteps_AcceptsAValidDeclaration(t *testing.T) {
	if err := contract.ValidateBlockSteps(validBlockSteps()); err != nil {
		t.Fatalf("ValidateBlockSteps(valid) = %v, want nil", err)
	}
}

func TestValidateBlockSteps_AcceptsAnEmptyDeclaration(t *testing.T) {
	if err := contract.ValidateBlockSteps(contract.BlockSteps{}); err != nil {
		t.Fatalf("ValidateBlockSteps(empty) = %v, want nil", err)
	}
}

func TestValidateBlockSteps_RefusesEachDefectNamingWhatItIsAbout(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*contract.BlockSteps)
		want   []string
	}{
		{"uncatalogued step type", func(b *contract.BlockSteps) {
			b.Steps[0].Type = "aws.not-a-type"
		}, []string{`step "registry"`, `"aws.not-a-type"`}},
		{"step without ownership", func(b *contract.BlockSteps) {
			b.Steps[0].Ownership = ""
		}, []string{`step "registry"`, "no ownership"}},
		{"step with an invalid ownership", func(b *contract.BlockSteps) {
			b.Steps[0].Ownership = "borrowed"
		}, []string{`step "registry"`, "ownership", `"borrowed"`}},
		{"step with an empty id", func(b *contract.BlockSteps) {
			b.Steps[0].ID = ""
			b.Steps[1].Inputs = map[string]string{"base": "requires.image.outputs.artifact"}
		}, []string{"step at index 0", "id"}},
		{"duplicate step id", func(b *contract.BlockSteps) {
			b.Steps[1].ID = "registry"
			b.Steps[1].Inputs = nil
			b.Offers = nil
		}, []string{`step "registry"`, "more than once"}},
		{"binding in no known form", func(b *contract.BlockSteps) {
			b.Steps[0].Inputs = map[string]string{"name": "appName"}
		}, []string{`step "registry"`, `input "name"`, `"appName"`}},
		{"undeclared param", func(b *contract.BlockSteps) {
			b.Steps[0].Inputs = map[string]string{"name": "params.region"}
		}, []string{`step "registry"`, `input "name"`, `param "region"`}},
		{"binding to a later step", func(b *contract.BlockSteps) {
			b.Steps[0].Inputs = map[string]string{"name": "steps.build-image.outputs.artifact"}
		}, []string{`step "registry"`, `input "name"`, `"build-image"`, "earlier step"}},
		{"binding to the step itself", func(b *contract.BlockSteps) {
			b.Steps[0].Inputs = map[string]string{"name": "steps.registry.outputs.ecrRegistry"}
		}, []string{`step "registry"`, `input "name"`, `"registry"`, "earlier step"}},
		{"binding to an output the earlier step's type lacks", func(b *contract.BlockSteps) {
			b.Steps[1].Inputs = map[string]string{"registry": "steps.registry.outputs.imageRepo"}
		}, []string{`step "build-image"`, `input "registry"`, `"imageRepo"`, `"aws.ecr-repository"`}},
		{"binding to an undeclared requirement", func(b *contract.BlockSteps) {
			b.Steps[1].Inputs = map[string]string{"base": "requires.runner.outputs.runsOn"}
		}, []string{`step "build-image"`, `input "base"`, `requirement "runner"`}},
		{"binding to an output missing from the requirement's reads", func(b *contract.BlockSteps) {
			b.Steps[1].Inputs = map[string]string{"base": "requires.image.outputs.imageRepo"}
		}, []string{`step "build-image"`, `input "base"`, `"imageRepo"`, "reads"}},
		{"requirement with neither kind nor type", func(b *contract.BlockSteps) {
			b.Requires[0].Kind = ""
		}, []string{`requirement "image"`, "neither", "kind", "type"}},
		{"requirement with both kind and type", func(b *contract.BlockSteps) {
			b.Requires[0].Type = "aws.ecr-image"
		}, []string{`requirement "image"`, "kind", "type"}},
		{"requirement with an empty id", func(b *contract.BlockSteps) {
			b.Requires[1].Resource = ""
		}, []string{"requirement at index 1", "resource"}},
		{"duplicate requirement id", func(b *contract.BlockSteps) {
			b.Requires[1].Resource = "image"
		}, []string{`requirement "image"`, "more than once"}},
		{"requirement narrowed to an uncatalogued type", func(b *contract.BlockSteps) {
			b.Requires[1].Type = "github.not-a-type"
		}, []string{`requirement "workflows"`, `"github.not-a-type"`}},
		{"requirement of a kind no catalogued type has", func(b *contract.BlockSteps) {
			b.Requires[0].Kind = "message-queue"
			b.Requires[0].Reads = nil
			b.Steps[1].Inputs = nil
		}, []string{`requirement "image"`, `kind "message-queue"`}},
		{"requirement reading an output its kind lacks", func(b *contract.BlockSteps) {
			b.Requires[0].Reads = []string{"runsOn"}
			b.Steps[1].Inputs = nil
		}, []string{`requirement "image"`, `"runsOn"`, `kind "container-image"`}},
		{"kind requirement whose reads no single type of the kind outputs", func(b *contract.BlockSteps) {
			// aws.ec2-instance outputs publicIp but not cloud.region, and
			// hetzner.server the reverse, so no host type meets both reads.
			b.Requires[0].Kind = "host"
			b.Requires[0].Reads = []string{"publicIp", "cloud.region"}
			b.Steps[1].Inputs = nil
		}, []string{`requirement "image"`, `kind "host"`, `"publicIp"`, `"cloud.region"`}},
		{"binding to a many-provider requirement", func(b *contract.BlockSteps) {
			b.Requires[1].Reads = []string{"workflowPath"}
			b.Steps[1].Inputs = map[string]string{"path": "requires.workflows.outputs.workflowPath"}
		}, []string{`step "build-image"`, `input "path"`, `requirement "workflows"`, "many"}},
		{"narrowed requirement reading an output its type lacks", func(b *contract.BlockSteps) {
			b.Requires[1].Reads = []string{"repo"}
		}, []string{`requirement "workflows"`, `"repo"`, `"github.workflow-file"`}},
		{"offer naming no declared step", func(b *contract.BlockSteps) {
			b.Offers = []contract.Offer{{Step: "push"}}
		}, []string{"offer", `"push"`}},
		{"offer naming no step at all", func(b *contract.BlockSteps) {
			b.Offers = []contract.Offer{{Step: ""}}
		}, []string{"offer", "step"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := validBlockSteps()
			tc.mutate(&b)
			err := contract.ValidateBlockSteps(b)
			if err == nil {
				t.Fatalf("ValidateBlockSteps accepted a declaration with %s", tc.name)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}

func TestValidateBlockSteps_ReportsEveryDefectNotOnlyTheFirst(t *testing.T) {
	b := validBlockSteps()
	b.Steps[0].Ownership = ""
	b.Offers = []contract.Offer{{Step: "push"}}
	err := contract.ValidateBlockSteps(b)
	if err == nil {
		t.Fatal("ValidateBlockSteps accepted two defects")
	}
	for _, w := range []string{"ownership", `"push"`} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not report the defect naming %q", err, w)
		}
	}
}

func TestResourceRequirement_KindMatchesEveryTypeOfThatKindThatOutputsTheReads(t *testing.T) {
	req := contract.ResourceRequirement{Resource: "image", Kind: "container-image", Reads: []string{"artifact"}}
	for _, id := range []string{"aws.ecr-image", "github.container-image"} {
		typ, ok := contract.LookupResourceType(id)
		if !ok {
			t.Fatalf("%s is not catalogued", id)
		}
		if !req.Matches(typ) {
			t.Errorf("kind container-image reading artifact does not match %s", id)
		}
	}
}

func TestResourceRequirement_KindRefusesAnotherKindAndAMissingRead(t *testing.T) {
	repo, _ := contract.LookupResourceType("aws.ecr-repository")
	req := contract.ResourceRequirement{Resource: "image", Kind: "container-image"}
	if req.Matches(repo) {
		t.Error("kind container-image matches aws.ecr-repository, of kind container-repository")
	}
	img, _ := contract.LookupResourceType("aws.ecr-image")
	req.Reads = []string{"runsOn"}
	if req.Matches(img) {
		t.Error("a requirement reading runsOn matches aws.ecr-image, which does not output it")
	}
}

func TestResourceRequirement_NarrowedToATypeRefusesEveryOtherType(t *testing.T) {
	req := contract.ResourceRequirement{Resource: "image", Type: "aws.ecr-image", Reads: []string{"artifact"}}
	ecr, _ := contract.LookupResourceType("aws.ecr-image")
	ghcr, ok := contract.LookupResourceType("github.container-image")
	if !ok {
		t.Fatal("github.container-image is not catalogued")
	}
	if !req.Matches(ecr) {
		t.Error("a requirement narrowed to aws.ecr-image does not match it")
	}
	if req.Matches(ghcr) {
		t.Error("a requirement narrowed to aws.ecr-image matches github.container-image")
	}
}

func TestResourceTypes_GitHubContainerImageIsTheGHCRType(t *testing.T) {
	got, ok := contract.LookupResourceType("github.container-image")
	if !ok {
		t.Fatal("github.container-image is not catalogued")
	}
	want := contract.ResourceType{
		ID: "github.container-image", Kind: "container-image", Capability: "github",
		Category: "artifact-registry", Languages: []string{"any-container"},
		ArtifactType: "container-image", Lifecycle: "replaceable", Scope: "per-app",
		Outputs: []string{"artifact", "imageRepo"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("github.container-image = %+v, want %+v", got, want)
	}
}

func TestDeriveCapabilities_IsTheUnionOfStepAndNarrowedRequirementCapabilities(t *testing.T) {
	steps := []contract.Step{
		{ID: "registry", Type: "aws.ecr-repository", Ownership: contract.OwnershipCreated},
		{ID: "workflow", Type: "github.workflow-file", Ownership: contract.OwnershipCreated},
	}
	if got, want := contract.DeriveCapabilities(steps, nil), []string{"aws", "github"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DeriveCapabilities(ecr-repository, workflow-file) = %v, want %v", got, want)
	}

	requires := []contract.ResourceRequirement{
		{Resource: "image", Kind: "container-image"},
		{Resource: "claim", Type: "crossplane.reconcile-claim"},
	}
	got := contract.DeriveCapabilities(steps[1:], requires)
	if want := []string{"crossplane", "github"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DeriveCapabilities = %v, want %v: a kind requirement adds nothing, a narrowed one adds its type's capability", got, want)
	}
}

func TestDeriveCapabilities_HoldsEachCapabilityOnceAndIsNeverNil(t *testing.T) {
	steps := []contract.Step{
		{ID: "a", Type: "aws.ecr-repository", Ownership: contract.OwnershipCreated},
		{ID: "b", Type: "aws.ecr-image", Ownership: contract.OwnershipCreated},
	}
	if got, want := contract.DeriveCapabilities(steps, nil), []string{"aws"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DeriveCapabilities = %v, want %v", got, want)
	}
	if got := contract.DeriveCapabilities(nil, nil); got == nil || len(got) != 0 {
		t.Errorf("DeriveCapabilities(nil, nil) = %#v, want an empty non-nil list", got)
	}
}

func TestResourceRequirement_MarshalsADR0079Keys(t *testing.T) {
	m := marshalToMap(t, contract.ResourceRequirement{
		Resource: "contributions", Type: "github.workflow-file", Reads: []string{"workflowPath"}, Many: true,
	})
	want := map[string]any{
		"resource": "contributions", "type": "github.workflow-file",
		"reads": []any{"workflowPath"}, "many": true,
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("ResourceRequirement marshals to %v, want %v", m, want)
	}
	m = marshalToMap(t, contract.ResourceRequirement{Resource: "image", Kind: "container-image"})
	if _, ok := m["many"]; ok {
		t.Errorf("a one-provider requirement marshals a many key: %v", m)
	}
}

func TestOffer_MarshalsTheStepKey(t *testing.T) {
	if m := marshalToMap(t, contract.Offer{Step: "build-image"}); !reflect.DeepEqual(m, map[string]any{"step": "build-image"}) {
		t.Errorf("Offer marshals to %v", m)
	}
}

func TestStepsManifestGolden_DeclaresWhatItsAccessorSays(t *testing.T) {
	raw := string(contract.StepsManifestGolden())
	if !strings.Contains(raw, "steps:") || !strings.Contains(raw, "- step: ") {
		t.Fatalf("the steps golden does not declare spec.steps and an offer:\n%s", raw)
	}
	d := contract.StepsManifestGoldenDeclares()
	if len(d.Steps) < 2 || len(d.Requires) < 2 || len(d.Offers) < 1 || len(d.Params) < 1 {
		t.Fatalf("StepsManifestGoldenDeclares() = %+v, want params, two or more steps and requirements, and an offer", d)
	}
	for _, s := range d.Steps {
		if !strings.Contains(raw, "id: "+s.ID) || !strings.Contains(raw, "type: "+s.Type) {
			t.Errorf("the golden does not declare step %q of type %q", s.ID, s.Type)
		}
	}
	var many bool
	for _, r := range d.Requires {
		if !strings.Contains(raw, "resource: "+r.Resource) {
			t.Errorf("the golden does not declare requirement %q", r.Resource)
		}
		many = many || r.Many
	}
	if !many {
		t.Error("the golden declares no many-provider requirement")
	}
	if err := contract.ValidateBlockSteps(d); err != nil {
		t.Errorf("the steps golden's declaration is refused: %v", err)
	}
	bindings := map[string]bool{}
	for _, s := range d.Steps {
		for _, ref := range s.Inputs {
			bindings[strings.SplitN(ref, ".", 2)[0]] = true
		}
	}
	for _, form := range []string{"params", "steps", "requires"} {
		if !bindings[form] {
			t.Errorf("the golden binds no input in the %s form", form)
		}
	}
}

func TestStepsManifestGoldenDeclares_ReturnsAFreshCopy(t *testing.T) {
	a := contract.StepsManifestGoldenDeclares()
	a.Steps[0].ID = "changed"
	if b := contract.StepsManifestGoldenDeclares(); b.Steps[0].ID == "changed" {
		t.Error("StepsManifestGoldenDeclares shares its steps with an earlier caller")
	}
}

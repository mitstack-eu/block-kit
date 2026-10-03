package contract_test

// resourcetype_declarations_test.go pins the declarations each resource type
// carries beside its facets: its inputs with secret marks and replacement
// rules, its lifecycle with a handover when stateful, its upgrade behaviour,
// how it is observed and proven absent, a time budget per resource verb, and
// the failures it can meet, each permanent or transient. A fixture type that
// lacks one is refused, naming the type and what it lacks.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

// requireRefusal checks that ValidateResourceTypes refuses rt with an error
// naming rt's ID and carrying every fragment of wants.
func requireRefusal(t *testing.T, rt contract.ResourceType, wants ...string) {
	t.Helper()
	err := contract.ValidateResourceTypes([]contract.ResourceType{rt})
	if err == nil {
		t.Fatalf("ValidateResourceTypes(%+v) = nil, want an error", rt)
	}
	if !strings.Contains(err.Error(), rt.ID) {
		t.Errorf("error %q does not name the type %q", err, rt.ID)
	}
	rest := strings.ReplaceAll(err.Error(), rt.ID, "")
	for _, w := range wants {
		if !strings.Contains(rest, w) {
			t.Errorf("error %q does not say %q", err, w)
		}
	}
}

func TestValidateResourceTypes_RefusesATypeLackingARequiredOutputOfItsKind(t *testing.T) {
	rt := validType()
	rt.Outputs = []string{}
	requireRefusal(t, rt, `"registryHost"`, `"container-repository"`, "required output")
}

func TestValidateResourceTypes_RefusesATypeProducingAnOutputItsKindDoesNotName(t *testing.T) {
	rt := validType()
	rt.Outputs = []string{"registryHost", "repo"}
	requireRefusal(t, rt, `"repo"`, `"container-repository"`)
}

func TestValidateResourceTypes_RefusesAnInputWithoutAReplacementRule(t *testing.T) {
	rt := validType()
	rt.Inputs[1].Replacement = ""
	requireRefusal(t, rt, `"image_tag_mutability"`, "replacement")
}

func TestValidateResourceTypes_RefusesAStatefulLifecycleWithoutAHandover(t *testing.T) {
	rt := validType()
	rt.Lifecycle, rt.Handover = "stateful", ""
	requireRefusal(t, rt, "stateful", "handover")
}

func TestValidateResourceTypes_RefusesATypeWithoutAnObserveDeclaration(t *testing.T) {
	rt := validType()
	rt.Observe = ""
	requireRefusal(t, rt, "observe")
}

func TestValidateResourceTypes_RefusesATypeWithoutAnAbsenceDeclaration(t *testing.T) {
	rt := validType()
	rt.Absence = ""
	requireRefusal(t, rt, "absence")
}

func TestValidateResourceTypes_RefusesATypeMissingATimeBudgetNamingTheVerb(t *testing.T) {
	for _, verb := range contract.ResourceVerbs() {
		t.Run("without "+verb, func(t *testing.T) {
			rt := validType()
			delete(rt.TimeBudgets, verb)
			requireRefusal(t, rt, "time budget", `"`+verb+`"`)
		})
		t.Run("zero "+verb, func(t *testing.T) {
			rt := validType()
			rt.TimeBudgets[verb] = 0
			requireRefusal(t, rt, "time budget", `"`+verb+`"`)
		})
	}
	t.Run("no time budgets at all", func(t *testing.T) {
		rt := validType()
		rt.TimeBudgets = nil
		requireRefusal(t, rt, "time budget")
	})
}

func TestResourceVerbs_AreEnsureObserveAndDelete(t *testing.T) {
	assertVocabulary(t, "ResourceVerbs", contract.ResourceVerbs, []string{"ensure", "observe", "delete"})
}

func TestUpgradeBehaviours_AreTheADR0048Values(t *testing.T) {
	assertVocabulary(t, "UpgradeBehaviours", contract.UpgradeBehaviours, []string{"in-place", "blue-green", "recreate"})
}

func TestInputReplacements_AreUpdateAndReplace(t *testing.T) {
	assertVocabulary(t, "InputReplacements", contract.InputReplacements, []string{"update", "replace"})
}

func TestHandovers_IncludeSnapshotAndAreReturnedFresh(t *testing.T) {
	hs := contract.Handovers()
	found := false
	for _, h := range hs {
		found = found || h == "snapshot"
		if h == "" {
			t.Error("Handovers() holds an empty value")
		}
	}
	if !found {
		t.Errorf("Handovers() = %q, want it to hold snapshot", hs)
	}
	assertVocabulary(t, "Handovers", contract.Handovers, append([]string{}, hs...))
}

func TestInput_MarshalsItsFields(t *testing.T) {
	m := marshalToMap(t, contract.Input{Name: "value", Required: true, Secret: true, Accepts: "value", Replacement: "update"})
	want := map[string]any{"name": "value", "required": true, "secret": true, "accepts": "value", "replacement": "update"}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("Input marshals to %v, want %v", m, want)
	}
	m = marshalToMap(t, contract.Input{Name: "tags", Accepts: "value", Replacement: "update"})
	if m["required"] != false || m["secret"] != false {
		t.Errorf("Input marshals to %v, want required and secret written as false", m)
	}
}

func TestFailure_MarshalsClassAndCause(t *testing.T) {
	m := marshalToMap(t, contract.Failure{Class: "permanent", Cause: "the name is taken"})
	if want := map[string]any{"class": "permanent", "cause": "the name is taken"}; !reflect.DeepEqual(m, want) {
		t.Errorf("Failure marshals to %v, want %v", m, want)
	}
}

func TestProviderResource_MarshalsProviderResourceAndReplaceOn(t *testing.T) {
	m := marshalToMap(t, contract.ProviderResource{
		Provider: "registry.opentofu.org/hashicorp/aws", Resource: "aws_vpc", ReplaceOn: []string{"cidr_block"},
	})
	want := map[string]any{
		"provider": "registry.opentofu.org/hashicorp/aws", "resource": "aws_vpc", "replaceOn": []any{"cidr_block"},
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("ProviderResource marshals to %v, want %v", m, want)
	}
}

// A secret input is one whose value is sensitive. Each type that takes a
// secret value marks it, and the generated inputs carry the provider's own
// sensitive mark, so aws.ssm-parameter and github.actions-secret each take a
// secret value.
func TestResourceTypes_SecretValuesAreMarkedSecretInputs(t *testing.T) {
	for _, c := range []struct{ id, input string }{
		{"aws.ssm-parameter", "value"},
		{"github.actions-secret", "value"},
	} {
		rt, ok := contract.LookupResourceType(c.id)
		if !ok {
			t.Errorf("the catalogue has no %q", c.id)
			continue
		}
		found := false
		for _, in := range rt.Inputs {
			if in.Name == c.input {
				found = true
				if !in.Secret {
					t.Errorf("%s: input %q is not marked secret", c.id, c.input)
				}
			}
		}
		if !found {
			t.Errorf("%s has no input %q", c.id, c.input)
		}
	}
}

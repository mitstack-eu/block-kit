package contract_test

// kubernetes_namespace_test.go pins the kubernetes.namespace type a cluster
// block places in an app's namespace, and the two registry entries its kind
// requires. The pinned tables in kinds_test.go and resourcetypes_test.go hold
// its kind, facets and outputs; this file holds what those tables do not
// reach: each entry's kind label and secret mark, and the type's one input,
// its upgrade behaviour and its being written by hand rather than mapped onto
// a provider resource.

import (
	"reflect"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

func TestLookupResource_NamespaceAndServiceAccountAreNonSecretCanonicalEntriesWithNoAliases(t *testing.T) {
	for _, c := range []struct{ name, kind string }{
		{"namespace", "Host & network"},
		{"serviceAccount", "Access"},
	} {
		r, ok := contract.LookupResource(c.name)
		if !ok {
			t.Errorf("LookupResource(%q) found nothing", c.name)
			continue
		}
		if r.Name != c.name {
			t.Errorf("LookupResource(%q).Name = %q, want its own canonical entry", c.name, r.Name)
		}
		if r.Kind != c.kind {
			t.Errorf("%q: Kind = %q, want %q", c.name, r.Kind, c.kind)
		}
		if r.Secret {
			t.Errorf("%q is secret, want it not secret", c.name)
		}
		if len(r.Aliases) != 0 {
			t.Errorf("%q: aliases = %q, want none", c.name, r.Aliases)
		}
	}
}

func TestResourceTypes_KubernetesNamespaceLivesInKubernetesAndTakesOneClusterFromHetzner(t *testing.T) {
	rt, ok := contract.LookupResourceType("kubernetes.namespace")
	if !ok {
		t.Fatal("the catalogue has no kubernetes.namespace")
	}
	if rt.Kind != "kubernetes-namespace" {
		t.Errorf("Kind = %q, want kubernetes-namespace", rt.Kind)
	}
	if rt.Place() != "kubernetes" {
		t.Errorf("Place() = %q, want kubernetes", rt.Place())
	}
	wantInputs := []contract.Input{{
		Name:        "cluster",
		Required:    true,
		Secret:      false,
		Accepts:     "resource:kubernetes-cluster",
		Place:       "capability:hetzner",
		Replacement: "replace",
	}}
	if !reflect.DeepEqual(rt.Inputs, wantInputs) {
		t.Errorf("Inputs = %+v, want exactly %+v", rt.Inputs, wantInputs)
	}
	if got, want := sortedCopy(rt.Outputs), []string{"namespace", "serviceAccount"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Outputs = %q, want exactly %q", rt.Outputs, want)
	}
	if rt.Upgrade != "in-place" {
		t.Errorf("Upgrade = %q, want in-place", rt.Upgrade)
	}
	if rt.Handover != "" {
		t.Errorf("Handover = %q, want none for a replaceable type", rt.Handover)
	}
	if rt.ProviderResource != nil {
		t.Errorf("ProviderResource = %+v, want none: the namespace and its service account are written by hand", rt.ProviderResource)
	}
}

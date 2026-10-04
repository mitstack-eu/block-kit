package contract_test

// resource_request_test.go pins the request half of running a verb on one
// declared resource: a request may name the resource it concerns under
// "resource" and carry that resource's inputs under "resourceInputs", each
// input a scalar or a mapping kept as raw JSON. Desired carries both across
// to the resource's handler. Inputs without a resource to bind them to are
// refused, naming the field. A response's ensure outcome may report the
// identity the ensure stored.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

// namedResourceInputs carries a scalar input and a mapping input.
var namedResourceInputs = map[string]any{
	"network": "vpc-0abc",
	"rules":   map[string]any{"ingress": "443", "egress": "all"},
}

// wantRawInputs checks got holds exactly the inputs namedResourceInputs
// names, each decoding to the same JSON value.
func wantRawInputs(t *testing.T, what string, got map[string]json.RawMessage) {
	t.Helper()
	if len(got) != len(namedResourceInputs) {
		t.Fatalf("%s holds %d inputs %v, want %d", what, len(got), got, len(namedResourceInputs))
	}
	for name, want := range namedResourceInputs {
		raw, ok := got[name]
		if !ok {
			t.Errorf("%s has no input %q", what, name)
			continue
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Errorf("%s input %q is not JSON: %v (%s)", what, name, err, raw)
			continue
		}
		if !reflect.DeepEqual(value, want) {
			t.Errorf("%s input %q = %s, want %v", what, name, raw, want)
		}
	}
}

func TestDecodeRequest_ReadsTheNamedResourceAndItsInputs(t *testing.T) {
	raw := mutateSubscribeGolden(t, func(m map[string]any) {
		m["resource"] = "firewall"
		m["resourceInputs"] = namedResourceInputs
	})

	req, err := contract.DecodeRequest(raw)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if req.Resource != "firewall" {
		t.Errorf("Resource = %q, want %q", req.Resource, "firewall")
	}
	wantRawInputs(t, "ResourceInputs", req.ResourceInputs)
}

func TestRequestDesired_CarriesTheNamedResourceAndItsInputs(t *testing.T) {
	raw := mutateSubscribeGolden(t, func(m map[string]any) {
		m["resource"] = "firewall"
		m["resourceInputs"] = namedResourceInputs
	})
	req, err := contract.DecodeRequest(raw)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}

	d := req.Desired()
	if d.Resource != "firewall" {
		t.Errorf("Desired().Resource = %q, want %q", d.Resource, "firewall")
	}
	wantRawInputs(t, "Desired().ResourceInputs", d.ResourceInputs)
	if d.SubscriptionID != req.SubscriptionID || !reflect.DeepEqual(d.Params, req.Params) {
		t.Errorf("Desired() lost the subscription or params: %+v", d)
	}
}

func TestDecodeRequest_NamingNoResourceLeavesTheResourceFieldsEmpty(t *testing.T) {
	for _, name := range []string{"subscribe", "upgrade", "unsubscribe", "validate"} {
		t.Run(name, func(t *testing.T) {
			req := decodeGolden(t, name)
			if req.Resource != "" || req.ResourceInputs != nil {
				t.Errorf("%s names no resource, but decodes to Resource %q and ResourceInputs %v", name, req.Resource, req.ResourceInputs)
			}
			d := req.Desired()
			if d.Resource != "" || d.ResourceInputs != nil {
				t.Errorf("%s names no resource, but Desired() carries Resource %q and ResourceInputs %v", name, d.Resource, d.ResourceInputs)
			}
		})
	}
}

func TestDecodeRequest_RefusesResourceInputsWithNoResourceNamingTheField(t *testing.T) {
	cases := map[string]func(m map[string]any){
		"no resource":    func(m map[string]any) { m["resourceInputs"] = namedResourceInputs },
		"empty resource": func(m map[string]any) { m["resource"] = ""; m["resourceInputs"] = namedResourceInputs },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := contract.DecodeRequest(mutateSubscribeGolden(t, mutate))
			if err == nil {
				t.Fatal("DecodeRequest accepted resource inputs with no resource to bind them to, want it refused")
			}
			if !strings.Contains(err.Error(), `"resourceInputs"`) {
				t.Errorf("DecodeRequest error = %q, want it to name the field %q", err.Error(), "resourceInputs")
			}
		})
	}
}

func TestDecodeRequest_AcceptsAResourceWithNoInputs(t *testing.T) {
	req, err := contract.DecodeRequest(mutateSubscribeGolden(t, func(m map[string]any) {
		m["resource"] = "network"
	}))
	if err != nil {
		t.Fatalf("DecodeRequest refused a request naming a resource with no inputs: %v", err)
	}
	if req.Resource != "network" || req.ResourceInputs != nil {
		t.Errorf("decoded Resource %q ResourceInputs %v, want %q and none", req.Resource, req.ResourceInputs, "network")
	}
}

func TestRequestMarshal_OmitsTheResourceFieldsWhenNoResourceIsNamed(t *testing.T) {
	raw, err := json.Marshal(contract.Request{Operation: "validate"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"resource`) {
		t.Errorf("json.Marshal = %s, want no resource or resourceInputs key for a request naming no resource", raw)
	}
}

func TestCallOutcome_EncodesIdentityOnlyWhenReported(t *testing.T) {
	raw, err := json.Marshal(contract.CallOutcome{Identity: map[string]string{"groupId": "sg-0abc"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), `{"identity":{"groupId":"sg-0abc"}}`; got != want {
		t.Errorf("json.Marshal = %s, want %s", got, want)
	}

	raw, err = json.Marshal(contract.CallOutcome{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), `{}`; got != want {
		t.Errorf("json.Marshal(zero CallOutcome) = %s, want %s: an ensure that reports no identity encodes as before", got, want)
	}
}

func TestDecodeResponse_ReadsTheIdentityAnEnsureReported(t *testing.T) {
	resp, err := contract.DecodeResponse([]byte(`{"resources":{"firewall":{"ensure":{"identity":{"groupId":"sg-0abc"},"outputs":{"groupArn":"arn:sg"}}}}}`))
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	ensure := resp.Resources["firewall"].Ensure
	if ensure == nil {
		t.Fatal("firewall has no ensure outcome")
	}
	if want := map[string]string{"groupId": "sg-0abc"}; !reflect.DeepEqual(ensure.Identity, want) {
		t.Errorf("ensure Identity = %v, want %v", ensure.Identity, want)
	}
	if want := map[string]string{"groupArn": "arn:sg"}; !reflect.DeepEqual(ensure.Outputs, want) {
		t.Errorf("ensure Outputs = %v, want %v", ensure.Outputs, want)
	}
}

func TestEnsured_HoldsTheIdentityAndOutputsAnEnsureReports(t *testing.T) {
	e := contract.Ensured{
		Identity: map[string]string{"groupId": "sg-0abc"},
		Outputs:  map[string]string{"groupArn": "arn:sg"},
	}
	if e.Identity["groupId"] != "sg-0abc" || e.Outputs["groupArn"] != "arn:sg" {
		t.Errorf("Ensured = %+v, want the identity and outputs it was given", e)
	}
}

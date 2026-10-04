package handler_test

// ensure_report_test.go pins what the Handler does with a declared resource
// that also implements handler.ReportingResource, and what a named
// resource's ensure is handed:
//
//   - the Handler calls EnsureWithReport in place of Ensure, never both,
//     whether or not the request names a resource;
//   - the resource's ensure entry carries the identity the ensure stored and
//     the outputs it published;
//   - an identity or output value equal to a request secret is refused as
//     secret-in-output, naming the key;
//   - a request naming a resource hands that resource's ensure a Desired
//     carrying the name and the request's resource inputs.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
)

// reportingResource is a recordedResource that also reports what its
// ensure stored and published. Its EnsureWithReport writes its own entry to
// the shared call log, so a log showing "<name> ensure" proves the plain
// Ensure ran too.
type reportingResource struct {
	*recordedResource
	identity  map[string]string
	published map[string]string
	desired   []contract.Desired
}

func (r *reportingResource) EnsureWithReport(ctx context.Context, d contract.Desired) (contract.Ensured, error) {
	r.log.add(r.name + " ensure with report")
	r.desired = append(r.desired, d)
	if err := r.FakeResource.Ensure(ctx, d); err != nil {
		return contract.Ensured{}, err
	}
	return contract.Ensured{Identity: r.identity, Outputs: r.published}, nil
}

var (
	_ handler.DeclaredResource  = (*reportingResource)(nil)
	_ handler.ReportingResource = (*reportingResource)(nil)
)

// firewallIdentity and firewallPublished are what the reporting firewall's
// ensure stores and publishes.
var (
	firewallIdentity  = map[string]string{"groupId": "sg-0abc"}
	firewallPublished = map[string]string{"groupArn": "arn:aws:ec2:eu-west-1:123456789012:security-group/sg-0abc"}
)

// withReportingFirewall swaps b's firewall for one that reports its ensure,
// returning it and the resources to serve.
func withReportingFirewall(b *threeBlock) (*reportingResource, map[string]handler.DeclaredResource) {
	f := &reportingResource{recordedResource: b.firewall, identity: firewallIdentity, published: firewallPublished}
	resources := b.resources()
	resources["firewall"] = f
	return f, resources
}

// --- the ensure report -----------------------------------------------------------

func TestNamedResource_TheEnsureEntryCarriesTheIdentityAndOutputsTheEnsureReported(t *testing.T) {
	for _, operation := range []string{"subscribe", "upgrade"} {
		t.Run(operation, func(t *testing.T) {
			b := newThreeBlock()
			if operation == "upgrade" {
				b.provision()
			}
			_, resources := withReportingFirewall(b)

			status, raw, resp := serveThree(t, resources, operation, map[string]any{"resource": "firewall"})
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %s", status, raw)
			}
			wantCalls(t, b.log, "firewall ensure with report", "firewall observe")
			wantUntouched(t, b.network, b.instance)
			wantOnlyEntries(t, resp, "firewall")

			f := outcomeOf(t, resp, "firewall")
			wantCallSucceeded(t, "firewall", "ensure", f.Ensure)
			if f.Ensure != nil {
				if !reflect.DeepEqual(f.Ensure.Identity, firewallIdentity) {
					t.Errorf("firewall ensure identity = %v, want the identity its ensure stored %v", f.Ensure.Identity, firewallIdentity)
				}
				if !reflect.DeepEqual(f.Ensure.Outputs, firewallPublished) {
					t.Errorf("firewall ensure outputs = %v, want the outputs its ensure published %v", f.Ensure.Outputs, firewallPublished)
				}
			}
			wantObserved(t, "firewall", f, contract.Present, map[string]string{"groupId": "sg-0abc"})
			if resp.Outputs["groupId"] != "sg-0abc" {
				t.Errorf("outputs = %v, want the named resource's groupId", resp.Outputs)
			}
			for _, key := range []string{"vpcId", "publicIp"} {
				if _, ok := resp.Outputs[key]; ok {
					t.Errorf("outputs = %v carry %q, which only another resource produces", resp.Outputs, key)
				}
			}
		})
	}
}

func TestReportingResource_ARequestNamingNoResourceCallsEnsureWithReportInPlaceOfEnsure(t *testing.T) {
	b := newThreeBlock()
	_, resources := withReportingFirewall(b)

	status, raw, resp := serveThree(t, resources, "subscribe", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", status, raw)
	}
	wantCalls(t, b.log,
		"network ensure", "network observe",
		"firewall ensure with report", "firewall observe",
		"instance ensure", "instance observe")

	f := outcomeOf(t, resp, "firewall")
	if f.Ensure == nil || !reflect.DeepEqual(f.Ensure.Identity, firewallIdentity) {
		t.Errorf("firewall ensure outcome = %+v, want identity %v", f.Ensure, firewallIdentity)
	}
	for _, name := range []string{"network", "instance"} {
		o := outcomeOf(t, resp, name)
		if o.Ensure == nil || o.Ensure.Identity != nil || o.Ensure.Outputs != nil {
			t.Errorf("resource %q ensure outcome = %+v, want an empty one: it reports nothing from its ensure", name, o.Ensure)
		}
	}
}

func TestReportingResource_AnEnsureWithReportErrorIsReportedUnderItsResource(t *testing.T) {
	b := newThreeBlock()
	f, resources := withReportingFirewall(b)
	f.EnsureErr = handler.Refuse("quota", "no security groups left in %s", "vpc-0abc")

	status, raw, resp := serveThree(t, resources, "subscribe", map[string]any{"resource": "firewall"})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a refusal; body %s", status, raw)
	}
	o := outcomeOf(t, resp, "firewall")
	if o.Ensure == nil || !strings.Contains(o.Ensure.Error, "no security groups left in vpc-0abc") {
		t.Errorf("firewall ensure outcome = %+v, want the refusal's error", o.Ensure)
	}
	if o.Ensure != nil && o.Ensure.Identity != nil {
		t.Errorf("firewall ensure outcome reports identity %v from an ensure that failed", o.Ensure.Identity)
	}
	if o.Observe != nil {
		t.Errorf("firewall reports an observe outcome %+v after its ensure failed", *o.Observe)
	}
}

// --- secrets -----------------------------------------------------------------------

func TestReportingResource_RefusesAnEnsureReportCarryingARequestSecret(t *testing.T) {
	cases := []struct {
		name      string
		identity  map[string]string
		published map[string]string
		key       string
	}{
		{"identity value", map[string]string{"groupId": guardSecret}, firewallPublished, "groupId"},
		{"output value", firewallIdentity, map[string]string{"groupArn": guardSecret}, "groupArn"},
	}
	for _, c := range cases {
		for _, named := range []bool{true, false} {
			label := c.name + " naming no resource"
			if named {
				label = c.name + " naming the resource"
			}
			t.Run(label, func(t *testing.T) {
				b := newThreeBlock()
				f, resources := withReportingFirewall(b)
				f.identity, f.published = c.identity, c.published
				extra := map[string]any{"secrets": guardSecrets()}
				if named {
					extra["resource"] = "firewall"
				}

				status, raw, resp := serveThree(t, resources, "subscribe", extra)
				if status != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400; body %s", status, raw)
				}
				if got := handler.RefusedClass(resp.StatusDetail); got != "secret-in-output" {
					t.Errorf("refusal class = %q, want %q", got, "secret-in-output")
				}
				if !strings.Contains(resp.Error, `"`+c.key+`"`) {
					t.Errorf("error = %q, want it to name the key %q", resp.Error, c.key)
				}
				if strings.Contains(string(raw), guardSecret) {
					t.Errorf("response carries a request secret: %s", raw)
				}
				if b.instance.EnsureCalls != 0 {
					t.Errorf("instance ensured %d times, want 0 after firewall's ensure was refused", b.instance.EnsureCalls)
				}
			})
		}
	}
}

// --- what the named resource's ensure is handed -----------------------------------

// requestInputs carries a scalar input and a mapping input.
var requestInputs = map[string]any{
	"network": "vpc-0abc",
	"rules":   map[string]any{"ingress": "443", "egress": "all"},
}

// wantDesiredFor checks d names resource and carries requestInputs, and
// still carries the subscription and params the request sent.
func wantDesiredFor(t *testing.T, d contract.Desired, resource string) {
	t.Helper()
	if d.Resource != resource {
		t.Errorf("Desired.Resource = %q, want %q", d.Resource, resource)
	}
	if len(d.ResourceInputs) != len(requestInputs) {
		t.Errorf("Desired.ResourceInputs = %v, want the %d inputs the request carried", d.ResourceInputs, len(requestInputs))
	}
	for name, want := range requestInputs {
		var got any
		if err := json.Unmarshal(d.ResourceInputs[name], &got); err != nil {
			t.Errorf("Desired.ResourceInputs[%q] = %s, not JSON: %v", name, d.ResourceInputs[name], err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Desired.ResourceInputs[%q] = %s, want %v", name, d.ResourceInputs[name], want)
		}
	}
	if d.SubscriptionID != "sub-1" || d.Params["appName"] != "myapp" {
		t.Errorf("Desired lost the request's subscription or params: %+v", d)
	}
}

func TestNamedResource_TheRequestsResourceAndInputsReachAReportingResourcesEnsure(t *testing.T) {
	b := newThreeBlock()
	f, resources := withReportingFirewall(b)

	status, raw, _ := serveThree(t, resources, "subscribe", map[string]any{
		"resource":       "firewall",
		"resourceInputs": requestInputs,
		"params":         map[string]any{"appName": "myapp"},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", status, raw)
	}
	if len(f.desired) != 1 {
		t.Fatalf("firewall's EnsureWithReport ran %d times, want 1", len(f.desired))
	}
	wantDesiredFor(t, f.desired[0], "firewall")
}

// capturingResource is a recordedResource that keeps each Desired its plain
// Ensure is handed.
type capturingResource struct {
	*recordedResource
	desired []contract.Desired
}

func (r *capturingResource) Ensure(ctx context.Context, d contract.Desired) error {
	r.desired = append(r.desired, d)
	return r.recordedResource.Ensure(ctx, d)
}

func TestNamedResource_TheRequestsResourceAndInputsReachAPlainResourcesEnsure(t *testing.T) {
	b := newThreeBlock()
	instance := &capturingResource{recordedResource: b.instance}
	resources := b.resources()
	resources["instance"] = instance

	status, raw, _ := serveThree(t, resources, "upgrade", map[string]any{
		"resource":       "instance",
		"resourceInputs": requestInputs,
		"params":         map[string]any{"appName": "myapp"},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", status, raw)
	}
	if len(instance.desired) != 1 {
		t.Fatalf("instance's Ensure ran %d times, want 1", len(instance.desired))
	}
	wantDesiredFor(t, instance.desired[0], "instance")
}

// errEnsureNotExpected fails an ensure the request never asked for, so a
// handler that ensures more than the named resource answers an error.
var errEnsureNotExpected = errors.New("unexpected ensure")

func TestNamedResource_ARequestNamingAResourceWithNoInputsHandsItsEnsureNone(t *testing.T) {
	b := newThreeBlock()
	instance := &capturingResource{recordedResource: b.instance}
	resources := b.resources()
	resources["instance"] = instance
	b.network.EnsureErr = errEnsureNotExpected

	status, raw, _ := serveThree(t, resources, "subscribe", map[string]any{"resource": "instance"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", status, raw)
	}
	if len(instance.desired) != 1 {
		t.Fatalf("instance's Ensure ran %d times, want 1", len(instance.desired))
	}
	if d := instance.desired[0]; d.Resource != "instance" || d.ResourceInputs != nil {
		t.Errorf("Desired names %q with inputs %v, want %q and none", d.Resource, d.ResourceInputs, "instance")
	}
}

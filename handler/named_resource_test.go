package handler_test

// named_resource_test.go pins how the Handler serves a request that names
// one of a block's declared resources under "resource":
//
//   - subscribe and upgrade ensure that resource alone and settle it
//     present, unsubscribe deletes it alone and settles it absent, and
//     validate observes it alone, once;
//   - the response's resources map holds that resource's entry alone, and
//     the top-level outputs are that resource's;
//   - a name the manifest does not declare, or any name sent to a block
//     served over a single Resource, answers 400 naming it and reaches no
//     resource;
//   - the idempotency cache keeps the answers for different named
//     resources apart under one subscription and idempotency key.

import (
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/kittest"
)

// threeResourceManifest declares network, firewall then instance, so
// creation order is network, firewall, instance and teardown order is
// instance, firewall, network.
const threeResourceManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: three-resources
  name: Three Resources
spec:
  version: 1.0.0
  resources:
    - name: network
      type: aws.vpc
    - name: firewall
      type: aws.security-group
      inputs: { network: resources.network }
    - name: instance
      type: aws.ec2-instance
      inputs: { securityGroup: resources.firewall }
  lifecycle:
    endpoint: https://three-resources.example/hook
    operations: [subscribe, unsubscribe, upgrade, validate]
    contract: kit/v1
`

// threeBlock is a converging block over threeResourceManifest whose three
// resources share one call log.
type threeBlock struct {
	network, firewall, instance *recordedResource
	log                         *callLog
}

func newThreeBlock() *threeBlock {
	log := &callLog{}
	return &threeBlock{
		network: &recordedResource{
			name: "network", log: log,
			FakeResource: &kittest.FakeResource{Outputs: map[string]string{"vpcId": "vpc-0abc"}},
		},
		firewall: &recordedResource{
			name: "firewall", log: log,
			FakeResource: &kittest.FakeResource{Outputs: map[string]string{"groupId": "sg-0abc"}},
		},
		instance: &recordedResource{
			name: "instance", log: log,
			FakeResource: &kittest.FakeResource{Outputs: map[string]string{"publicIp": "203.0.113.7"}},
		},
		log: log,
	}
}

// provision marks all three resources as already standing.
func (b *threeBlock) provision() {
	b.network.Count, b.firewall.Count, b.instance.Count = 1, 1, 1
}

func (b *threeBlock) resources() map[string]handler.DeclaredResource {
	return map[string]handler.DeclaredResource{"network": b.network, "firewall": b.firewall, "instance": b.instance}
}

// newThreeResourceHandler builds a Handler over resources under the
// three-resource manifest, with no single Resource.
func newThreeResourceHandler(t *testing.T, s *kittest.Signer, resources map[string]handler.DeclaredResource) *handler.Handler {
	t.Helper()
	return newHandler(t, nil, s, func(c *handler.Config) {
		c.Manifest = []byte(threeResourceManifest)
		c.Resources = resources
	})
}

// serveThree serves one verb over resources under the three-resource
// manifest and returns the status, the raw body and the decoded response.
func serveThree(t *testing.T, resources map[string]handler.DeclaredResource, operation string, extra map[string]any) (int, []byte, contract.Response) {
	t.Helper()
	s := kittest.NewSigner(t, "kid-three", 17)
	h := newThreeResourceHandler(t, s, resources)
	fields := map[string]any{"operation": operation}
	for k, v := range extra {
		fields[k] = v
	}
	status, raw := postHook(t, h, s, envelope(t, s, "nonce-three-"+operation, fields))
	assertConforms(t, raw)
	return status, raw, decodeResponse(t, raw)
}

// wantOnlyEntries fails unless the response's resources map holds exactly
// the named entries.
func wantOnlyEntries(t *testing.T, resp contract.Response, names ...string) {
	t.Helper()
	got := make([]string, 0, len(resp.Resources))
	for name := range resp.Resources {
		got = append(got, name)
	}
	sort.Strings(got)
	want := append([]string(nil), names...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("response reports resources %q, want only %q", got, want)
	}
}

// wantUntouched fails when any of the given resources saw a call.
func wantUntouched(t *testing.T, resources ...*recordedResource) {
	t.Helper()
	for _, r := range resources {
		if r.EnsureCalls+r.ObserveCalls+r.DeleteCalls != 0 {
			t.Errorf("resource %q saw ensure=%d observe=%d delete=%d, want no call to reach it",
				r.name, r.EnsureCalls, r.ObserveCalls, r.DeleteCalls)
		}
	}
}

// --- a request naming a declared resource -----------------------------------

func TestNamedResource_SubscribeAndUpgradeEnsureAndSettleTheNamedResourceAlone(t *testing.T) {
	for _, operation := range []string{"subscribe", "upgrade"} {
		t.Run(operation, func(t *testing.T) {
			b := newThreeBlock()
			if operation == "upgrade" {
				b.provision()
			}

			status, _, resp := serveThree(t, b.resources(), operation, map[string]any{"resource": "firewall"})
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200; response %+v", status, resp)
			}
			wantCalls(t, b.log, "firewall ensure", "firewall observe")
			wantUntouched(t, b.network, b.instance)
			wantOnlyEntries(t, resp, "firewall")

			f := outcomeOf(t, resp, "firewall")
			wantCallSucceeded(t, "firewall", "ensure", f.Ensure)
			wantObserved(t, "firewall", f, contract.Present, map[string]string{"groupId": "sg-0abc"})
			if f.Delete != nil {
				t.Errorf("firewall reports a delete outcome %+v on %s", *f.Delete, operation)
			}
			if resp.Pending {
				t.Error("pending = true, want false: the named resource is present")
			}
			if want := map[string]string{"groupId": "sg-0abc"}; !reflect.DeepEqual(resp.Outputs, want) {
				t.Errorf("outputs = %v, want the named resource's own outputs %v", resp.Outputs, want)
			}
		})
	}
}

func TestNamedResource_UnsubscribeDeletesAndSettlesTheNamedResourceAlone(t *testing.T) {
	b := newThreeBlock()
	b.provision()

	status, _, resp := serveThree(t, b.resources(), "unsubscribe", map[string]any{"resource": "firewall"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; response %+v", status, resp)
	}
	wantCalls(t, b.log, "firewall delete", "firewall observe")
	wantUntouched(t, b.network, b.instance)
	wantOnlyEntries(t, resp, "firewall")

	f := outcomeOf(t, resp, "firewall")
	wantCallSucceeded(t, "firewall", "delete", f.Delete)
	wantObserved(t, "firewall", f, contract.Absent, nil)
	if f.Ensure != nil {
		t.Errorf("firewall reports an ensure outcome %+v on unsubscribe", *f.Ensure)
	}
	if resp.Absent == nil || !*resp.Absent {
		t.Errorf("absent = %v, want true: the named resource was proved absent", resp.Absent)
	}
	if b.network.Count != 1 || b.instance.Count != 1 {
		t.Errorf("network count %d, instance count %d, want both still 1: only the named resource is torn down",
			b.network.Count, b.instance.Count)
	}
}

func TestNamedResource_ValidateObservesTheNamedResourceAloneOnce(t *testing.T) {
	b := newThreeBlock()
	b.firewall.Count = 1

	status, _, resp := serveThree(t, b.resources(), "validate", map[string]any{"resource": "firewall"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; response %+v", status, resp)
	}
	wantCalls(t, b.log, "firewall observe")
	wantUntouched(t, b.network, b.instance)
	wantOnlyEntries(t, resp, "firewall")

	f := outcomeOf(t, resp, "firewall")
	wantObserved(t, "firewall", f, contract.Present, map[string]string{"groupId": "sg-0abc"})
	if f.Ensure != nil || f.Delete != nil {
		t.Errorf("firewall reports an ensure or delete outcome on validate: %+v", f)
	}
	if resp.Pending {
		t.Error("pending = true, want false: the named resource is present, whatever the others are")
	}
	if resp.Absent == nil || *resp.Absent {
		t.Errorf("absent = %v, want false: the named resource is present", resp.Absent)
	}
	if want := map[string]string{"groupId": "sg-0abc"}; !reflect.DeepEqual(resp.Outputs, want) {
		t.Errorf("outputs = %v, want the named resource's own outputs %v", resp.Outputs, want)
	}
}

// --- a name the block cannot serve -------------------------------------------

func TestNamedResource_ANameTheManifestDoesNotDeclareAnswers400NamingItAndReachesNoResource(t *testing.T) {
	for _, operation := range []string{"subscribe", "upgrade", "unsubscribe", "validate"} {
		t.Run(operation, func(t *testing.T) {
			b := newThreeBlock()
			b.provision()

			status, raw, resp := serveThree(t, b.resources(), operation, map[string]any{"resource": "cache"})
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 for a resource the manifest does not declare; body %s", status, raw)
			}
			if !strings.Contains(resp.Error, `"cache"`) {
				t.Errorf("error = %q, want it to name the undeclared resource %q", resp.Error, "cache")
			}
			if len(b.log.calls) != 0 {
				t.Errorf("calls = %q, want none: no resource may be reached for an undeclared name", b.log.calls)
			}
			if len(resp.Resources) != 0 {
				t.Errorf("response reports resources %+v, want none: no resource was reached", resp.Resources)
			}
		})
	}
}

func TestNamedResource_ABlockServedOverOneResourceAnswers400ForAnyNamedResource(t *testing.T) {
	for _, operation := range []string{"subscribe", "upgrade", "unsubscribe", "validate"} {
		t.Run(operation, func(t *testing.T) {
			fake := &kittest.FakeResource{Count: 1, Outputs: map[string]string{"endpoint": "https://one.example"}}
			s := kittest.NewSigner(t, "kid-one-named", 19)
			h := newHandler(t, fake, s)

			status, raw := postHook(t, h, s, envelope(t, s, "nonce-one-named-"+operation, map[string]any{
				"operation": operation,
				"resource":  "network",
			}))
			assertConforms(t, raw)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: a block served over one Resource declares no resource to name; body %s", status, raw)
			}
			resp := decodeResponse(t, raw)
			if !strings.Contains(resp.Error, `"network"`) {
				t.Errorf("error = %q, want it to name the resource %q", resp.Error, "network")
			}
			zeroCalls(t, fake)
		})
	}
}

// --- idempotency ------------------------------------------------------------------

func TestNamedResource_OneIdempotencyKeyNamingDifferentResourcesRunsEachResource(t *testing.T) {
	b := newThreeBlock()
	s := kittest.NewSigner(t, "kid-named-idem", 23)
	h := newThreeResourceHandler(t, s, b.resources())
	deliver := func(nonce, resource string) (int, []byte) {
		t.Helper()
		return postHook(t, h, s, envelope(t, s, nonce, map[string]any{
			"operation":      "subscribe",
			"idempotencyKey": "idem-named-1",
			"resource":       resource,
		}))
	}

	status1, raw1 := deliver("nonce-named-idem-1", "network")
	status2, raw2 := deliver("nonce-named-idem-2", "firewall")
	status3, raw3 := deliver("nonce-named-idem-3", "firewall")

	if status1 != http.StatusOK || status2 != http.StatusOK {
		t.Fatalf("statuses = %d, %d, want 200 for both named resources; bodies %s %s", status1, status2, raw1, raw2)
	}
	if b.network.EnsureCalls != 1 || b.firewall.EnsureCalls != 1 || b.instance.EnsureCalls != 0 {
		t.Errorf("ensure calls network=%d firewall=%d instance=%d, want 1, 1, 0: each named resource runs once under the shared key",
			b.network.EnsureCalls, b.firewall.EnsureCalls, b.instance.EnsureCalls)
	}
	wantOnlyEntries(t, decodeResponse(t, raw1), "network")
	wantOnlyEntries(t, decodeResponse(t, raw2), "firewall")
	if status3 != status2 || string(raw3) != string(raw2) {
		t.Errorf("a repeat delivery naming firewall answered %d %s, want the replayed firewall answer %d %s", status3, raw3, status2, raw2)
	}
}

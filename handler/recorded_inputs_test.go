package handler_test

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
)

// requestIdentity is the identity firewall's ensure stored, as the request
// carries it under resourceIdentity.
var requestIdentity = map[string]any{"groupId": "sg-0abc", "vpcId": "vpc-0abc"}

// requestResource is a recordedResource that also implements
// handler.RequestObserver and handler.RequestDeleter, keeping the Desired
// each of those calls is handed and writing each to the shared call log
// under its own name, so a plain "observe" or "delete" entry proves the
// identity-only call ran instead. Its first unknownPolls observes report
// unknown, so a settle polls it more than once.
type requestResource struct {
	*recordedResource
	unknownPolls int
	observed     []contract.Desired
	deleted      []contract.Desired
}

func (r *requestResource) ObserveWithRequest(ctx context.Context, d contract.Desired) (contract.Observation, error) {
	r.log.add(r.name + " observe with request")
	r.observed = append(r.observed, d)
	obs, err := r.FakeResource.Observe(ctx, d.Identity)
	if err == nil && len(r.observed) <= r.unknownPolls {
		obs.State = contract.Unknown
	}
	return obs, err
}

func (r *requestResource) DeleteWithRequest(ctx context.Context, d contract.Desired) error {
	r.log.add(r.name + " delete with request")
	r.deleted = append(r.deleted, d)
	return r.FakeResource.Delete(ctx, d.Identity)
}

var (
	_ handler.DeclaredResource = (*requestResource)(nil)
	_ handler.RequestObserver  = (*requestResource)(nil)
	_ handler.RequestDeleter   = (*requestResource)(nil)
)

// withRequestFirewall swaps the block's firewall for one implementing
// RequestObserver and RequestDeleter whose first unknownPolls observes
// report unknown, and returns it with the block's resources.
func withRequestFirewall(b *threeBlock, unknownPolls int) (*requestResource, map[string]handler.DeclaredResource) {
	f := &requestResource{recordedResource: b.firewall, unknownPolls: unknownPolls}
	resources := b.resources()
	resources["firewall"] = f
	return f, resources
}

// namingFirewall is the request body naming firewall with its recorded
// inputs and stored identity.
func namingFirewall() map[string]any {
	return map[string]any{
		"resource":         "firewall",
		"resourceInputs":   requestInputs,
		"resourceIdentity": requestIdentity,
		"params":           map[string]any{"appName": "myapp"},
	}
}

// wantRecordedFor checks d is the verb's own Desired naming firewall: the
// request's subscription and params, its recorded inputs and the identity
// firewall's ensure stored.
func wantRecordedFor(t *testing.T, call string, d contract.Desired) {
	t.Helper()
	wantDesiredFor(t, d, "firewall")
	want := map[string]string{"groupId": "sg-0abc", "vpcId": "vpc-0abc"}
	if !reflect.DeepEqual(d.ResourceIdentity, want) {
		t.Errorf("%s was handed ResourceIdentity %v, want the stored identity %v", call, d.ResourceIdentity, want)
	}
}

func TestRecordedInputs_AnUnsubscribeNamingTheSecondOfThreeResourcesHandsItsDeleteAndEveryObservePollTheRecordedInputsAndIdentity(t *testing.T) {
	b := newThreeBlock()
	b.provision()
	f, resources := withRequestFirewall(b, 1)

	status, raw, resp := serveThree(t, resources, "unsubscribe", namingFirewall())
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", status, raw)
	}
	if resp.Absent == nil || !*resp.Absent {
		t.Errorf("absent = %v, want true: firewall settled absent on its second poll; body %s", resp.Absent, raw)
	}
	wantCalls(t, b.log, "firewall delete with request", "firewall observe with request", "firewall observe with request")
	wantUntouched(t, b.network, b.instance)
	wantOnlyEntries(t, resp, "firewall")

	if len(f.deleted) != 1 {
		t.Fatalf("firewall's DeleteWithRequest ran %d times, want 1", len(f.deleted))
	}
	wantRecordedFor(t, "DeleteWithRequest", f.deleted[0])
	if len(f.observed) != 2 {
		t.Fatalf("firewall's ObserveWithRequest ran %d times, want 2: one unknown poll, then absent", len(f.observed))
	}
	for i, d := range f.observed {
		wantRecordedFor(t, fmt.Sprintf("settle poll %d", i+1), d)
	}
}

func TestRecordedInputs_AValidateNamingOneResourceHandsItsObserveTheRecordedInputsAndIdentity(t *testing.T) {
	b := newThreeBlock()
	b.provision()
	f, resources := withRequestFirewall(b, 0)

	status, raw, resp := serveThree(t, resources, "validate", namingFirewall())
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", status, raw)
	}
	wantCalls(t, b.log, "firewall observe with request")
	wantUntouched(t, b.network, b.instance)
	wantOnlyEntries(t, resp, "firewall")
	wantObserved(t, "firewall", outcomeOf(t, resp, "firewall"), contract.Present, map[string]string{"groupId": "sg-0abc"})

	if len(f.observed) != 1 {
		t.Fatalf("firewall's ObserveWithRequest ran %d times, want 1", len(f.observed))
	}
	wantRecordedFor(t, "ObserveWithRequest", f.observed[0])
}

func TestRecordedInputs_AnUpgradeOrSubscribeNamingOneResourceHandsEveryObservePollTheRecordedInputsAndIdentity(t *testing.T) {
	for _, operation := range []string{"upgrade", "subscribe"} {
		t.Run(operation, func(t *testing.T) {
			b := newThreeBlock()
			if operation == "upgrade" {
				b.provision()
			}
			f, resources := withRequestFirewall(b, 1)

			status, raw, resp := serveThree(t, resources, operation, namingFirewall())
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %s", status, raw)
			}
			if resp.Pending {
				t.Errorf("pending = true, want false: firewall settled present on its second poll; body %s", raw)
			}
			wantCalls(t, b.log, "firewall ensure", "firewall observe with request", "firewall observe with request")
			wantUntouched(t, b.network, b.instance)
			wantOnlyEntries(t, resp, "firewall")

			if len(f.observed) != 2 {
				t.Fatalf("firewall's ObserveWithRequest ran %d times, want 2: one unknown poll, then present", len(f.observed))
			}
			for i, d := range f.observed {
				wantRecordedFor(t, fmt.Sprintf("settle poll %d", i+1), d)
			}
		})
	}
}

func TestRecordedInputs_AnUnsubscribeNamingNoResourceDeletesAndObservesThroughTheRequestCalls(t *testing.T) {
	b := newThreeBlock()
	b.provision()
	f, resources := withRequestFirewall(b, 0)

	status, raw, _ := serveThree(t, resources, "unsubscribe", map[string]any{"params": map[string]any{"appName": "myapp"}})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", status, raw)
	}
	wantCalls(t, b.log,
		"instance delete", "instance observe",
		"firewall delete with request", "firewall observe with request",
		"network delete", "network observe")

	if len(f.deleted) != 1 || len(f.observed) != 1 {
		t.Fatalf("firewall's DeleteWithRequest ran %d times and ObserveWithRequest %d, want 1 each", len(f.deleted), len(f.observed))
	}
	for call, d := range map[string]contract.Desired{"DeleteWithRequest": f.deleted[0], "ObserveWithRequest": f.observed[0]} {
		if d.SubscriptionID != "sub-1" || d.Params["appName"] != "myapp" {
			t.Errorf("%s was handed %+v, want the request's own subscription and params", call, d)
		}
		if d.Resource != "" || d.ResourceInputs != nil || d.ResourceIdentity != nil {
			t.Errorf("%s was handed Resource %q, ResourceInputs %v and ResourceIdentity %v, want all empty: the request names no resource",
				call, d.Resource, d.ResourceInputs, d.ResourceIdentity)
		}
	}
}

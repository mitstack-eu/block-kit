package handler_test

// declared_cancellation_test.go pins that a request naming no resource
// stops at a resource boundary once its context is done: before each
// resource's first call the Handler checks the request context, and once it
// is done no further resource is reached. subscribe and upgrade then answer
// pending, unsubscribe answers absent:false and validate answers pending,
// each naming the first resource not reached and reporting only the entries
// recorded before it.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/kittest"
)

// cancellingResource cancels the request context from inside its Observe
// once that Observe reports the state the verb settles on, so the verb sees
// a done context only after this resource has settled.
type cancellingResource struct {
	*recordedResource
	settledOn contract.State
	cancel    context.CancelFunc
}

func (r *cancellingResource) Observe(ctx context.Context, id contract.Identity) (contract.Observation, error) {
	obs, err := r.recordedResource.Observe(ctx, id)
	if err == nil && obs.State == r.settledOn {
		r.cancel()
	}
	return obs, err
}

// serveCancelling serves one verb over b's resources, with the resource
// named by cancelling cancelling the request context once it settles on
// settledOn.
func serveCancelling(t *testing.T, b *threeBlock, cancelling string, settledOn contract.State, operation string) (int, []byte, contract.Response) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resources := b.resources()
	inner := resources[cancelling].(*recordedResource)
	resources[cancelling] = &cancellingResource{recordedResource: inner, settledOn: settledOn, cancel: cancel}

	s := kittest.NewSigner(t, "kid-cancel", 31)
	h := newThreeResourceHandler(t, s, resources)
	status, raw := postHookWithContext(t, h, s, envelope(t, s, "nonce-cancel-"+operation, map[string]any{"operation": operation}), ctx)
	assertConforms(t, raw)
	return status, raw, decodeResponse(t, raw)
}

// wantDetailNaming fails unless the statusDetail names the resource the verb
// did not reach.
func wantDetailNaming(t *testing.T, resp contract.Response, name string) {
	t.Helper()
	if !strings.Contains(resp.StatusDetail, `"`+name+`"`) {
		t.Errorf("statusDetail = %q, want it to name %q, the first resource not reached", resp.StatusDetail, name)
	}
}

func TestDeclaredResources_SubscribeAndUpgradeStopBeforeTheNextResourceOnceTheContextIsDone(t *testing.T) {
	for _, operation := range []string{"subscribe", "upgrade"} {
		t.Run(operation, func(t *testing.T) {
			b := newThreeBlock()
			if operation == "upgrade" {
				b.provision()
			}

			status, raw, resp := serveCancelling(t, b, "network", contract.Present, operation)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %s", status, raw)
			}
			wantCalls(t, b.log, "network ensure", "network observe")
			wantUntouched(t, b.firewall, b.instance)
			if !resp.Pending {
				t.Error("pending = false, want true: firewall and instance were never reached")
			}
			wantDetailNaming(t, resp, "firewall")
			wantOnlyEntries(t, resp, "network")
			wantObserved(t, "network", outcomeOf(t, resp, "network"), contract.Present, nil)
		})
	}
}

func TestDeclaredResources_UnsubscribeStopsBeforeTheNextResourceOnceTheContextIsDone(t *testing.T) {
	b := newThreeBlock()
	b.provision()

	status, raw, resp := serveCancelling(t, b, "instance", contract.Absent, "unsubscribe")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", status, raw)
	}
	wantCalls(t, b.log, "instance delete", "instance observe")
	wantUntouched(t, b.network, b.firewall)
	if resp.Absent == nil || *resp.Absent {
		t.Errorf("absent = %v, want false: firewall and network were never torn down", resp.Absent)
	}
	wantDetailNaming(t, resp, "firewall")
	wantOnlyEntries(t, resp, "instance")
	wantObserved(t, "instance", outcomeOf(t, resp, "instance"), contract.Absent, nil)
}

func TestDeclaredResources_ValidateStopsBeforeTheNextResourceOnceTheContextIsDone(t *testing.T) {
	b := newThreeBlock()
	b.provision()

	status, raw, resp := serveCancelling(t, b, "network", contract.Present, "validate")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", status, raw)
	}
	wantCalls(t, b.log, "network observe")
	wantUntouched(t, b.firewall, b.instance)
	if !resp.Pending {
		t.Error("pending = false, want true: firewall and instance were never observed")
	}
	if resp.Absent != nil && *resp.Absent {
		t.Error("absent = true, but network was observed present")
	}
	wantDetailNaming(t, resp, "firewall")
	wantOnlyEntries(t, resp, "network")
}

// The cancelling wrapper must still satisfy DeclaredResource, so the
// fixture cannot drift from the interface it stands in for.
var _ handler.DeclaredResource = (*cancellingResource)(nil)

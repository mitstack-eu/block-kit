package handler_test

// declared_resources_unchanged_test.go pins what handing a named resource's
// observe and delete its recorded inputs and stored identity must leave
// alone: a block written against DeclaredResource's three calls alone,
// served a request naming it with recorded inputs and a stored identity,
// still has its Observe and Delete handed the request's Identity and
// nothing else. The no-name answer bytes are pinned in
// declared_no_name_answer_test.go.

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/kittest"
)

// identityOnlyResource is a declared resource written against
// DeclaredResource alone: Ensure, Observe and Delete, and no other method.
// It keeps the Identity each Observe and Delete is handed.
type identityOnlyResource struct {
	fake     *kittest.FakeResource
	observed []contract.Identity
	deleted  []contract.Identity
}

func (r *identityOnlyResource) Ensure(ctx context.Context, d contract.Desired) error {
	return r.fake.Ensure(ctx, d)
}

func (r *identityOnlyResource) Observe(ctx context.Context, id contract.Identity) (contract.Observation, error) {
	r.observed = append(r.observed, id)
	return r.fake.Observe(ctx, id)
}

func (r *identityOnlyResource) Delete(ctx context.Context, id contract.Identity) error {
	r.deleted = append(r.deleted, id)
	return r.fake.Delete(ctx, id)
}

var _ handler.DeclaredResource = (*identityOnlyResource)(nil)

// wantRequestIdentity checks id is the Identity of the request serveThree
// sends: its subscription, block and org.
func wantRequestIdentity(t *testing.T, call string, id contract.Identity) {
	t.Helper()
	if id.SubscriptionID != "sub-1" || id.BlockID != "block-1" || id.OrgID != "org-1" {
		t.Errorf("%s was handed Identity %+v, want the request's subscription sub-1, block block-1 and org org-1", call, id)
	}
}

func TestDeclaredResources_AResourceWithOnlyEnsureObserveAndDeleteIsObservedAndDeletedWithTheIdentityAlone(t *testing.T) {
	for _, operation := range []string{"subscribe", "upgrade", "unsubscribe", "validate"} {
		t.Run(operation, func(t *testing.T) {
			b := newThreeBlock()
			firewall := &identityOnlyResource{fake: &kittest.FakeResource{Outputs: map[string]string{"groupId": "sg-0abc"}}}
			if operation != "subscribe" {
				firewall.fake.Count = 1
			}
			resources := b.resources()
			resources["firewall"] = firewall

			status, raw, _ := serveThree(t, resources, operation, map[string]any{
				"resource":         "firewall",
				"resourceInputs":   requestInputs,
				"resourceIdentity": map[string]any{"groupId": "sg-0abc"},
				"params":           map[string]any{"appName": "myapp"},
			})
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %s", status, raw)
			}
			if len(firewall.observed) == 0 {
				t.Fatalf("%s never observed firewall", operation)
			}
			for i, id := range firewall.observed {
				wantRequestIdentity(t, fmt.Sprintf("Observe %d", i+1), id)
			}
			wantDeletes := 0
			if operation == "unsubscribe" {
				wantDeletes = 1
			}
			if len(firewall.deleted) != wantDeletes {
				t.Fatalf("firewall's Delete ran %d times on %s, want %d", len(firewall.deleted), operation, wantDeletes)
			}
			for _, id := range firewall.deleted {
				wantRequestIdentity(t, "Delete", id)
			}
			wantUntouched(t, b.network, b.instance)
		})
	}
}

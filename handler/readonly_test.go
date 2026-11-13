package handler_test

// readonly_test.go pins that a request which must write nothing reaches the block's kit clients as one that cannot send a write.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
)

type deletingObserver struct{ client *ghapi.Client }

func (o deletingObserver) ObserveResource(ctx context.Context, _ contract.ResourceRef) (contract.Observation, error) {
	return contract.Observation{State: contract.Present}, o.client.DeleteRepo(ctx, "acme", "widget")
}

func TestValidate_OnlyTheValidateThatSettlesAVerbReachesTheBlocksKitClientsAbleToWrite(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]any
		writes bool
	}{
		{"a validate that settles no verb", nil, false},
		{"a validate naming one resource", map[string]any{"resource": "a", "resourceType": "aws.iam-role"}, false},
		{"the validate that settles a verb", map[string]any{"settling": true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var writes atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					writes.Add(1)
				}
			}))
			t.Cleanup(srv.Close)
			obs := map[string]handler.Observer{}
			for name := range declaredTypes {
				obs[name] = deletingObserver{ghapi.NewClient(srv.URL, "ghs_readonly", srv.Client())}
			}

			_, resp := serveBlock(t, blockCall{r: &blockResource{}, obs: obs, op: "validate", fields: tc.fields})

			if got := writes.Load() > 0; got != tc.writes {
				t.Errorf("the block's client wrote %t, want %t", got, tc.writes)
			}
			if refused := strings.Contains(resp.Error, contract.ErrReadOnly.Error()); refused == tc.writes {
				t.Errorf("the answer's error is %q, want the read-only refusal exactly when the validate may not write (%t)", resp.Error, !tc.writes)
			}
		})
	}
}

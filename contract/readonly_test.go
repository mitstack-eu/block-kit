package contract_test

// readonly_test.go pins what a context marked read-only refuses to send.

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
)

func TestRefuseWrite_RefusesEveryMethodButAReadOnlyAContextMarkedReadOnlyCarries(t *testing.T) {
	marked := contract.WithReadOnly(context.Background())
	for _, tc := range []struct {
		method string
		refuse bool
	}{
		{http.MethodGet, false}, {http.MethodHead, false},
		{http.MethodPost, true}, {http.MethodPut, true}, {http.MethodPatch, true}, {http.MethodDelete, true},
	} {
		t.Run(tc.method, func(t *testing.T) {
			if err := contract.RefuseWrite(marked, tc.method); errors.Is(err, contract.ErrReadOnly) != tc.refuse {
				t.Errorf("RefuseWrite(read-only, %s) = %v, want a refusal wrapping ErrReadOnly: %v", tc.method, err, tc.refuse)
			}
			if err := contract.RefuseWrite(context.Background(), tc.method); err != nil {
				t.Errorf("RefuseWrite(a context that may write, %s) = %v, want nil", tc.method, err)
			}
		})
	}
}

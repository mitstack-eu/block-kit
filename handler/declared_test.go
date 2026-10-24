package handler_test

// An observe outcome carries the identity its observer read, else the one the observer was handed, and never one holding a request secret.

import (
	"maps"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/verify/kittest"
)

func TestDeclared_AnObserveOutcomeCarriesTheIdentityItsObserverReported(t *testing.T) {
	const token = "ghp_identity-leak-5b1e09"
	read := map[string]string{"roleName": "a-role-read"}
	cases := []struct {
		label          string
		fields         map[string]any
		reported, want map[string]string
	}{
		{"a validate handed no identity answers the one its observer read", map[string]any{}, read, read},
		{"an observer reporting none keeps the identity the request records", recordedAll(), nil, map[string]string{"roleName": "a-role"}},
		{"an identity holding a request secret is refused, as an output holding one is", recordedAll(), map[string]string{"roleName": token}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			cloud := newFakeCloud()
			cloud.createAll()
			obs := asObservers(blockObservers(cloud))
			obs["a"] = fixed(contract.Observation{State: contract.Present, Identity: tc.reported})
			tc.fields["secrets"] = map[string]any{"github": map[string]any{"token": token}}

			status, resp := serveBlock(t, blockCall{r: &blockResource{cloud: cloud}, obs: obs, op: "validate", fields: tc.fields})

			kittest.AssertNoSecret(t, "the answer", resp.Error+resp.StatusDetail, token)
			if tc.want == nil {
				wantRefusal(t, status, resp, "secret-in-output", "identity")
				return
			}
			wantOK(t, status, resp)
			if got := resp.Resources["a"].Observe; got == nil || !maps.Equal(got.Identity, tc.want) {
				t.Errorf("a observe = %+v, want identity %v", got, tc.want)
			}
		})
	}
}

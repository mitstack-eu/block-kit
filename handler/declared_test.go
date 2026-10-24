package handler_test

import (
	"maps"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
)

func TestDeclared_AValidateHandedNoIdentityAnswersTheIdentityItsObserverRead(t *testing.T) {
	read := map[string]string{"roleName": "a-role-read"}
	obs := asObservers(blockObservers(standingCloud()))
	obs["a"] = fixed(contract.Observation{State: contract.Present, Identity: read})

	_, resp := serveBlock(t, blockCall{r: &blockResource{}, obs: obs, op: "validate"})

	if got := resp.Resources["a"].Observe; got == nil || !maps.Equal(got.Identity, read) {
		t.Errorf("a observe = %+v, want the identity its observer read, %v", got, read)
	}
}

// Package observe, part of the core every block needs, holds the shared read-only observers, which ask the provider what stands now and never answer from what an ensure remembered.
package observe

import (
	"fmt"

	"github.com/mitstack-eu/block-kit/contract"
)

// present reports the identity r names, since the observer found the resource by it.
func present(r contract.ResourceRef, outputs map[string]string) contract.Observation {
	return contract.Observation{State: contract.Present, Identity: r.Identity, Outputs: outputs}
}

func absent() contract.Observation {
	return contract.Observation{State: contract.Absent}
}

func unknown(format string, args ...any) contract.Observation {
	return contract.Observation{State: contract.Unknown, StatusDetail: fmt.Sprintf(format, args...)}
}

// A missing key answers unknown before any API call is made.
func identityKeys(r contract.ResourceRef, keys ...string) ([]string, contract.Observation, bool) {
	values := make([]string, len(keys))
	for i, key := range keys {
		values[i] = r.Identity[key]
		if values[i] == "" {
			return nil, unknown("resource %q: its identity holds no %s, so there is nothing to observe", r.Name, key), false
		}
	}
	return values, contract.Observation{}, true
}

func existence(r contract.ResourceRef, exists bool, err error, outputs map[string]string) (contract.Observation, error) {
	if err != nil {
		return contract.Observation{}, err
	}
	if !exists {
		return absent(), nil
	}
	return present(r, outputs), nil
}

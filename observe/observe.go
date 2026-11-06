// Package observe, part of the core every block needs, holds the shared read-only observers, which ask the provider what stands now and never answer from what an ensure remembered.
package observe

import (
	"context"
	"fmt"
	"slices"

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

// heldByOwner answers an unsubscribing observe whose identity lacks its key, as a failed create leaves it, from what lister finds under the subscription's id.
// It answers present, naming the lowest id of typ, or absent when the list holds none; a failed list returns its error, and noKey stands when there is nothing to list with.
// Several objects of typ cannot be told apart without a name, so the lowest id stands for them all: an unsubscribe deletes every object the subscription holds.
// A type the lister cannot type passes an empty typ and keeps noKey, since a list that never names it proves nothing about its absence.
func heldByOwner(ctx context.Context, r contract.ResourceRef, lister TaggedLister, typ, key string, noKey contract.Observation) (contract.Observation, error) {
	if !r.Unsubscribing || lister == nil || typ == "" || r.Desired.SubscriptionID == "" {
		return noKey, nil
	}
	tagged, err := lister.ListTagged(ctx, r.Desired, r.Desired.SubscriptionID)
	if err != nil {
		return contract.Observation{}, err
	}
	var ids []string
	for _, t := range tagged {
		if t.Type == typ {
			ids = append(ids, t.ID)
		}
	}
	if len(ids) == 0 {
		return absent(), nil
	}
	return contract.Observation{State: contract.Present, Identity: map[string]string{key: slices.Min(ids)}}, nil
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

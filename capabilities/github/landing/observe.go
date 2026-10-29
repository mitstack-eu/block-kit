package landing

import (
	"context"
	"fmt"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/contract"
)

// Observer is the handler.Observer of a resource whose identity is a landing's Record: present while main carries one file it created at its recorded blob, absent once main carries none or the repository is proven deleted, and unknown when a read fails.
type Observer struct {
	// Client returns the client the resource's repository is read with.
	Client func(ctx context.Context, d contract.Desired) (*ghapi.Client, error)
}

func (o Observer) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	rec, err := RecordFromIdentity(r.Identity)
	if err != nil {
		return unknown("resource %q has no landing record to read: %v", r.Name, err), nil
	}
	c, err := o.Client(ctx, r.Desired)
	if err != nil {
		return unknown("no client reads %s: %v", rec.Repository, err), nil
	}
	st, err := readMain(ctx, c, rec)
	switch {
	case err != nil:
		return unknown("%s cannot be read: %v", rec.Repository, err), nil
	case st.deleted:
		return contract.Observation{State: contract.Absent, StatusDetail: rec.Repository + " is proven deleted, so it holds nothing this resource landed"}, nil
	case len(st.unchanged) > 0:
		return contract.Observation{State: contract.Present, StatusDetail: fmt.Sprintf("main of %s carries %d landed files unchanged", rec.Repository, len(st.unchanged))}, nil
	}
	return contract.Observation{State: contract.Absent, StatusDetail: fmt.Sprintf("main of %s carries none of the files this resource landed unchanged", rec.Repository)}, nil
}

func unknown(format string, args ...any) contract.Observation {
	return contract.Observation{State: contract.Unknown, StatusDetail: fmt.Sprintf(format, args...)}
}

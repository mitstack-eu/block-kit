package landing

import (
	"context"
	"fmt"
	"strings"

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
		return contract.Observation{State: contract.Present, StatusDetail: fmt.Sprintf("main of %s carries %s unchanged", rec.Repository, strings.Join(st.unchanged, ", "))}, nil
	}
	return contract.Observation{State: contract.Absent, StatusDetail: fmt.Sprintf("main of %s carries none of the files this resource landed unchanged", rec.Repository)}, nil
}

// OfferObserver is the handler.Observer of a contribution whose files another block commits: outside an unsubscribe it is present while the repository can be read, since the files may not have landed yet, and during one it answers as Observer does.
type OfferObserver struct {
	Client func(ctx context.Context, d contract.Desired) (*ghapi.Client, error)
}

func (o OfferObserver) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	if r.Unsubscribing {
		return Observer(o).ObserveResource(ctx, r)
	}
	rec, err := RecordFromIdentity(r.Identity)
	if err != nil {
		return unknown("resource %q has no landing record to read: %v", r.Name, err), nil
	}
	c, err := o.Client(ctx, r.Desired)
	if err != nil {
		return unknown("no client reads %s: %v", rec.Repository, err), nil
	}
	deleted, err := repositoryDeleted(ctx, api{c: c, repo: rec.Repository})
	switch {
	case err != nil:
		return unknown("%s cannot be read: %v", rec.Repository, err), nil
	case deleted:
		return contract.Observation{State: contract.Absent, StatusDetail: rec.Repository + " is proven deleted, so nothing is offered to it"}, nil
	}
	return contract.Observation{State: contract.Present, StatusDetail: "the contribution is offered to " + rec.Repository}, nil
}

func unknown(format string, args ...any) contract.Observation {
	return contract.Observation{State: contract.Unknown, StatusDetail: fmt.Sprintf(format, args...)}
}

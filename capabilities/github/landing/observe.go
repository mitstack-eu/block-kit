package landing

import (
	"context"
	"fmt"
	"strings"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/contract"
)

// Observer is the handler.Observer of a resource whose identity is a landing's Record: present while main carries one file it created at its recorded blob, absent once main carries none, the repository is proven deleted or an unsubscribe is marked contract.AppDeleting, and unknown when a read fails.
type Observer struct {
	// Client returns the client the resource's repository is read with.
	Client func(ctx context.Context, d contract.Desired) (*ghapi.Client, error)
}

func (o Observer) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	rec, err := RecordFromIdentity(r.Identity)
	if err != nil {
		return unknown("resource %q has no landing record to read: %v", r.Name, err), nil
	}
	if r.Unsubscribing && contract.AppDeleting(ctx) {
		return contract.Observation{State: contract.Absent, StatusDetail: fmt.Sprintf("the app's delete leaves the files this resource landed on main of %s as the app has them, so the landing ends with the subscription", rec.Repository)}, nil
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
		return presentWithRemoval(ctx, api{c: c, repo: rec.Repository}, r, st.unchanged), nil
	}
	return contract.Observation{State: contract.Absent, StatusDetail: fmt.Sprintf("main of %s carries none of the files this resource landed unchanged", rec.Repository)}, nil
}

// presentWithRemoval answers a resource main still carries, and during an unsubscribe names what its removal waits on while that removal is under way.
func presentWithRemoval(ctx context.Context, a api, r contract.ResourceRef, unchanged []string) contract.Observation {
	obs := contract.Observation{State: contract.Present, StatusDetail: fmt.Sprintf("main of %s carries %s unchanged", a.repo, strings.Join(unchanged, ", "))}
	if !r.Unsubscribing || !subscriptionID.MatchString(r.Desired.SubscriptionID) {
		return obs
	}
	waitsOn, err := a.removalUnderWay(ctx, r.Desired.SubscriptionID)
	switch {
	case err != nil:
		return unknown("%s, and its removal pull request cannot be read: %v", obs.StatusDetail, err)
	case waitsOn != "":
		obs.Removing = &contract.Removing{WaitsOn: waitsOn}
	}
	return obs
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

package kittest

import (
	"maps"
	"slices"
	"strings"

	"github.com/mitstack-eu/block-kit/capabilities/github/landing"
	"github.com/mitstack-eu/block-kit/contract"
)

// emptyIdentityUnsubscribe sends each recorded resource an unsubscribing observe that names no identity, as the engine does for a row a failed create left without one, and holds the answer to what the provider holds of its type, which is nothing for a type marked providerHoldsNothing; a file-landing type is left out, since its record is the only way to find what it landed.
func (c *blockCheck) emptyIdentityUnsubscribe() {
	tagged, err := c.listTagged()
	if err != nil {
		c.problem("listing what carries the subscription's id failed: %v", err)
		return
	}
	for _, name := range unitNames(c.spec, c.desired.RecordedResources) {
		typ := c.desired.RecordedResources[name].Type
		if landing.LandsFiles(typ) {
			continue
		}
		var held []string
		holdsNothing := c.providerHoldsNothing(typ)
		for _, t := range tagged {
			if t.Type == typ && !holdsNothing {
				held = append(held, t.ID)
			}
		}
		d := c.desired
		d.RecordedResources = maps.Clone(c.desired.RecordedResources)
		d.RecordedResources[name] = contract.RecordedResource{Type: typ, State: c.desired.RecordedResources[name].State}
		d.Resource, d.ResourceType, d.ResourceIdentity, d.ResourceUnsubscribing = name, typ, nil, true
		_, resp, ok := c.sendOK("validate", d)
		if !ok {
			continue
		}
		c.ran[guardEmptyID] = true
		c.emptyIdentityAnswer(name, typ, held, resp)
	}
}

func (c *blockCheck) emptyIdentityAnswer(name, typ string, held []string, resp contract.Response) {
	o := resp.Resources[name].Observe
	const probe = "on an unsubscribing observe with an empty identity"
	switch {
	case o == nil:
		c.resourceProblem(name, "an unsubscribing observe with an empty identity answered no observe")
	case c.providerHoldsNothing(typ) && o.State != contract.Absent:
		c.resourceProblem(name, "observed %s %s, but the type snapshot marks %s providerHoldsNothing, so the provider holds none of it, want absent", o.State, probe, typ)
	case len(held) == 0 && o.State != contract.Absent:
		c.resourceProblem(name, "observed %s %s while the provider holds no %s, want absent", o.State, probe, typ)
	case len(held) > 0 && o.State == contract.Absent:
		c.resourceProblem(name, "observed absent %s, but the provider still holds %s %s, want present or pending", probe, typ, strings.Join(held, ", "))
	case len(held) > 0 && o.State == contract.Unknown && !explainsUnknown(*o, resp, held):
		c.resourceProblem(name, "observed unknown %s while the provider holds %s %s, want present or pending, or an unknown naming what it holds", probe, typ, strings.Join(held, ", "))
	}
}

// explainsUnknown reports whether an unknown answer gives a reason other than the missing identity: a wait, a redrive, or the object the provider holds.
func explainsUnknown(o contract.CallOutcome, resp contract.Response, held []string) bool {
	return o.WaitingOnPerson != nil || resp.Redrive ||
		slices.ContainsFunc(held, func(id string) bool { return strings.Contains(o.StatusDetail, id) })
}

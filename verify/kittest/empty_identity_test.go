package kittest_test

// An unsubscribing observe naming no identity is answered from what the provider holds, never unknown for want of the identity.

import (
	"context"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/verify/kittest"
)

// emptyIdentityObserver answers an unsubscribing observe with no identity through answer, and every other observe as providerObserver does.
type emptyIdentityObserver struct {
	p      *fakeProvider
	answer func(held []string) contract.Observation
}

func (o emptyIdentityObserver) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	if r.Identity["id"] != "" || !r.Unsubscribing {
		return providerObserver{p: o.p}.ObserveResource(ctx, r)
	}
	var held []string
	tagged, _ := o.p.ListTagged(ctx, r.Desired.SubscriptionID)
	for _, item := range tagged {
		if item.Type == r.Type {
			held = append(held, item.ID)
		}
	}
	return o.answer(held), nil
}

func emptyIdentityBlock(t *testing.T, answer func(held []string) contract.Observation) kittest.Block {
	t.Helper()
	b, in := hostBlock(t)
	observer := emptyIdentityObserver{p: b.p, answer: answer}
	in.Config.Observers = map[string]handler.Observer{"aws.ec2-instance": observer, "aws.ebs-volume": observer}
	return in
}

func unknownForWantOfAnIdentity([]string) contract.Observation {
	return contract.Observation{State: contract.Unknown, StatusDetail: "its identity holds nothing, so there is nothing to observe"}
}

func TestCheckBlock_RefusesUnknownOnAnEmptyIdentityNamingEachTypeAndBothStates(t *testing.T) {
	problems := kittest.CheckBlock(emptyIdentityBlock(t, unknownForWantOfAnIdentity))

	for _, typ := range []string{"aws.ec2-instance", "aws.ebs-volume"} {
		wantProblemNaming(t, problems, `resource "`+typ+`"`, "unknown", "empty identity", "want present or pending")
		wantProblemNaming(t, problems, `resource "`+typ+`"`, "unknown", "empty identity", "want absent")
	}
}

func TestCheckBlock_AcceptsAbsentOnAProviderHoldingNothingAndPresentOnOneHoldingIt(t *testing.T) {
	_, in := hostBlock(t)

	tb := runAndCollect(func(tb testing.TB) { kittest.AssertBlock(tb, in) })

	if tb.failed {
		t.Errorf("AssertBlock failed a block answering absent when nothing is held and present when it is: %q", tb.logs)
	}
	if !strings.Contains(strings.Join(tb.logs, "\n"), "empty-identity-unsubscribe") {
		t.Errorf("AssertBlock logs %q, want it to name the empty-identity-unsubscribe guard", tb.logs)
	}
}

func TestCheckBlock_RefusesAbsentWhileTheProviderStillHoldsTheResource(t *testing.T) {
	problems := kittest.CheckBlock(emptyIdentityBlock(t, func([]string) contract.Observation {
		return contract.Observation{State: contract.Absent}
	}))

	wantProblemNaming(t, problems, `resource "aws.ebs-volume"`, "absent", "empty identity", "still holds aws.ebs-volume")
}

func TestCheckBlock_RefusesPresentWhileTheProviderHoldsNothing(t *testing.T) {
	problems := kittest.CheckBlock(emptyIdentityBlock(t, func([]string) contract.Observation {
		return contract.Observation{State: contract.Present}
	}))

	wantProblemNaming(t, problems, `resource "aws.ebs-volume"`, "present", "empty identity", "want absent")
}

func TestCheckBlock_AcceptsUnknownThatNamesTheObjectTheProviderHolds(t *testing.T) {
	in := emptyIdentityBlock(t, func(held []string) contract.Observation {
		if len(held) == 0 {
			return contract.Observation{State: contract.Absent}
		}
		return contract.Observation{State: contract.Unknown, StatusDetail: "unrecorded: " + strings.Join(held, ", ") + " is held and no identity names it"}
	})

	wantNoProblems(t, kittest.CheckBlock(in))
}

func TestCheckBlock_AcceptsUnknownThatWaitsOnAPersonOrAsksForARedrive(t *testing.T) {
	for name, whileHeld := range map[string]contract.Observation{
		"waiting on a person": {State: contract.Unknown, WaitingOnPerson: &contract.WaitingOnPerson{Action: "approve the pull request"}},
		"a redrive":           {State: contract.Unknown, Redrive: true},
	} {
		t.Run(name, func(t *testing.T) {
			wantNoProblems(t, kittest.CheckBlock(emptyIdentityBlock(t, func(held []string) contract.Observation {
				if len(held) == 0 {
					return contract.Observation{State: contract.Absent}
				}
				return whileHeld
			})))
		})
	}
}

func TestCheckBlock_ProbesTheTypeEachRunMeetsARequirementWith(t *testing.T) {
	b, in := containerBlock(t)
	in.Config.Observers = map[string]handler.Observer{"app-container": emptyIdentityObserver{p: b.p, answer: unknownForWantOfAnIdentity}}

	problems := kittest.CheckBlock(in)

	wantProblemNaming(t, problems, `resource "app-container"`, "empty identity", "requires.host met by hetzner.server")
}

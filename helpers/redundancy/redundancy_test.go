package redundancy_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/helpers/redundancy"
)

var stepStart = time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)

func serving(name string, at time.Time) redundancy.Member {
	return redundancy.Member{Name: name, Reading: &redundancy.Reading{At: at, Serving: true}}
}

func down(name string, at time.Time) redundancy.Member {
	return redundancy.Member{Name: name, Reading: &redundancy.Reading{At: at, Serving: false}}
}

func unread(name string) redundancy.Member {
	return redundancy.Member{Name: name}
}

func fresh(minutes int) time.Time {
	return stepStart.Add(time.Duration(minutes) * time.Minute)
}

func requireRefused(t *testing.T, err error, wantDetail string) {
	t.Helper()
	var refused *handler.Refused
	if !errors.As(err, &refused) {
		t.Fatalf("AllowRemoval() = %v; want a *handler.Refused", err)
	}
	if refused.Class != redundancy.ClassBelowMinimum {
		t.Errorf("refusal class = %q; want %q", refused.Class, redundancy.ClassBelowMinimum)
	}
	if refused.Detail != wantDetail {
		t.Errorf("refusal detail =\n%s\nwant\n%s", refused.Detail, wantDetail)
	}
}

func TestAllowRemoval_RefusesNamingEachRemainingMember(t *testing.T) {
	cases := []struct {
		name       string
		set        redundancy.Set
		remove     string
		wantDetail string
	}{
		{
			name: "three gateways, one with no reading, refuse replacing another and name the unread member",
			set: redundancy.Set{
				Name:    "gateways",
				Members: []redundancy.Member{serving("gw-fsn1", fresh(1)), unread("gw-nbg1"), serving("gw-hel1", fresh(2))},
				Minimum: redundancy.AllButOne(),
			},
			remove: "gw-fsn1",
			wantDetail: `removing "gw-fsn1" from the gateways would leave 1 serving, below their minimum of 2 (all but the one removed), ` +
				`judged by readings since 2026-11-08T09:00:00Z: "gw-nbg1" has no reading; "gw-hel1" read serving at 2026-11-08T09:02:00Z`,
		},
		{
			name: "three etcd members, one read before the step began, refuse removing a member and name the stale reading and its time",
			set: redundancy.Set{
				Name:    "etcd members",
				Members: []redundancy.Member{serving("cp-1", fresh(1)), serving("cp-2", fresh(-1)), serving("cp-3", fresh(3))},
				Minimum: redundancy.Majority(),
			},
			remove: "cp-3",
			wantDetail: `removing "cp-3" from the etcd members would leave 1 serving, below their minimum of 2 (a majority of 3), ` +
				`judged by readings since 2026-11-08T09:00:00Z: "cp-1" read serving at 2026-11-08T09:01:00Z; "cp-2" read at 2026-11-08T08:59:00Z, before the step began`,
		},
		{
			name: "three members, one already down, refuse removing a second and name the majority minimum",
			set: redundancy.Set{
				Name:    "OpenBao members",
				Members: []redundancy.Member{serving("bao-0", fresh(1)), down("bao-1", fresh(1)), serving("bao-2", fresh(1))},
				Minimum: redundancy.Majority(),
			},
			remove: "bao-2",
			wantDetail: `removing "bao-2" from the OpenBao members would leave 1 serving, below their minimum of 2 (a majority of 3), ` +
				`judged by readings since 2026-11-08T09:00:00Z: "bao-0" read serving at 2026-11-08T09:01:00Z; "bao-1" read not serving at 2026-11-08T09:01:00Z`,
		},
		{
			name: "a single gateway with the all-but-one minimum, refuse replacing it, since at least one must serve",
			set: redundancy.Set{
				Name:    "gateways",
				Members: []redundancy.Member{serving("gw-fsn1", fresh(1))},
				Minimum: redundancy.AllButOne(),
			},
			remove:     "gw-fsn1",
			wantDetail: `removing "gw-fsn1" from the gateways would leave 0 serving, below their minimum of 1 (all but the one removed), judged by readings since 2026-11-08T09:00:00Z: no other member`,
		},
		{
			name: "a single etcd member with the majority minimum, refuse removing it",
			set: redundancy.Set{
				Name:    "etcd members",
				Members: []redundancy.Member{serving("cp-1", fresh(1))},
				Minimum: redundancy.Majority(),
			},
			remove:     "cp-1",
			wantDetail: `removing "cp-1" from the etcd members would leave 0 serving, below their minimum of 1 (a majority of 1), judged by readings since 2026-11-08T09:00:00Z: no other member`,
		},
		{
			name: "four database instances with one unread and one down, refuse replacing a third, naming every remaining member",
			set: redundancy.Set{
				Name:    "database instances",
				Members: []redundancy.Member{serving("db-a", fresh(1)), unread("db-b"), down("db-c", fresh(2)), serving("db-d", fresh(3))},
				Minimum: redundancy.AllButOne(),
			},
			remove: "db-a",
			wantDetail: `removing "db-a" from the database instances would leave 1 serving, below their minimum of 3 (all but the one removed), ` +
				`judged by readings since 2026-11-08T09:00:00Z: "db-b" has no reading; "db-c" read not serving at 2026-11-08T09:02:00Z; "db-d" read serving at 2026-11-08T09:03:00Z`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.set.AllowRemoval(tc.remove, stepStart)

			requireRefused(t, err, tc.wantDetail)
		})
	}
}

func TestAllowRemoval_AllowsWhenTheRestServeFreshly(t *testing.T) {
	cases := []struct {
		name   string
		set    redundancy.Set
		remove string
	}{
		{
			name: "three members all freshly serving with a majority minimum, allow removing one",
			set: redundancy.Set{
				Name:    "etcd members",
				Members: []redundancy.Member{serving("cp-1", fresh(1)), serving("cp-2", fresh(2)), serving("cp-3", fresh(3))},
				Minimum: redundancy.Majority(),
			},
			remove: "cp-2",
		},
		{
			name: "three gateways all freshly serving with the all-but-one minimum, allow replacing one",
			set: redundancy.Set{
				Name:    "gateways",
				Members: []redundancy.Member{serving("gw-fsn1", fresh(1)), serving("gw-nbg1", fresh(1)), serving("gw-hel1", fresh(1))},
				Minimum: redundancy.AllButOne(),
			},
			remove: "gw-nbg1",
		},
		{
			name: "the member being removed is the one down and the rest serve, allow removing it",
			set: redundancy.Set{
				Name:    "workers",
				Members: []redundancy.Member{serving("w-1", fresh(1)), unread("w-2"), serving("w-3", fresh(1))},
				Minimum: redundancy.AllButOne(),
			},
			remove: "w-2",
		},
		{
			name: "a reading taken at the very start of the step counts as fresh",
			set: redundancy.Set{
				Name:    "gateways",
				Members: []redundancy.Member{serving("gw-fsn1", stepStart), serving("gw-nbg1", stepStart)},
				Minimum: redundancy.AllButOne(),
			},
			remove: "gw-fsn1",
		},
		{
			name: "a single gateway whose caller declares no redundancy, allow replacing it",
			set: redundancy.Set{
				Name:    "gateways",
				Members: []redundancy.Member{serving("gw-fsn1", fresh(1))},
				Minimum: redundancy.NoRedundancy(),
			},
			remove: "gw-fsn1",
		},
		{
			name: "five etcd members with two down, allow removing a third whose loss still leaves a majority",
			set: redundancy.Set{
				Name:    "etcd members",
				Members: []redundancy.Member{serving("cp-1", fresh(1)), down("cp-2", fresh(1)), serving("cp-3", fresh(1)), serving("cp-4", fresh(1)), unread("cp-5")},
				Minimum: redundancy.Majority(),
			},
			remove: "cp-2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.set.AllowRemoval(tc.remove, stepStart)

			if err != nil {
				t.Errorf("AllowRemoval(%q) = %v; want nil", tc.remove, err)
			}
		})
	}
}

func TestAllowRemoval_ThreeMemberMajoritySetRefusesWhileASecondMemberIsDown(t *testing.T) {
	set := redundancy.Set{
		Name:    "etcd members",
		Members: []redundancy.Member{serving("cp-1", fresh(1)), serving("cp-2", fresh(1)), down("cp-3", fresh(1))},
		Minimum: redundancy.Majority(),
	}

	err := set.AllowRemoval("cp-1", stepStart)

	requireRefused(t, err, `removing "cp-1" from the etcd members would leave 1 serving, below their minimum of 2 (a majority of 3), `+
		`judged by readings since 2026-11-08T09:00:00Z: "cp-2" read serving at 2026-11-08T09:01:00Z; "cp-3" read not serving at 2026-11-08T09:01:00Z`)
}

func TestAllowRemoval_RejectsACallThatCannotBeJudged(t *testing.T) {
	members := []redundancy.Member{serving("gw-fsn1", fresh(1)), serving("gw-nbg1", fresh(1)), serving("gw-hel1", fresh(1))}
	cases := []struct {
		name   string
		set    redundancy.Set
		start  time.Time
		remove string
	}{
		{
			name:   "a set that declares no minimum",
			set:    redundancy.Set{Name: "gateways", Members: members},
			start:  stepStart,
			remove: "gw-fsn1",
		},
		{
			name:   "a step with no start time, against which no reading can be judged fresh",
			set:    redundancy.Set{Name: "gateways", Members: members, Minimum: redundancy.AllButOne()},
			start:  time.Time{},
			remove: "gw-fsn1",
		},
		{
			name:   "a member the set does not hold",
			set:    redundancy.Set{Name: "gateways", Members: members, Minimum: redundancy.AllButOne()},
			start:  stepStart,
			remove: "gw-ash1",
		},
		{
			name:   "a set that names one member twice",
			set:    redundancy.Set{Name: "gateways", Members: append([]redundancy.Member{serving("gw-nbg1", fresh(1))}, members...), Minimum: redundancy.AllButOne()},
			start:  stepStart,
			remove: "gw-fsn1",
		},
		{
			name:   "a set of three that declares no redundancy, which only a set of one may",
			set:    redundancy.Set{Name: "gateways", Members: members, Minimum: redundancy.NoRedundancy()},
			start:  stepStart,
			remove: "gw-fsn1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.set.AllowRemoval(tc.remove, tc.start)

			if err == nil {
				t.Fatalf("AllowRemoval(%q, %v) = nil; want an error", tc.remove, tc.start)
			}
			var refused *handler.Refused
			if errors.As(err, &refused) {
				t.Errorf("AllowRemoval(%q, %v) = %v; want a block error, since a refusal would blame the cluster for the caller's mistake", tc.remove, tc.start, err)
			}
		})
	}
}

func ExampleSet_AllowRemoval() {
	stepStart := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	set := redundancy.Set{
		Name: "etcd members",
		Members: []redundancy.Member{
			{Name: "cp-1", Reading: &redundancy.Reading{At: stepStart.Add(time.Minute), Serving: true}},
			{Name: "cp-2", Reading: &redundancy.Reading{At: stepStart.Add(time.Minute), Serving: false}},
			{Name: "cp-3", Reading: &redundancy.Reading{At: stepStart.Add(time.Minute), Serving: true}},
		},
		Minimum: redundancy.Majority(),
	}

	fmt.Println(set.AllowRemoval("cp-2", stepStart))
	fmt.Println(set.AllowRemoval("cp-3", stepStart))
	// Output:
	// <nil>
	// refused:redundancy-below-minimum: removing "cp-3" from the etcd members would leave 1 serving, below their minimum of 2 (a majority of 3), judged by readings since 2026-11-08T09:00:00Z: "cp-1" read serving at 2026-11-08T09:01:00Z; "cp-2" read not serving at 2026-11-08T09:01:00Z
}

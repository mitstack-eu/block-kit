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

// read is a member read the given minutes after the step began, a negative count reading it before.
func read(name string, minutes int, up bool) redundancy.Member {
	at := stepStart.Add(time.Duration(minutes) * time.Minute)
	return redundancy.Member{Name: name, Reading: &redundancy.Reading{At: at, Serving: up}}
}

func serving(name string, minutes int) redundancy.Member { return read(name, minutes, true) }
func down(name string, minutes int) redundancy.Member    { return read(name, minutes, false) }
func unread(name string) redundancy.Member               { return redundancy.Member{Name: name} }

func set(name string, minimum redundancy.Minimum, members ...redundancy.Member) redundancy.Set {
	return redundancy.Set{Name: name, Members: members, Minimum: minimum}
}

func requireRefused(t *testing.T, err error, wantDetail string) {
	t.Helper()
	var refused *handler.Refused
	if !errors.As(err, &refused) {
		t.Fatalf("AllowRemoval() = %v; want a *handler.Refused", err)
	}
	if refused.Class != redundancy.ClassBelowMinimum || refused.Detail != wantDetail {
		t.Errorf("refusal = %q %q\nwant      %q %q", refused.Class, refused.Detail, redundancy.ClassBelowMinimum, wantDetail)
	}
}

func TestAllowRemoval(t *testing.T) {
	majority, allButOne := redundancy.Majority(), redundancy.AllButOne()
	cases := []struct {
		name   string
		set    redundancy.Set
		remove string
		// refused is the refusal's detail, "" when the removal is allowed.
		refused string
	}{
		{"three gateways, one with no reading, refuse replacing another and name the unread member",
			set("gateways", allButOne, serving("gw-fsn1", 1), unread("gw-nbg1"), serving("gw-hel1", 2)), "gw-fsn1",
			`removing "gw-fsn1" from the gateways would leave 1 serving, below their minimum of 2 (all but the one removed), judged by readings since 2026-11-08T09:00:00Z: "gw-nbg1" has no reading; "gw-hel1" read serving at 2026-11-08T09:02:00Z`},
		{"three etcd members, one read before the step began, refuse removing a member and name the stale reading and its time",
			set("etcd members", majority, serving("cp-1", 1), serving("cp-2", -1), serving("cp-3", 3)), "cp-3",
			`removing "cp-3" from the etcd members would leave 1 serving, below their minimum of 2 (a majority of 3), judged by readings since 2026-11-08T09:00:00Z: "cp-1" read serving at 2026-11-08T09:01:00Z; "cp-2" read at 2026-11-08T08:59:00Z, before the step began`},
		{"three members, one already down, refuse removing a second and name the majority minimum",
			set("OpenBao members", majority, serving("bao-0", 1), down("bao-1", 1), serving("bao-2", 1)), "bao-2",
			`removing "bao-2" from the OpenBao members would leave 1 serving, below their minimum of 2 (a majority of 3), judged by readings since 2026-11-08T09:00:00Z: "bao-0" read serving at 2026-11-08T09:01:00Z; "bao-1" read not serving at 2026-11-08T09:01:00Z`},
		{"four database instances, one unread and one down, refuse replacing a serving one and name every other member",
			set("database instances", allButOne, serving("db-a", 1), unread("db-b"), down("db-c", 2), serving("db-d", 3)), "db-a",
			`removing "db-a" from the database instances would leave 1 serving, below their minimum of 3 (all but the one removed), judged by readings since 2026-11-08T09:00:00Z: "db-b" has no reading; "db-c" read not serving at 2026-11-08T09:02:00Z; "db-d" read serving at 2026-11-08T09:03:00Z`},
		{"a single gateway with the all-but-one minimum, refuse replacing it, since at least one must serve",
			set("gateways", allButOne, serving("gw-fsn1", 1)), "gw-fsn1",
			`removing "gw-fsn1" from the gateways would leave 0 serving, below their minimum of 1 (all but the one removed), judged by readings since 2026-11-08T09:00:00Z: no other member`},
		{"a single etcd member with the majority minimum, refuse removing it",
			set("etcd members", majority, serving("cp-1", 1)), "cp-1",
			`removing "cp-1" from the etcd members would leave 0 serving, below their minimum of 1 (a majority of 1), judged by readings since 2026-11-08T09:00:00Z: no other member`},
		{"a single gateway whose caller declares no redundancy, allow replacing it",
			set("gateways", redundancy.NoRedundancy(), serving("gw-fsn1", 1)), "gw-fsn1", ""},
		{"three members all freshly serving with a majority minimum, allow removing one",
			set("etcd members", majority, serving("cp-1", 1), serving("cp-2", 2), serving("cp-3", 3)), "cp-2", ""},
		{"three gateways all freshly serving with the all-but-one minimum, allow replacing one",
			set("gateways", allButOne, serving("gw-fsn1", 1), serving("gw-nbg1", 1), serving("gw-hel1", 1)), "gw-nbg1", ""},
		{"the member being removed is the one unread while the rest serve, allow removing it",
			set("workers", allButOne, serving("w-1", 1), unread("w-2"), serving("w-3", 1)), "w-2", ""},
		{"a reading taken at the very start of the step counts as fresh, allow the removal",
			set("gateways", allButOne, serving("gw-fsn1", 0), serving("gw-nbg1", 0)), "gw-fsn1", ""},
		{"five etcd members with two not serving, allow removing one of those two while a majority serves",
			set("etcd members", majority, serving("cp-1", 1), down("cp-2", 1), serving("cp-3", 1), serving("cp-4", 1), unread("cp-5")), "cp-2", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.set.AllowRemoval(tc.remove, stepStart)

			if tc.refused != "" {
				requireRefused(t, err, tc.refused)
			} else if err != nil {
				t.Errorf("AllowRemoval(%q) = %v; want nil", tc.remove, err)
			}
		})
	}
}

func TestAllowRemoval_ThreeMemberMajoritySetRefusesWhileASecondMemberIsDown(t *testing.T) {
	etcd := set("etcd members", redundancy.Majority(), serving("cp-1", 1), serving("cp-2", 1), down("cp-3", 1))

	err := etcd.AllowRemoval("cp-1", stepStart)

	requireRefused(t, err, `removing "cp-1" from the etcd members would leave 1 serving, below their minimum of 2 (a majority of 3), judged by readings since 2026-11-08T09:00:00Z: "cp-2" read serving at 2026-11-08T09:01:00Z; "cp-3" read not serving at 2026-11-08T09:01:00Z`)
}

func TestAllowRemoval_ACallThatCannotBeJudgedIsTheBlocksErrorAndNoRefusal(t *testing.T) {
	members := []redundancy.Member{serving("gw-fsn1", 1), serving("gw-nbg1", 1), serving("gw-hel1", 1)}
	cases := []struct {
		name   string
		set    redundancy.Set
		start  time.Time
		remove string
	}{
		{"a set that declares no minimum", redundancy.Set{Name: "gateways", Members: members}, stepStart, "gw-fsn1"},
		{"a step with no start time, against which no reading can be judged fresh", set("gateways", redundancy.AllButOne(), members...), time.Time{}, "gw-fsn1"},
		{"a member the set does not hold", set("gateways", redundancy.AllButOne(), members...), stepStart, "gw-ash1"},
		{"a set that names one member twice", set("gateways", redundancy.AllButOne(), append(members, serving("gw-nbg1", 1))...), stepStart, "gw-fsn1"},
		{"a set of three that declares no redundancy, which only a set of one may", set("gateways", redundancy.NoRedundancy(), members...), stepStart, "gw-fsn1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.set.AllowRemoval(tc.remove, tc.start)

			var refused *handler.Refused
			if err == nil || errors.As(err, &refused) {
				t.Errorf("AllowRemoval(%q, %v) = %v; want a plain error, since a refusal would blame the cluster for the block's mistake", tc.remove, tc.start, err)
			}
		})
	}
}

func ExampleSet_AllowRemoval() {
	stepStart := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	readAt := stepStart.Add(time.Minute)
	set := redundancy.Set{
		Name: "etcd members",
		Members: []redundancy.Member{
			{Name: "cp-1", Reading: &redundancy.Reading{At: readAt, Serving: true}},
			{Name: "cp-2", Reading: &redundancy.Reading{At: readAt, Serving: false}},
			{Name: "cp-3", Reading: &redundancy.Reading{At: readAt, Serving: true}},
		},
		Minimum: redundancy.Majority(),
	}

	fmt.Println(set.AllowRemoval("cp-2", stepStart))
	fmt.Println(set.AllowRemoval("cp-3", stepStart))
	// Output:
	// <nil>
	// refused:redundancy-below-minimum: removing "cp-3" from the etcd members would leave 1 serving, below their minimum of 2 (a majority of 3), judged by readings since 2026-11-08T09:00:00Z: "cp-1" read serving at 2026-11-08T09:01:00Z; "cp-2" read not serving at 2026-11-08T09:01:00Z
}

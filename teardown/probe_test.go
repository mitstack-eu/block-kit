package teardown_test

// probe_test.go — MIT-1694.
//
// Probe answers "is this app's infrastructure gone yet". A stack switch is the
// caller that makes the answer load-bearing: the three AWS infra blocks
// hardcode account-unique names, so the target stack cannot be created until
// the source stack's resources are provably gone. Getting a false "yes" here
// does not fail cleanly — it collides, and the OpenTofu engine's self-repair
// imports would then adopt resources another engine owns.
//
// So every case below is about refusing to say "absent" on anything less than
// evidence.

import (
	"context"
	"errors"
	"testing"

	"github.com/mitstack-eu/operator-kit/adopt"
	"github.com/mitstack-eu/operator-kit/teardown"
)

func probeIdentity() adopt.Identity {
	return adopt.Identity{AppID: "app_1", Name: "checkout"}
}

func TestProbe_NothingFoundIsAbsent(t *testing.T) {
	got, err := teardown.Probe(context.Background(), probeIdentity(),
		[]teardown.Class{&stub{kind: "ebs-volume"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Absent {
		t.Fatalf("an empty discovery across every class is absent, got %+v", got)
	}
}

func TestProbe_AResourceStillThereIsNotAbsent(t *testing.T) {
	c := &stub{kind: "ebs-volume", found: []teardown.Resource{
		{ID: "vol-1", Tags: map[string]string{adopt.TagAppID: "app_1"}},
	}}
	got, err := teardown.Probe(context.Background(), probeIdentity(), []teardown.Class{c})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Absent {
		t.Fatal("a resource that is still there means not absent")
	}
	if len(got.Remaining) != 1 || got.Remaining[0].ID != "vol-1" {
		t.Fatalf("the probe must name what is left, got %+v", got.Remaining)
	}
}

// THE CASE THAT MATTERS MOST. A class whose discovery FAILED tells us nothing,
// and "we could not look" must never be reported as "there is nothing there" —
// that is a false all-clear, after which nothing in the system mentions the
// leak again. Sweep already refuses to call an unsearched class clean; Probe
// inherits the same rule for the same reason.
func TestProbe_ADiscoveryFailureIsNeverAbsent(t *testing.T) {
	c := &stub{kind: "ebs-volume", findErr: errors.New("throttled")}
	got, err := teardown.Probe(context.Background(), probeIdentity(), []teardown.Class{c})
	if err == nil {
		t.Fatal("a discovery failure must surface as an error, not be swallowed")
	}
	if got.Absent {
		t.Fatal("a class that could not be searched is not a class that is clean")
	}
	if len(got.Unsearched) != 1 || got.Unsearched[0] != "ebs-volume" {
		t.Fatalf("the probe must name the class it could not search, got %+v", got.Unsearched)
	}
}

// One clean class does not make the app absent when another could not be read.
func TestProbe_OneUnsearchableClassDefeatsTheCleanOnes(t *testing.T) {
	got, _ := teardown.Probe(context.Background(), probeIdentity(), []teardown.Class{
		&stub{kind: "ebs-volume"},
		&stub{kind: "ecr-repository", findErr: errors.New("access denied")},
	})
	if got.Absent {
		t.Fatal("absent must require EVERY class to have been searched and found empty")
	}
}

// A resource carrying another app's id is not ours, so it says nothing about
// whether OUR app is gone. Probe applies the same ownership rule as the sweep
// rather than counting every row the provider returned.
func TestProbe_AnotherAppsResourceDoesNotHoldUsBack(t *testing.T) {
	c := &stub{kind: "ebs-volume", found: []teardown.Resource{
		{ID: "vol-other", Tags: map[string]string{adopt.TagAppID: "app_2"}},
	}}
	got, err := teardown.Probe(context.Background(), probeIdentity(), []teardown.Class{c})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Absent {
		t.Fatalf("a foreign app's resource must not make our app look present: %+v", got)
	}
}

// Probe must never delete. It is called on a LIVE subscription too — every
// validate carries the marker — so a probe with a side effect would tear down
// a healthy app just by being asked how it is.
func TestProbe_NeverDeletesAnything(t *testing.T) {
	c := &stub{kind: "ebs-volume", found: []teardown.Resource{
		{ID: "vol-1", Tags: map[string]string{adopt.TagAppID: "app_1"}},
	}}
	if _, err := teardown.Probe(context.Background(), probeIdentity(), []teardown.Class{c}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.deleted) != 0 {
		t.Fatalf("Probe deleted %v — it is a read, and validate calls it on live apps", c.deleted)
	}
}

// An identity that matches nothing in particular matches everything; refusing
// beats probing with a wildcard.
func TestProbe_RefusesAnEmptyIdentity(t *testing.T) {
	got, err := teardown.Probe(context.Background(), adopt.Identity{}, []teardown.Class{&stub{kind: "ebs-volume"}})
	if err == nil {
		t.Fatal("an unvalidated identity must be refused")
	}
	if got.Absent {
		t.Fatal("a refused probe is not an absent one")
	}
}

// --- MIT-1949: skipReason's evidence must reach the probe, not be discarded -

// skipReason has four branches. Two of them are evidence the probe was
// silently throwing away: an untagged resource means "we could not tell",
// which is the Unsearched case, not a vote for absent.
func TestProbe_UntaggedResourceIsUnsearchedNotAbsent(t *testing.T) {
	c := &stub{kind: "ebs-volume", found: []teardown.Resource{
		{ID: "vol-untagged", Tags: map[string]string{}},
	}}
	got, _ := teardown.Probe(context.Background(), probeIdentity(), []teardown.Class{c})

	if got.Absent {
		t.Fatal("an untagged resource has no identity — we could not tell, so this must not be absent")
	}
	found := false
	for _, u := range got.Unsearched {
		if u == "ebs-volume" {
			found = true
		}
	}
	if !found {
		t.Fatalf("an untagged resource must mark its class UNSEARCHED, got %+v", got)
	}
}

// A resource carrying THIS app's AppId and InUse must appear in Remaining —
// it is affirmative evidence our resource still stands, not something to
// discard the way a foreign-owned resource is.
func TestProbe_OwnedResourceInUseAppearsInRemaining(t *testing.T) {
	c := &stub{kind: "ebs-volume", found: []teardown.Resource{
		{ID: "vol-inuse", Tags: map[string]string{adopt.TagAppID: "app_1"}, InUse: true},
	}}
	got, err := teardown.Probe(context.Background(), probeIdentity(), []teardown.Class{c})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Absent {
		t.Fatal("our own in-use resource is affirmative evidence we are not absent")
	}
	if len(got.Remaining) != 1 || got.Remaining[0].ID != "vol-inuse" {
		t.Fatalf("an in-use resource of ours must be named in Remaining, got %+v", got.Remaining)
	}
}

// A resource belonging to another app says nothing about ours, so it must
// stay dropped — neither Remaining nor Unsearched — exactly as it is today.
// This pins the two skipReason branches that are correctly discarded, so a
// future change to the other two cannot widen this one by accident.
func TestProbe_ForeignAppIDResourceStaysDropped(t *testing.T) {
	c := &stub{kind: "ebs-volume", found: []teardown.Resource{
		{ID: "vol-foreign", Tags: map[string]string{adopt.TagAppID: "app_2"}},
	}}
	got, err := teardown.Probe(context.Background(), probeIdentity(), []teardown.Class{c})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Absent {
		t.Fatalf("a foreign app's resource must not hold our teardown back: %+v", got)
	}
	if len(got.Remaining) != 0 || len(got.Unsearched) != 0 {
		t.Fatalf("a foreign app's resource must be dropped, not surfaced: %+v", got)
	}
}

// A resource carrying a foreign ManagedBy tag is not mitstack's to reason
// about, so it too must stay dropped.
func TestProbe_ForeignManagedByResourceStaysDropped(t *testing.T) {
	c := &stub{kind: "ebs-volume", found: []teardown.Resource{
		{ID: "vol-foreign-mb", Tags: map[string]string{
			adopt.TagAppID:     "app_1",
			adopt.TagManagedBy: "some-other-platform",
		}},
	}}
	got, err := teardown.Probe(context.Background(), probeIdentity(), []teardown.Class{c})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Absent {
		t.Fatalf("a resource managed by another platform must not hold our teardown back: %+v", got)
	}
	if len(got.Remaining) != 0 || len(got.Unsearched) != 0 {
		t.Fatalf("a foreign-managed resource must be dropped, not surfaced: %+v", got)
	}
}

// THE CONTRACT TEST. Sweep and Probe must never disagree about the same
// evidence — a mixed fixture of all four skipReason outcomes must leave the
// sweep not-clean and the probe not-absent, in lockstep. Before this fix the
// same fixture read not-clean from Sweep and absent:true from Probe, and
// Probe is what settles a subscription — so this is the test that catches a
// future regression re-splitting the two readings apart.
func TestProbeAndSweep_AgreeOnASharedFixture(t *testing.T) {
	fixture := []teardown.Resource{
		{ID: "vol-untagged", Tags: map[string]string{}},
		{ID: "vol-inuse", Tags: map[string]string{adopt.TagAppID: "app_1"}, InUse: true},
		{ID: "vol-foreign-app", Tags: map[string]string{adopt.TagAppID: "app_2"}},
		{ID: "vol-foreign-mb", Tags: map[string]string{
			adopt.TagAppID:     "app_1",
			adopt.TagManagedBy: "some-other-platform",
		}},
	}

	sweepStub := &stub{kind: "ebs-volume", found: fixture}
	sweepRep, sweepErr := teardown.Sweep(context.Background(), probeIdentity(), []teardown.Class{sweepStub})
	if sweepErr != nil {
		t.Fatalf("unexpected sweep error: %v", sweepErr)
	}
	if sweepRep.Clean() {
		t.Fatalf("sweep fixture sanity check failed — expected skips to make this not-clean: %+v", sweepRep)
	}

	probeStub := &stub{kind: "ebs-volume", found: fixture}
	probePresence, probeErr := teardown.Probe(context.Background(), probeIdentity(), []teardown.Class{probeStub})
	if probeErr != nil {
		t.Fatalf("unexpected probe error: %v", probeErr)
	}

	if probePresence.Absent {
		t.Fatalf("the sweep sees this app as not-clean (%+v) but the probe says absent:true (%+v) — "+
			"the same fixture must not yield contradictory verdicts", sweepRep, probePresence)
	}
	if len(probePresence.Remaining) != 1 || probePresence.Remaining[0].ID != "vol-inuse" {
		t.Errorf("Remaining must name exactly the in-use resource of ours, got %+v", probePresence.Remaining)
	}
	unsearched := false
	for _, u := range probePresence.Unsearched {
		if u == "ebs-volume" {
			unsearched = true
		}
	}
	if !unsearched {
		t.Errorf("Unsearched must carry the class the untagged resource made unprovable, got %+v", probePresence.Unsearched)
	}
}

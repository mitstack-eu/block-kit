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

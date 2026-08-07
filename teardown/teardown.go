// Package teardown reclaims what the IaC engine never tracked (MIT-1470).
//
// `tofu destroy` (or a CloudFormation stack delete, or a Crossplane claim
// delete) removes what the engine's STATE tracks. This package deals with
// everything state does not: a resource that drifted out — imported and lost, a
// crash mid-apply, a targeted destroy — or one created out of band. Such a
// resource survives a teardown that REPORTS SUCCESS and bills the consumer
// indefinitely. MIT-1133 is a running EC2 instance that outlived its own
// subscription exactly this way; MIT-1402/MIT-1403 are DLM snapshots and the
// state bucket surviving a clean destroy.
//
// Three properties, in order of how badly getting them wrong hurts:
//
//  1. PARTITION SAFETY over completeness. Every discovery is scoped to the
//     app's EXACT identity — never a tag-key filter, never a wildcard — and a
//     resource in use, or carrying another app's id, is never touched. The
//     failure mode of over-reach is deleting another tenant's live
//     infrastructure, which is strictly worse than the leak being fixed.
//
//  2. THE DETECTOR AND THE RECLAIMER ARE THE SAME CODE. MIT-1126:
//     `exit-sweep.sh` filtered on the app NAME while the reclaimer filtered on
//     AppId, so the detector printed CLEAN over live resources. A false
//     all-clear is the worst outcome available here — after it, nothing left in
//     the system will ever mention the leak. Sweep runs ONE Find and applies
//     ONE selection rule, so the two cannot drift apart.
//
//  3. A RECLAIM FAILURE SURFACES. A silent failure is indistinguishable from a
//     clean teardown. Sweep continues past a failure so later classes are still
//     swept, then returns an error naming everything that survived.
//
// Absence is success (MIT-1302/MIT-1278): nine of thirteen operators once
// hard-failed on a half-provisioned resource, turning teardown into an endless
// retry instead of a finish.
package teardown

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/mitstack-eu/operator-kit/adopt"
)

// Resource is one leftover discovered in the provider.
type Resource struct {
	// Kind is the class name ("instance", "volume", "eip", ...).
	Kind string
	// ID is the provider's own id, used to delete and to report.
	ID string
	// Tags as read back from the provider. Identity is decided from these.
	Tags map[string]string
	// InUse marks a resource that is attached, associated or otherwise live.
	// It is skipped and reported, never deleted.
	InUse bool
	// Note is optional context carried into the report.
	Note string
}

// Class is one kind of leftover: how to find it, and how to remove it.
//
// Find queries the PROVIDER, not engine state — that is the entire point. An
// implementation must scope its query to the identity it is given.
type Class interface {
	Kind() string
	Find(ctx context.Context, id adopt.Identity) ([]Resource, error)
	Delete(ctx context.Context, r Resource) error
}

// Skip records a resource that was deliberately left alone, and why. Skips are
// REPORTED rather than dropped: "we chose not to touch this" and "there was
// nothing there" are different facts, and only one of them means clean.
type Skip struct {
	Resource Resource
	Reason   string
}

// Failure records a resource that could not be reclaimed.
type Failure struct {
	Resource Resource
	Err      error
}

// Report is the evidence a teardown test asserts against.
//
// `scripts/check-operator-teardown-tests.sh` RUNS each operator's
// TestUnsubscribeDeletes* rather than grepping for it, so that a rotting test
// fails CI. This is what such a test should be checking.
type Report struct {
	Reclaimed []Resource
	Skipped   []Skip
	Failed    []Failure
	// Unsearched lists classes whose discovery failed. A class that could not
	// be searched is not a class that is clean.
	Unsearched []string
}

// Clean reports whether the sweep both searched everything and left nothing
// behind. A discovery failure makes it false — an unsearched class is not a
// clean one, and reporting otherwise is the false all-clear arrived at from the
// other direction.
func (r Report) Clean() bool {
	return len(r.Failed) == 0 && len(r.Unsearched) == 0 && len(r.Skipped) == 0
}

func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "reclaimed %d", len(r.Reclaimed))
	for _, x := range r.Reclaimed {
		fmt.Fprintf(&b, " %s/%s", x.Kind, x.ID)
	}
	if len(r.Skipped) > 0 {
		fmt.Fprintf(&b, "; skipped %d", len(r.Skipped))
		for _, s := range r.Skipped {
			fmt.Fprintf(&b, " %s/%s(%s)", s.Resource.Kind, s.Resource.ID, s.Reason)
		}
	}
	if len(r.Failed) > 0 {
		fmt.Fprintf(&b, "; FAILED %d", len(r.Failed))
		for _, f := range r.Failed {
			fmt.Fprintf(&b, " %s/%s", f.Resource.Kind, f.Resource.ID)
		}
	}
	if len(r.Unsearched) > 0 {
		fmt.Fprintf(&b, "; UNSEARCHED %s", strings.Join(r.Unsearched, ","))
	}
	return b.String()
}

// Sweep finds and reclaims every leftover across the given classes.
//
// It returns a Report in all cases — including alongside an error, because the
// operator still needs to know what DID get reclaimed before the failure.
func Sweep(ctx context.Context, id adopt.Identity, classes []Class) (Report, error) {
	var rep Report

	// An identity that matches nothing in particular matches everything. Refuse
	// before any class is asked to find, let alone delete.
	if err := id.Validate(); err != nil {
		return rep, fmt.Errorf("teardown: refusing to sweep: %w", err)
	}

	var errs []error

	for _, c := range classes {
		found, err := c.Find(ctx, id)
		if err != nil {
			rep.Unsearched = append(rep.Unsearched, c.Kind())
			errs = append(errs, fmt.Errorf("teardown: %s: discovery failed (this class is UNSEARCHED, not clean): %w", c.Kind(), err))
			continue
		}

		for _, r := range found {
			if r.Kind == "" {
				r.Kind = c.Kind()
			}

			// ONE selection rule, applied here — not re-derived per class, so a
			// class cannot disagree with the sweep about what is ours (MIT-1126).
			if reason, ok := skipReason(id, r); ok {
				rep.Skipped = append(rep.Skipped, Skip{Resource: r, Reason: reason})
				continue
			}

			if err := c.Delete(ctx, r); err != nil {
				// It existed at Find time and is gone now: a race, not a leak.
				if IsNotFound(err) {
					rep.Reclaimed = append(rep.Reclaimed, r)
					continue
				}
				rep.Failed = append(rep.Failed, Failure{Resource: r, Err: err})
				errs = append(errs, fmt.Errorf("teardown: %s/%s still present after reclaim: %w", r.Kind, r.ID, err))
				continue
			}
			rep.Reclaimed = append(rep.Reclaimed, r)
		}
	}

	if len(errs) > 0 {
		return rep, errors.Join(errs...)
	}
	return rep, nil
}

// skipReason decides whether a discovered resource must be left alone. Every
// branch here is a case where deleting would be worse than leaking.
func skipReason(id adopt.Identity, r Resource) (string, bool) {
	owner := r.Tags[adopt.TagAppID]
	switch {
	case owner == "":
		return "untagged — no identity, so it cannot be proven ours", true
	case owner != id.AppID:
		return "belongs to " + owner, true
	}
	if mb, ok := r.Tags[adopt.TagManagedBy]; ok && mb != adopt.ManagedByValue {
		return "managed by " + mb, true
	}
	if r.InUse {
		return "in use", true
	}
	return "", false
}

// notFoundPattern matches the "it is already gone" family across AWS services
// and plain HTTP. Being generous here is safe: a false positive means we treat
// an existing resource as reclaimed, which the next sweep re-discovers — while a
// false negative turns a benign race into a failed teardown.
// `not[ -]?found` rather than `NotFound`: AWS says NotFound, Kubernetes says
// `... "app" not found`, and the space cost a real bug — the Crossplane twin's
// absent-claim teardown still 422'd because the kit only matched the
// concatenated spelling. Found by USING this, not by reading it.
var notFoundPattern = regexp.MustCompile(
	`(?i)(not[ -]?found|NoSuchBucket|NoSuchEntity|NoSuchKey|ResourceNotFoundException|` +
		`does not exist|cannot be found|404)`)

// IsNotFound reports whether err means the resource is already gone — which is
// SUCCESS for a teardown, not a failure (MIT-1302/MIT-1278).
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	return notFoundPattern.MatchString(err.Error())
}

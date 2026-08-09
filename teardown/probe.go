package teardown

// probe.go — is this app's infrastructure gone yet? (MIT-1694)
//
// `ProbeAbsence` asks a block operator whether the resources it provisioned
// are gone, and the answer rides back on the `validate` verb's response as an
// explicit `absent` marker. Until now no operator emitted one, so
// `verifyTeardownAndSettle` failed open and settled the subscription anyway.
// That is the right call for a teardown — the alternative traps a consumer who
// wants out — but it makes "the old infrastructure is gone" an assumption.
//
// A stack switch is where the assumption stops being survivable. The three AWS
// infra blocks hardcode account-unique names (the ECR repository and both IAM
// roles), so the target stack cannot be created until the source stack's
// resources are genuinely gone. A premature "yes" does not fail cleanly: it
// collides, and the OpenTofu engine's self-repair imports would then adopt
// resources another engine owns, so a later delete destroys infrastructure the
// first engine still believes is its own.
//
// Probe reuses each class's own Find rather than asking the provider a second,
// differently-worded question. That is the package's existing property #2 —
// the detector and the reclaimer are the same code — applied to a third
// caller. MIT-1126 is what happens when they diverge: `exit-sweep.sh` filtered
// on the app name while the reclaimer filtered on AppId, so the detector
// printed CLEAN over live resources.

import (
	"context"
	"errors"
	"fmt"

	"github.com/mitstack-eu/operator-kit/adopt"
)

// Presence is the evidence behind an `absent` marker.
//
// Remaining and Unsearched are both reported because they are different facts
// with the same consequence: one means we found something, the other means we
// could not look. Only the first can be acted on, and neither is absent.
type Presence struct {
	// Absent is true only when every class was searched successfully and none
	// of them found a resource belonging to this app.
	Absent bool
	// Remaining lists what is still there and ours.
	Remaining []Resource
	// Unsearched lists classes whose discovery failed.
	Unsearched []string
}

// Probe reports whether anything a Sweep would reclaim is still present,
// WITHOUT deleting anything.
//
// Read-only is not a stylistic choice. Every `validate` carries the marker,
// including validates against a live, healthy subscription, so a probe with a
// side effect would tear an app down merely by being asked how it is.
//
// A discovery failure is never absent, and it also returns an error, so a
// caller that ignores the error still cannot mistake "we could not look" for
// "there is nothing there". That is the false all-clear this package exists to
// prevent, arrived at from the reporting side instead of the reclaiming side.
func Probe(ctx context.Context, id adopt.Identity, classes []Class) (Presence, error) {
	var p Presence

	// An identity that matches nothing in particular matches everything.
	// Refuse before any class is asked to find.
	if err := id.Validate(); err != nil {
		return p, fmt.Errorf("teardown: refusing to probe: %w", err)
	}

	var errs []error

	for _, c := range classes {
		found, err := c.Find(ctx, id)
		if err != nil {
			p.Unsearched = append(p.Unsearched, c.Kind())
			errs = append(errs, fmt.Errorf("teardown: %s: discovery failed (this class is UNSEARCHED, not absent): %w", c.Kind(), err))
			continue
		}

		for _, r := range found {
			if r.Kind == "" {
				r.Kind = c.Kind()
			}
			// The SAME ownership rule the sweep applies, from the same
			// function — a resource the sweep would refuse to delete because
			// it belongs to another app says nothing about whether ours is
			// gone, and counting it would keep a finished teardown looking
			// unfinished forever.
			if _, skipped := skipReason(id, r); skipped {
				continue
			}
			p.Remaining = append(p.Remaining, r)
		}
	}

	p.Absent = len(p.Remaining) == 0 && len(p.Unsearched) == 0

	if len(errs) > 0 {
		return p, errors.Join(errs...)
	}
	return p, nil
}

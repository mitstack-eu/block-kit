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
	// Unsearched lists classes we could not clear — either Find itself
	// failed, or Find succeeded but returned a resource with no owner tag,
	// so we could not attribute it to this app or rule it out (MIT-1949).
	// Both facts mean the same thing to a caller: not provably absent. An
	// entry here does NOT imply a discovery error — check the returned error
	// for that, never infer it from this field being non-empty.
	Unsearched []string
}

// Probe reports whether anything a Sweep would reclaim is still present,
// WITHOUT deleting anything.
//
// Read-only is not a stylistic choice. Every `validate` carries the marker,
// including validates against a live, healthy subscription, so a probe with a
// side effect would tear an app down merely by being asked how it is.
//
// A Find failure is never absent, and it also returns an error, so a caller
// that ignores the error still cannot mistake "we could not look" for "there
// is nothing there". That is the false all-clear this package exists to
// prevent, arrived at from the reporting side instead of the reclaiming side.
//
// An unattributable resource reaches Unsearched by the SAME "not provably
// absent" logic but WITHOUT an error (MIT-1949, see skipCannotTell below) —
// Absent:false is the only signal, deliberately, because of what the error
// channel does downstream: verifyTeardownAndSettle fails OPEN on any probe
// error it does not specifically recognise and settles the subscription
// anyway. An error here would settle a teardown over infrastructure we could
// not even attribute — the exact false all-clear this comment starts with,
// reached through the error channel instead of the absence one.
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

		// classUnprovable is set once any resource in THIS class came back
		// untagged. One flag per class, checked after the resource loop, so
		// several untagged resources from the same class add "unsearched"
		// only once instead of once per resource.
		classUnprovable := false

		for _, r := range found {
			if r.Kind == "" {
				r.Kind = c.Kind()
			}
			// The SAME ownership rule the sweep applies, from the same
			// function (MIT-1949) — but the three refusal kinds it can
			// return are not equivalent evidence for a caller answering
			// "is our app gone", so each is read on its own terms instead of
			// being uniformly dropped:
			switch _, kind := skipReason(id, r); kind {
			case skipForeign:
				// Belongs to another app, or another platform's ManagedBy.
				// Says nothing about OUR app, so it is dropped exactly as
				// the sweep drops it — counting it would keep a finished
				// teardown looking unfinished forever.
			case skipCannotTell:
				// No owner tag at all: we cannot prove this is or is not
				// ours. That is the Unsearched fact in a different shape —
				// reporting it as absence would be the false all-clear this
				// package exists to prevent, arrived at through a resource
				// instead of a discovery failure.
				//
				// This deliberately does NOT append to errs. The only signal
				// is Absent:false via Unsearched; Probe must return a nil
				// error for this case alone. verifyTeardownAndSettle fails
				// OPEN on any probe error it does not specifically
				// recognise and settles the subscription regardless — so
				// surfacing an error here would settle a teardown over a
				// resource we could not even attribute, which is the false
				// all-clear this whole change exists to close, reopened
				// through the error channel instead of the absence one.
				classUnprovable = true
			default:
				// notSkipped (ours, not in use — the sweep would delete it)
				// or skipOwnStillStanding (ours, in use — the sweep leaves
				// it standing). Either way it exists right now, which is
				// affirmative evidence our app is not gone yet.
				p.Remaining = append(p.Remaining, r)
			}
		}

		if classUnprovable {
			p.Unsearched = append(p.Unsearched, c.Kind())
		}
	}

	p.Absent = len(p.Remaining) == 0 && len(p.Unsearched) == 0

	if len(errs) > 0 {
		return p, errors.Join(errs...)
	}
	return p, nil
}

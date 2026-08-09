// Package handover carries a block's durable data across a REPLACEMENT
// (MIT-1598, ADR 0049).
//
// # The problem it exists for
//
// Switching an app between the three `aws-topology` providers is necessarily
// destroy-then-create: all three hardcode the same globally-unique AWS names
// (ECR repo `mitstack-app-<appName>`, IAM roles `mitstack-app-<appName>-instance`
// and `-deploy`), and IAM role and ECR repository names are account-unique. The
// two topologies cannot coexist, so the old one must be gone before the new one
// exists — which means the app's data volume cannot simply stay attached.
//
// It therefore has to be HANDED OVER: the outgoing block snapshots the volume
// and publishes the snapshot id; the incoming block restores from it. RPO
// becomes the duration of the switch.
//
// # The sharp edge, and it is the whole package
//
// **A snapshot owned by the stack being destroyed is not a handover. It is a
// slower deletion.**
//
// The obvious implementations all fall into this. A `snapshot` resource added
// to the OpenTofu module is destroyed by the same `tofu destroy`. CloudFormation's
// `DeletionPolicy: Snapshot` is closer, but hands back no id without a describe
// and is silent about whether the snapshot finished. A Crossplane composed
// resource is deleted with its claim.
//
// So the snapshot is taken through the EC2 API DIRECTLY, outside whatever state
// the engine keeps, and tagged as a deliberately-retained artifact. It survives
// the teardown by construction rather than by policy — no state file, stack or
// claim owns it and could take it along.
//
// That deliberately-retained artifact has to be reconciled with two existing
// rules, and both reconciliations are the point rather than an exception:
//
//   - The TEARDOWN contract (MIT-1124) says unsubscribe deletes everything the
//     block provisioned. A handover snapshot is the one exception, and it is a
//     narrow, tagged, provable one: `Tag()` marks it, and each operator's
//     teardown test asserts it is the ONLY thing left. An exception you can
//     enumerate is not a weakened contract.
//   - Orphan SWEEPS (MIT-1402/1403) reclaim what a teardown left behind. A
//     handover snapshot looks exactly like an orphan, so a sweep must exclude
//     it by tag. `IsHandover` is that predicate, living here rather than in
//     each sweeper, because a sweep that reclaims the handover turns a
//     preserved volume into a lost one at some arbitrary later time.
//
// # Why this is SDK-free
//
// The operator-kit module has no dependencies at all, and this package keeps it
// that way: the EC2 surface is a three-method domain interface each operator
// adapts to its own client. What is shared is the part that is easy to get
// wrong — the ORDER of operations, the refusal rules, waiting for the snapshot
// to actually complete, and the tagging that both the teardown exception and
// the sweep exclusion key on. None of that needs the SDK, and all of it is
// testable without one.
package handover

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// OutputKey is the block output that carries the snapshot id from the outgoing
// block to the incoming one. It is also the `handoverOutput` every
// `spec.continuity` declaration names — a mismatch between the two is a
// handover that silently does nothing, which is why the planner treats a
// differing name as a blocker rather than a warning.
const OutputKey = "dataSnapshotId"

// ParamKey is the block param a subscribing block reads to restore from a
// snapshot instead of creating an empty volume. Deliberately the same spelling
// as OutputKey, so the engine's auto-projection (m-55) carries it with no
// mapping table.
const ParamKey = "dataSnapshotId"

// Tags applied to every handover snapshot.
//
// TagRole is the one that matters operationally: it is what tells an orphan
// sweep this snapshot is retained ON PURPOSE. Without it a sweep reclaims the
// handover, and the failure appears long after the switch, as data that was
// preserved and then quietly was not.
const (
	TagRole   = "mitstack:handover"
	TagAppID  = "mitstack:app-id"
	RoleData  = "data"
	StateDone = "completed"
	StateFail = "error"
)

// ErrNoVolume reports that the app has no data volume to snapshot.
//
// This is NOT a failure for a teardown: an app torn down before its volume was
// ever created has nothing to hand over, and failing the unsubscribe would
// strand the subscription. Callers treat it as "no handover" and continue.
var ErrNoVolume = errors.New("handover: no data volume found for this app")

// API is the minimal EC2 surface a snapshot handover needs, expressed in domain
// terms so the kit stays dependency-free. Each operator adapts its own client.
type API interface {
	// FindDataVolume returns the volume id of appID's data volume, or
	// ErrNoVolume if it has none.
	FindDataVolume(ctx context.Context, appID string) (string, error)
	// CreateSnapshot starts a snapshot of volumeID with the given tags and
	// returns its id. It does NOT wait.
	CreateSnapshot(ctx context.Context, volumeID, description string, tags map[string]string) (string, error)
	// SnapshotState returns the snapshot's current state — "pending",
	// "completed" or "error" in EC2's vocabulary.
	SnapshotState(ctx context.Context, snapshotID string) (string, error)
}

// Clock is the sleep seam, so the wait loop is testable without real time.
type Clock interface {
	Sleep(d time.Duration)
	Now() time.Time
}

type realClock struct{}

func (realClock) Sleep(d time.Duration) { time.Sleep(d) }
func (realClock) Now() time.Time        { return time.Now() }

// Options tune the wait. Zero values select sane defaults, so a caller passing
// Options{} gets correct behaviour rather than an instant timeout.
type Options struct {
	// Timeout bounds the wait for the snapshot to complete. A snapshot of a
	// 10 GiB volume normally completes in a few minutes; the default is
	// generous because the alternative to waiting is handing back an id that
	// is not yet restorable.
	Timeout time.Duration
	// Poll is the interval between state checks.
	Poll time.Duration
	// Clock is injected by tests.
	Clock Clock
}

func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = 15 * time.Minute
	}
	if o.Poll <= 0 {
		o.Poll = 5 * time.Second
	}
	if o.Clock == nil {
		o.Clock = realClock{}
	}
	return o
}

// Tag returns the tag set every handover snapshot must carry.
//
// Exported because the teardown exception and the sweep exclusion are two
// different pieces of code that must agree on it exactly; a sweeper matching a
// tag nobody writes reclaims every handover.
func Tag(appID string) map[string]string {
	return map[string]string{
		TagRole:  RoleData,
		TagAppID: appID,
		// `AppId` too, in the repo's existing spelling. Every other per-app
		// resource carries it and every discovery filters on it, so a handover
		// snapshot that omitted it would be invisible to the tooling an
		// operator reaches for during an incident.
		//
		// It also makes the sweep exclusion LOAD-BEARING rather than
		// incidental: with this tag the orphan sweep genuinely finds the
		// handover snapshot, and only IsHandover keeps it from being deleted.
		// Relying on a tag mismatch to protect the data would be an accident
		// that a future tagging change silently undoes.
		"AppId": appID,
	}
}

// IsHandover reports whether a resource's tags mark it as a deliberately
// retained handover artifact. Sweeps MUST consult this before reclaiming a
// snapshot (MIT-1402/1403).
func IsHandover(tags map[string]string) bool {
	return tags[TagRole] == RoleData
}

// Snapshot takes a handover snapshot of appID's data volume and returns its id
// once the snapshot has COMPLETED.
//
// It waits on purpose. A snapshot id is returned to be restored from, and EC2
// hands one back the instant the call is accepted — long before the data is
// captured. Publishing that id would produce a handover that looks complete,
// restores a partial volume, and is discovered during a recovery.
//
// Returns ErrNoVolume when the app has no data volume; callers treat that as
// "nothing to hand over", never as a failure.
func Snapshot(ctx context.Context, api API, appID string, opts Options) (string, error) {
	if api == nil {
		// A nil API is a WIRING mistake and must be loud. Returning ("", nil)
		// would read to the caller as "there was nothing to hand over" — and
		// the caller's next step is destroying the volume.
		return "", errors.New("handover: no EC2 API configured — refusing to report a successful handover that never ran")
	}
	if appID == "" {
		return "", errors.New("handover: appID is required — an untagged snapshot cannot be found again")
	}
	o := opts.withDefaults()

	volumeID, err := api.FindDataVolume(ctx, appID)
	if err != nil {
		return "", err
	}
	if volumeID == "" {
		return "", ErrNoVolume
	}

	// ASCII only. EC2 rejects a non-ASCII snapshot description outright, and
	// that exact failure — an em dash in a `--description` — blocked every
	// data-touching apply until it was found (MIT-1543).
	desc := fmt.Sprintf("mitstack handover snapshot for app %s", appID)

	snapshotID, err := api.CreateSnapshot(ctx, volumeID, desc, Tag(appID))
	if err != nil {
		return "", fmt.Errorf("handover: create snapshot of %s: %w", volumeID, err)
	}
	if snapshotID == "" {
		return "", errors.New("handover: the snapshot call succeeded but returned no id — refusing to report a handover that cannot be restored")
	}

	deadline := o.Clock.Now().Add(o.Timeout)
	for {
		state, err := api.SnapshotState(ctx, snapshotID)
		if err != nil {
			return "", fmt.Errorf("handover: reading state of %s: %w", snapshotID, err)
		}
		switch state {
		case StateDone:
			return snapshotID, nil
		case StateFail:
			return "", fmt.Errorf("handover: snapshot %s reported state %q — the data was NOT captured", snapshotID, state)
		}

		if !o.Clock.Now().Before(deadline) {
			// Deliberately an error, not a best-effort success. An incomplete
			// snapshot is exactly where reporting success is worst: the caller
			// would go on to destroy the volume it describes.
			return "", fmt.Errorf("handover: snapshot %s did not complete within %s (last state %q) — the volume must NOT be destroyed",
				snapshotID, o.Timeout, state)
		}
		o.Clock.Sleep(o.Poll)
	}
}

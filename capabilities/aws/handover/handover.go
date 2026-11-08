// Package handover, an optional helper for AWS, carries a block's durable data across a replacement by snapshotting its EBS data volume through the EC2 API, outside any engine state.
package handover

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// OutputKey must match every spec.continuity handoverOutput, and ParamKey matches it so the platform projects one onto the other with no mapping.
const (
	OutputKey = "dataSnapshotId"
	ParamKey  = "dataSnapshotId"
)

// TagRole marks a snapshot an orphan sweep must keep.
const (
	TagRole   = "mitstack:handover"
	TagAppID  = "mitstack:app-id"
	RoleData  = "data"
	StateDone = "completed"
	StateFail = "error"
)

// ErrNoVolume reports an app with no data volume to snapshot, which is no teardown failure.
var ErrNoVolume = errors.New("handover: no data volume found for this app")

type API interface {
	FindDataVolume(ctx context.Context, appID string) (string, error)
	CreateSnapshot(ctx context.Context, volumeID, description string, tags map[string]string) (string, error)
	SnapshotState(ctx context.Context, snapshotID string) (string, error)
}

type Clock interface {
	Sleep(d time.Duration)
	Now() time.Time
}

type realClock struct{}

func (realClock) Sleep(d time.Duration) { time.Sleep(d) }
func (realClock) Now() time.Time        { return time.Now() }

type Options struct {
	Timeout time.Duration
	Poll    time.Duration
	Clock   Clock
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

func Tag(appID string) map[string]string {
	return map[string]string{
		TagRole:  RoleData,
		TagAppID: appID,
		"AppId":  appID,
	}
}

func IsHandover(tags map[string]string) bool {
	return tags[TagRole] == RoleData
}

// Snapshot returns the snapshot's id only once it has completed, since EC2 returns an id before the data is captured and the caller destroys the volume next.
func Snapshot(ctx context.Context, api API, appID string, opts Options) (string, error) {
	if api == nil {
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

	// ASCII only: EC2 rejects a non-ASCII description.
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
			return "", fmt.Errorf("handover: snapshot %s did not complete within %s (last state %q) — the volume must NOT be destroyed",
				snapshotID, o.Timeout, state)
		}
		o.Clock.Sleep(o.Poll)
	}
}

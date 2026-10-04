package kittest

import (
	"context"
	"errors"
	"sync"

	"github.com/mitstack-eu/operator-kit/contract"
)

// FakeResource is a Resource whose behaviour a test dictates (MIT-3070). Its
// method set matches handler.Resource and handler.DeclaredResource
// structurally, so an operator, or the kit's own handler tests, can drive a
// real lifecycle handler over it as a single resource or as one declared
// resource of several.
//
// The zero value is a conforming resource: Ensure/Observe/Delete converge
// Count normally. Every other field turns on one specific misbehaviour, so
// a harness test can drive CheckVerbSemantics to a known violation on
// purpose.
type FakeResource struct {
	mu sync.Mutex

	// Count is the number of resources currently provisioned.
	Count int
	// ObserveState is the state the next Observe reports. Defaults to
	// Present when Count > 0 and Absent when Count == 0; set it to
	// override.
	ObserveState *contract.State
	// Outputs the Observe reports.
	Outputs map[string]string
	// StatusDetail the Observe reports.
	StatusDetail string

	// EnsureErr, ObserveErr, DeleteErr are returned by the matching method.
	EnsureErr, ObserveErr, DeleteErr error

	// DuplicateOnEnsure makes every Ensure add a resource instead of
	// converging, so a test can drive the harness to its duplication
	// verdict.
	DuplicateOnEnsure bool
	// LeakOnDelete leaves one resource behind on Delete.
	LeakOnDelete bool
	// ErrorWhenAlreadyAbsent makes Delete fail when nothing is provisioned.
	ErrorWhenAlreadyAbsent bool
	// ClobberOnUpgrade drops the pre-existing outputs on a second Ensure.
	ClobberOnUpgrade bool
	// DeadReportedPending makes Observe answer Unknown with no error even
	// where ObserveErr is set, modelling an operator that never expresses
	// deadness and lets a subscription wait forever (MIT-1291).
	DeadReportedPending bool

	// EnsureCalls, ObserveCalls, DeleteCalls count the calls made, whether
	// or not each one succeeded.
	EnsureCalls, ObserveCalls, DeleteCalls int
}

// Ensure converges Count to a provisioned resource. Under DuplicateOnEnsure
// it instead adds a resource on every call, so a retried subscribe
// duplicates rather than converges. Under ClobberOnUpgrade, any Ensure call
// after the first drops whatever Outputs a previous call left behind,
// modelling a destructive upgrade.
func (f *FakeResource) Ensure(ctx context.Context, d contract.Desired) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.EnsureCalls++

	if f.EnsureErr != nil {
		return f.EnsureErr
	}

	if f.ClobberOnUpgrade && f.EnsureCalls > 1 {
		f.Outputs = nil
	}

	if f.DuplicateOnEnsure {
		f.Count++
	} else if f.Count == 0 {
		f.Count = 1
	}
	return nil
}

// Observe reports Present when Count > 0 and Absent when Count == 0, unless
// ObserveState overrides the reported state. Under DeadReportedPending it
// always reports Unknown with no error, even when ObserveErr is set, ahead
// of any other check.
func (f *FakeResource) Observe(ctx context.Context, id contract.Identity) (contract.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ObserveCalls++

	if f.DeadReportedPending {
		return contract.Observation{State: contract.Unknown, Outputs: f.Outputs, StatusDetail: f.StatusDetail}, nil
	}
	if f.ObserveErr != nil {
		return contract.Observation{}, f.ObserveErr
	}

	state := contract.Absent
	if f.Count > 0 {
		state = contract.Present
	}
	if f.ObserveState != nil {
		state = *f.ObserveState
	}
	return contract.Observation{State: state, Outputs: f.Outputs, StatusDetail: f.StatusDetail}, nil
}

// Delete converges Count to zero. Absence is success: calling Delete when
// nothing is provisioned returns nil, unless ErrorWhenAlreadyAbsent is set.
// Under LeakOnDelete, at least one resource always survives.
func (f *FakeResource) Delete(ctx context.Context, id contract.Identity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.DeleteCalls++

	if f.DeleteErr != nil {
		return f.DeleteErr
	}

	if f.Count == 0 {
		if f.ErrorWhenAlreadyAbsent {
			return errors.New("kittest: FakeResource.Delete called with nothing provisioned (ErrorWhenAlreadyAbsent)")
		}
		return nil
	}

	if f.LeakOnDelete {
		if f.Count > 1 {
			f.Count--
		}
		return nil
	}

	f.Count = 0
	return nil
}

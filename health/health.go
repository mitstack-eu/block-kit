// Package health makes `validate` a verb that ACTS, not one that only reports
// (MIT-1471).
//
// `validate` is what makes a block operable rather than merely installable, and
// aws-app-infra's is the only serious one on the platform: MIT-1119/MIT-1121
// report unhealthy for a dead box and REPLACE an instance that ran but never
// SSM-registered.
//
// Two of the rules here have a DESTRUCTIVE failure mode, which is why they are
// named, separately-tested guarantees rather than incidental behaviour:
//
//   - MIT-946 — a placeholder must NEVER replace serving content. A self-heal
//     cycle once overwrote a consumer's live app with the scaffold placeholder.
//   - MIT-948 — a recreated resource comes back at LAST-GOOD, not at the
//     placeholder, or self-heal hands the consumer an empty box in place of
//     their working one.
//
// And remediation is BOUNDED. An unbounded self-heal loop recreates a resource
// forever on the consumer's bill, which is a worse outcome than the unhealthy
// resource it was trying to fix.
package health

import (
	"context"
	"errors"
	"fmt"
)

// Status is what a probe concluded about a resource.
type Status int

const (
	// Converging — not healthy yet, but not dead. The zero value, because an
	// unset status must be the one that neither claims health nor triggers a
	// destructive remediation.
	Converging Status = iota
	// Healthy — serving.
	Healthy
	// Dead — will not recover by waiting; remediation is warranted.
	Dead
)

func (s Status) String() string {
	switch s {
	case Healthy:
		return "healthy"
	case Dead:
		return "dead"
	default:
		return "converging"
	}
}

// Check reports a resource's status. Distinguishing Converging from Dead is the
// caller's most important judgement: remediating a converging resource destroys
// something that was about to work.
type Check func(ctx context.Context) (Status, error)

// Remediate attempts to fix a Dead resource.
type Remediate func(ctx context.Context) error

// Content describes what a resource is currently serving, for the placeholder
// and last-good rules.
type Content struct {
	// Ref identifies what is deployed (an image digest, a commit sha).
	Ref string
	// IsPlaceholder marks the scaffold's stand-in content.
	IsPlaceholder bool
	// Serving is true when this content is actually being served to users.
	Serving bool
}

// ErrRemediationExhausted is returned when the attempt cap is reached.
var ErrRemediationExhausted = errors.New("health: remediation attempts exhausted")

// ErrWouldReplaceServingContent is returned when a promotion would put a
// placeholder over content that is serving (MIT-946).
var ErrWouldReplaceServingContent = errors.New("health: refusing to replace serving content with a placeholder")

// Healer runs check, and remediates when the resource is Dead — at most
// MaxAttempts times.
type Healer struct {
	Check     Check
	Remediate Remediate
	// MaxAttempts bounds remediation. Zero means ONE attempt, not unlimited:
	// the fail-open reading of a missing bound is what bills a consumer forever.
	MaxAttempts int

	attempts int
}

// Run probes once and remediates if warranted. It returns the status observed
// BEFORE any remediation, plus whether remediation ran, so a caller can narrate
// honestly rather than reporting the post-fix state as if nothing happened.
func (h *Healer) Run(ctx context.Context) (Status, bool, error) {
	st, err := h.Check(ctx)
	if err != nil {
		return Converging, false, fmt.Errorf("health: probe failed: %w", err)
	}
	if st != Dead {
		// Converging is explicitly NOT remediated. Replacing a resource that was
		// about to come up is how a self-heal turns a slow boot into a loop.
		return st, false, nil
	}
	if h.Remediate == nil {
		return st, false, nil
	}

	cap := h.MaxAttempts
	if cap <= 0 {
		cap = 1
	}
	if h.attempts >= cap {
		return st, false, fmt.Errorf("%w after %d attempt(s)", ErrRemediationExhausted, h.attempts)
	}
	h.attempts++

	if err := h.Remediate(ctx); err != nil {
		return st, true, fmt.Errorf("health: remediation failed: %w", err)
	}
	return st, true, nil
}

// Attempts reports how many remediations have been made.
func (h *Healer) Attempts() int { return h.attempts }

// Promote decides what content a resource should come back with after being
// recreated, given what it was serving (current) and the last known-good
// content.
//
// Two rules, both with a destructive failure mode:
//
//   - A placeholder never replaces content that is SERVING (MIT-946).
//   - A recreated resource restores LAST-GOOD where one exists, not the
//     placeholder (MIT-948).
func Promote(current, lastGood, candidate Content) (Content, error) {
	if candidate.IsPlaceholder && current.Serving && !current.IsPlaceholder {
		return Content{}, fmt.Errorf("%w: %q is serving", ErrWouldReplaceServingContent, current.Ref)
	}
	if candidate.IsPlaceholder && lastGood.Ref != "" && !lastGood.IsPlaceholder {
		// MIT-948: a recreated box comes back at last-good, not at the
		// placeholder. Returning the placeholder here hands the consumer an
		// empty box in place of their working one.
		return lastGood, nil
	}
	return candidate, nil
}

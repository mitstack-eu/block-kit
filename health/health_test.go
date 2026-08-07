package health_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mitstack-eu/operator-kit/health"
)

func check(s health.Status) health.Check {
	return func(context.Context) (health.Status, error) { return s, nil }
}

func TestStatus_ZeroValueIsConverging(t *testing.T) {
	// An unset status must neither claim health nor trigger a destructive
	// remediation. Both of the other two do one of those.
	var s health.Status
	if s != health.Converging {
		t.Errorf("the zero Status = %v, want Converging", s)
	}
}

func TestRun_HealthyIsNotRemediated(t *testing.T) {
	var remediated bool
	h := &health.Healer{Check: check(health.Healthy), Remediate: func(context.Context) error {
		remediated = true
		return nil
	}}

	st, did, err := h.Run(context.Background())
	if err != nil || st != health.Healthy || did || remediated {
		t.Errorf("st=%v did=%v remediated=%v err=%v", st, did, remediated, err)
	}
}

func TestRun_ConvergingIsNotRemediated(t *testing.T) {
	// The important one. Replacing a resource that was about to come up is how a
	// self-heal turns a slow boot into a destructive loop.
	var remediated bool
	h := &health.Healer{Check: check(health.Converging), Remediate: func(context.Context) error {
		remediated = true
		return nil
	}}

	_, did, err := h.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if did || remediated {
		t.Error("a converging resource must never be remediated")
	}
}

func TestRun_DeadIsRemediated(t *testing.T) {
	var remediated bool
	h := &health.Healer{Check: check(health.Dead), MaxAttempts: 2, Remediate: func(context.Context) error {
		remediated = true
		return nil
	}}

	st, did, err := h.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st != health.Dead {
		t.Errorf("Run must report the status observed BEFORE remediation, got %v", st)
	}
	if !did || !remediated {
		t.Error("a dead resource should be remediated")
	}
}

func TestRun_RemediationIsBounded(t *testing.T) {
	// An unbounded self-heal loop recreates a resource forever on the consumer's
	// bill — a worse outcome than the unhealthy resource it was fixing.
	var calls int
	h := &health.Healer{Check: check(health.Dead), MaxAttempts: 2, Remediate: func(context.Context) error {
		calls++
		return nil
	}}

	for range 5 {
		_, _, _ = h.Run(context.Background())
	}

	if calls != 2 {
		t.Errorf("remediated %d times, want the 2-attempt cap honoured", calls)
	}
	if _, _, err := h.Run(context.Background()); !errors.Is(err, health.ErrRemediationExhausted) {
		t.Errorf("err = %v, want ErrRemediationExhausted so the caller can report why it stopped", err)
	}
}

func TestRun_ZeroMaxAttemptsMeansOneNotUnlimited(t *testing.T) {
	// The fail-open reading of a missing bound is what bills a consumer forever.
	var calls int
	h := &health.Healer{Check: check(health.Dead), Remediate: func(context.Context) error {
		calls++
		return nil
	}}

	for range 4 {
		_, _, _ = h.Run(context.Background())
	}
	if calls != 1 {
		t.Errorf("remediated %d times with MaxAttempts=0, want exactly 1", calls)
	}
}

func TestRun_AProbeFailureIsConvergingNotDead(t *testing.T) {
	// If we could not determine health, we must not conclude death — that
	// conclusion triggers a destructive action.
	h := &health.Healer{
		Check:     func(context.Context) (health.Status, error) { return health.Dead, errors.New("api timeout") },
		Remediate: func(context.Context) error { t.Fatal("must not remediate on an unreadable probe"); return nil },
	}

	st, did, err := h.Run(context.Background())
	if err == nil {
		t.Fatal("a probe failure must surface")
	}
	if st != health.Converging || did {
		t.Errorf("st=%v did=%v — an unreadable probe must not be treated as dead", st, did)
	}
}

// --- The two destructive-failure-mode rules ---------------------------------

func TestPromote_APlaceholderNeverReplacesServingContent(t *testing.T) {
	// MIT-946: a self-heal cycle once overwrote a consumer's live app with the
	// scaffold placeholder.
	current := health.Content{Ref: "sha-real", Serving: true}
	candidate := health.Content{Ref: "placeholder", IsPlaceholder: true}

	_, err := health.Promote(current, health.Content{}, candidate)
	if !errors.Is(err, health.ErrWouldReplaceServingContent) {
		t.Fatalf("err = %v, want ErrWouldReplaceServingContent", err)
	}
}

func TestPromote_ARecreatedResourceComesBackAtLastGood(t *testing.T) {
	// MIT-948: returning the placeholder here hands the consumer an empty box in
	// place of their working one.
	current := health.Content{} // freshly recreated, serving nothing
	lastGood := health.Content{Ref: "sha-lastgood"}
	candidate := health.Content{Ref: "placeholder", IsPlaceholder: true}

	got, err := health.Promote(current, lastGood, candidate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Ref != "sha-lastgood" {
		t.Errorf("promoted %q, want the last-good content", got.Ref)
	}
}

func TestPromote_ThePlaceholderIsFineOnAGenuinelyNewResource(t *testing.T) {
	// First provision: nothing serving, no last-good. The placeholder is exactly
	// right, and refusing it would block the normal path.
	got, err := health.Promote(health.Content{}, health.Content{}, health.Content{Ref: "placeholder", IsPlaceholder: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Ref != "placeholder" {
		t.Errorf("promoted %q, want the placeholder on a genuinely new resource", got.Ref)
	}
}

func TestPromote_RealContentAlwaysWins(t *testing.T) {
	current := health.Content{Ref: "sha-old", Serving: true}
	candidate := health.Content{Ref: "sha-new"}

	got, err := health.Promote(current, health.Content{Ref: "sha-lastgood"}, candidate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Ref != "sha-new" {
		t.Errorf("promoted %q, want the real new content", got.Ref)
	}
}

func TestPromote_APlaceholderMayReplaceAPlaceholder(t *testing.T) {
	// The guard is about SERVING REAL content, not about placeholders as such.
	current := health.Content{Ref: "placeholder", IsPlaceholder: true, Serving: true}
	got, err := health.Promote(current, health.Content{}, health.Content{Ref: "placeholder-v2", IsPlaceholder: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Ref != "placeholder-v2" {
		t.Errorf("promoted %q", got.Ref)
	}
}

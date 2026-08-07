package kittest

import (
	"fmt"
	"sort"
	"testing"
)

// Lifecycle-verb semantics harness (MIT-1140).
//
// Every operator is expected to honour the same four properties, and every
// operator has been proving them — or not — its own way, in its own words. The
// result is that "idempotent" means something slightly different in thirteen
// places, and the ones where it means nothing at all are indistinguishable from
// the ones where it means something.
//
// This states the properties ONCE, as executable expectations an operator
// supplies its own seams to. It cannot run an operator for you: subscribe needs
// that operator's provisioner, its fakes, its request shape. What it can do is
// make the four properties impossible to state vaguely, and impossible to
// satisfy by accident.
//
// The properties, and why each is here:
//
//   - IDEMPOTENT SUBSCRIBE — subscribe is RETRIED. Not might be, is. An operator
//     that duplicates on the second call has a defect that only appears under
//     the conditions where it does the most damage (T-501, T-585, MIT-1229).
//   - UNSUBSCRIBE TEARS DOWN — and an absent resource is SUCCESS, not an error.
//     Nine of thirteen operators once hard-failed on a half-provisioned
//     resource, turning teardown into an endless retry (MIT-1302, MIT-1278).
//   - CONVERGENT UPGRADE — upgrade folds forward without clobbering. A
//     destructive upgrade is worse than a failed one.
//   - VALIDATE DISTINGUISHES CONVERGING FROM DEAD — the distinction MIT-1293
//     depends on, which only holds if the operator expresses it. Both twins
//     shipped without it (MIT-1480/MIT-1481).

// VerbSeams is what an operator supplies to have its verb semantics checked.
// Every field is required; a nil seam FAILS rather than skipping, because a
// harness that quietly skips the property it was asked to check is the vacuous
// pass this package exists to prevent.
type VerbSeams struct {
	// Subscribe performs a subscribe and reports how many resources exist
	// afterwards. Called twice by the idempotency check.
	Subscribe func() (resourceCount int, err error)

	// Unsubscribe tears down, and reports how many resources remain.
	Unsubscribe func() (remaining int, err error)

	// UnsubscribeWhenAbsent tears down when nothing was ever provisioned. It
	// must NOT error.
	UnsubscribeWhenAbsent func() error

	// Upgrade folds a change forward and reports whether pre-existing state
	// survived it.
	Upgrade func() (preservedExistingState bool, err error)

	// ValidateWhileConverging reports the state an operator returns for a
	// resource that is still coming up. It must be pending — not failed.
	ValidateWhileConverging func() (pending bool, isError bool)

	// ValidateWhenDead reports the state for a resource that will not recover.
	// It must NOT be pending.
	ValidateWhenDead func() (pending bool, isError bool)
}

// CheckVerbSemantics runs the four properties and returns one problem string
// per violation, empty when the operator conforms.
//
// It is a pure function rather than something that talks to *testing.T, for one
// reason that turned out to matter: the first version used t.Run subtests, and
// its own self-tests could not exercise it — t.Run on a bare &testing.T{} never
// executes, so every "the harness catches X" test passed while checking
// nothing. A harness whose own tests are theatre is precisely what this package
// exists to prevent, so the harness had to be made testable before it could be
// trusted.
func CheckVerbSemantics(s VerbSeams) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, sprintf(format, args...)) }

	for name, isNil := range map[string]bool{
		"Subscribe":               s.Subscribe == nil,
		"Unsubscribe":             s.Unsubscribe == nil,
		"UnsubscribeWhenAbsent":   s.UnsubscribeWhenAbsent == nil,
		"Upgrade":                 s.Upgrade == nil,
		"ValidateWhileConverging": s.ValidateWhileConverging == nil,
		"ValidateWhenDead":        s.ValidateWhenDead == nil,
	} {
		if isNil {
			add("VerbSeams.%s is nil — every seam is required. A harness that skips the property it was asked to check reports coverage that does not exist.", name)
		}
	}
	if len(problems) > 0 {
		sortStrings(problems)
		return problems
	}

	// subscribe is idempotent
	first, err := s.Subscribe()
	switch {
	case err != nil:
		add("first subscribe failed: %v", err)
	default:
		second, err := s.Subscribe()
		if err != nil {
			add("RETRIED subscribe failed: %v — subscribe is retried by the engine, so this is a normal condition, not an edge case", err)
		} else if second != first {
			add("a retried subscribe changed the resource count %d -> %d — it must CONVERGE, not duplicate (T-501/T-585)", first, second)
		}
	}

	// unsubscribe tears down
	if _, err := s.Subscribe(); err != nil {
		add("subscribe before teardown failed: %v", err)
	} else if remaining, err := s.Unsubscribe(); err != nil {
		add("unsubscribe failed: %v", err)
	} else if remaining != 0 {
		add("%d resource(s) survived unsubscribe — the consumer is still paying for them (MIT-1124)", remaining)
	}

	// absence is success
	if err := s.UnsubscribeWhenAbsent(); err != nil {
		add("unsubscribe errored on a resource that was never there: %v — absence is SUCCESS; erroring turns teardown into an endless retry instead of a finish (MIT-1302/MIT-1278)", err)
	}

	// upgrade is convergent
	if _, err := s.Subscribe(); err != nil {
		add("subscribe before upgrade failed: %v", err)
	} else if preserved, err := s.Upgrade(); err != nil {
		add("upgrade failed: %v", err)
	} else if !preserved {
		add("upgrade did not preserve pre-existing state — a destructive upgrade is worse than a failed one")
	}

	// validate distinguishes converging from dead
	if pending, isErr := s.ValidateWhileConverging(); !pending {
		add("a resource still coming up was not reported PENDING")
	} else if isErr {
		add("a resource still coming up was reported as an ERROR — a block merely waiting is never marked failed and never consumes a retry (MIT-1293)")
	}
	if deadPending, _ := s.ValidateWhenDead(); deadPending {
		add("a resource that will not recover was reported PENDING — it would wait forever instead of failing (MIT-1291)")
	}

	return problems
}

// AssertVerbSemantics runs the four properties and fails t with one error per
// violation.
func AssertVerbSemantics(t *testing.T, s VerbSeams) {
	t.Helper()
	for _, p := range CheckVerbSemantics(s) {
		t.Error(p)
	}
}

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }
func sortStrings(s []string)                    { sort.Strings(s) }

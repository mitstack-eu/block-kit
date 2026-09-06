package teardown

// verify.go — did the teardown we just reported actually happen?
//
// A `Check` asks one provider-specific question — "is this Kind still
// there?" — and this file holds the single ambiguity rule every operator
// answers it with, so an operator's own hand-derivation of the rule cannot
// diverge from the way the sibling packages in this kit already warn against.
//
// THE RULE: only a CONFIDENT POSITIVE may turn a reported success into a
// failure. Present returning (true, nil) is the one case that counts. Every
// other outcome — (false, nil), any non-nil error regardless of the bool,
// and a nil Present func — is either confirmed-gone or "could not ask", and
// an error is not evidence of either survival or absence. Treating "could
// not ask" as "it survived" invents failures nobody can act on; treating it
// as "it is gone" rebuilds the exact silence this rule exists to end.
//
// Survivor and Verify are split because most operators never call
// events.EmitFailed at all: their teardown success is the bare 200 response
// and their failure a 422 from writeError, so they need the ambiguity rule
// alone. Absent answers a different question — the `validate` verb's
// `absent` marker — and is deliberately NOT Survivor's negation; see its own
// comment for why.

import (
	"context"
	"fmt"
	"log"

	"github.com/mitstack-eu/operator-kit/events"
)

// Check asks whether one provider resource of Kind is still present.
type Check struct {
	// Kind names what is being asked about ("instance", "volume", ...), used
	// only for logging and for the detail a survivor is reported with.
	Kind string
	// Present answers the question. A nil error with true means confirmed
	// present; a nil error with false means confirmed gone. Any non-nil
	// error means the question could not be answered at all, whatever the
	// bool says alongside it.
	Present func(ctx context.Context) (bool, error)
}

// Options carries the side effects Survivor, Verify and Absent perform, each
// defaulted when left nil so a zero Options{} is always safe to pass.
type Options struct {
	// Logf receives every line this package would otherwise print, so a
	// caller can capture or discard them. Defaults to log.Printf.
	Logf func(format string, args ...any)
	// Scrub runs over every string before it is logged or emitted. Defaults
	// to the identity function.
	Scrub func(string) string
	// Emit reports a confirmed survivor. Defaults to events.EmitFailed,
	// which is a no-op when ctx carries no events envelope.
	Emit func(ctx context.Context, detail string) error
	// Detail turns a survivor's Kind into the string Emit carries. Defaults
	// to a plain sentence naming the kind.
	Detail func(kind string) string
}

// resolved fills every nil field of opts with its default, so the rest of
// this file can call each field directly without a nil check.
func resolved(opts Options) Options {
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	if opts.Scrub == nil {
		opts.Scrub = func(s string) string { return s }
	}
	if opts.Emit == nil {
		opts.Emit = events.EmitFailed
	}
	if opts.Detail == nil {
		opts.Detail = func(kind string) string {
			return fmt.Sprintf("teardown reported success but %s is still present", kind)
		}
	}
	return opts
}

// Survivor is the ambiguity rule alone: it asks each Check in order and
// returns the Kind of the first CONFIDENT POSITIVE — Present returning
// (true, nil) — or "" if none is found. It reports nothing and writes
// nothing but a log line per check, purely for operator diagnostics.
//
// Zero checks is a programming error, not a clean teardown, so it is logged
// rather than allowed to fall through silently.
func Survivor(ctx context.Context, opts Options, checks ...Check) string {
	opts = resolved(opts)

	if len(checks) == 0 {
		opts.Logf("teardown: Survivor called with zero checks — nothing was asked, this reports nothing")
		return ""
	}

	for _, c := range checks {
		if c.Present == nil {
			opts.Logf("teardown: %s: no Present func — could not ask, not treated as gone or as a survivor", opts.Scrub(c.Kind))
			continue
		}

		present, err := c.Present(ctx)
		if err != nil {
			opts.Logf("teardown: %s: could not confirm (error is not evidence): %s", opts.Scrub(c.Kind), opts.Scrub(err.Error()))
			continue
		}
		if present {
			return c.Kind
		}
	}

	return ""
}

// Verify calls Survivor and, when it finds one, reports the survivor through
// opts.Emit — logging rather than dropping a failure to deliver that report.
// It returns whether a survivor was found, independent of whether Emit
// succeeded: a survivor that could not be reported is still a survivor.
func Verify(ctx context.Context, opts Options, checks ...Check) bool {
	opts = resolved(opts)

	kind := Survivor(ctx, opts, checks...)
	if kind == "" {
		return false
	}

	detail := opts.Scrub(opts.Detail(kind))
	opts.Logf("teardown: survivor confirmed: %s", detail)

	if err := opts.Emit(ctx, detail); err != nil {
		opts.Logf("teardown: failed to report survivor %s: %s", opts.Scrub(kind), opts.Scrub(err.Error()))
	}

	return true
}

// Absent answers the `validate` verb's `absent` marker, and is NOT the
// negation of Survivor — a reader will assume it is, so this says otherwise
// explicitly. Survivor returns "" both when every resource is confidently
// gone AND when nothing could be asked, because it only needs to name a
// survivor when there is one. Absent needs the stronger claim behind that
// marker: every check was asked successfully, and every one of them
// answered gone. Any error, any nil Present, or zero checks defeats
// absence, because none of those is evidence that the resource is gone —
// only that we could not, or did not, look.
func Absent(ctx context.Context, opts Options, checks ...Check) bool {
	opts = resolved(opts)

	if len(checks) == 0 {
		opts.Logf("teardown: Absent called with zero checks — no evidence to assert absence from")
		return false
	}

	allGone := true

	for _, c := range checks {
		if c.Present == nil {
			opts.Logf("teardown: %s: no Present func — could not ask, not treated as absent", opts.Scrub(c.Kind))
			allGone = false
			continue
		}

		present, err := c.Present(ctx)
		if err != nil {
			opts.Logf("teardown: %s: could not confirm (error is not evidence): %s", opts.Scrub(c.Kind), opts.Scrub(err.Error()))
			allGone = false
			continue
		}
		if present {
			allGone = false
		}
	}

	return allGone
}

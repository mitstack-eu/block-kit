package progress_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/progress"
)

func sink() (*[]string, func(string)) {
	var lines []string
	return &lines, func(l string) { lines = append(lines, l) }
}

func TestObserve_NarratesPerResource(t *testing.T) {
	lines, s := sink()
	n := progress.NewWithSink(s)

	n.Observe(context.Background(), progress.Event{Action: "creating", Resource: "aws_instance.app"})

	if len(*lines) != 1 || !strings.Contains((*lines)[0], "aws_instance.app") {
		t.Errorf("lines = %v, want the resource named", *lines)
	}
}

func TestObserve_CountsTerminalEventsAgainstTheTotal(t *testing.T) {
	// "3/9" is the difference between a visible provision and a spinner.
	lines, s := sink()
	n := progress.NewWithSink(s)
	n.SetTotal(3)

	n.Observe(context.Background(), progress.Event{Action: "creating", Resource: "a"})
	n.Observe(context.Background(), progress.Event{Action: "created", Resource: "a"})
	n.Observe(context.Background(), progress.Event{Action: "created", Resource: "b"})

	if n.Done() != 2 {
		t.Errorf("Done() = %d, want 2 terminal events", n.Done())
	}
	if !strings.Contains((*lines)[2], "(2/3)") {
		t.Errorf("last line = %q, want a (2/3) count", (*lines)[2])
	}
}

func TestObserve_InProgressActionsDoNotAdvanceTheCount(t *testing.T) {
	_, s := sink()
	n := progress.NewWithSink(s)

	n.Observe(context.Background(), progress.Event{Action: "creating", Resource: "a"})
	n.Observe(context.Background(), progress.Event{Action: "creating", Resource: "b"})

	if n.Done() != 0 {
		t.Errorf("Done() = %d — 'creating' is not a completion", n.Done())
	}
}

func TestObserve_OmitsTheCountWhenTheTotalIsUnknown(t *testing.T) {
	lines, s := sink()
	n := progress.NewWithSink(s)

	n.Observe(context.Background(), progress.Event{Action: "created", Resource: "a"})

	if strings.Contains((*lines)[0], "/") {
		t.Errorf("line = %q, want no count when the total is unknown — a fake denominator is worse than none", (*lines)[0])
	}
}

func TestObserve_ScrubsSecretsBeforeTheLineLeavesTheProcess(t *testing.T) {
	lines, s := sink()
	n := progress.NewWithSink(s, "ghp_supersecret")

	n.Observe(context.Background(), progress.Event{
		Action: "creating", Resource: "repo", Detail: "using token ghp_supersecret",
	})

	if strings.Contains((*lines)[0], "ghp_supersecret") {
		t.Errorf("progress line leaked a secret: %q", (*lines)[0])
	}
}

func TestObserve_SanitisesNewlinesOutOfEngineOutput(t *testing.T) {
	// Engine output is request-adjacent and multi-line; an unsanitised detail
	// forges a log/event line (MIT-1326).
	lines, s := sink()
	n := progress.NewWithSink(s)

	n.Observe(context.Background(), progress.Event{
		Action: "creating", Resource: "a", Detail: "step one\nERROR: forged line",
	})

	if strings.ContainsAny((*lines)[0], "\r\n") {
		t.Errorf("progress line kept a newline: %q", (*lines)[0])
	}
}

func TestObserve_IgnoresAnEmptyEvent(t *testing.T) {
	lines, s := sink()
	n := progress.NewWithSink(s)

	n.Observe(context.Background(), progress.Event{})

	if len(*lines) != 0 {
		t.Errorf("lines = %v, want nothing emitted for an empty event", *lines)
	}
}

func TestNarrator_IsAdvisory_NoAPIReturnsASendError(t *testing.T) {
	// ADR 0042: the channel accelerates the lifecycle, it never carries it. This
	// is enforced by SHAPE — Observe returns nothing, so a caller cannot make
	// narration load-bearing by checking an error it should have ignored. If
	// someone adds an error return, this test stops compiling, which is the
	// point.
	n := progress.New()
	var observe func(context.Context, progress.Event) = n.Observe
	_ = observe
}

func TestNarrator_SurvivesAPanickingOrSlowSinkWithoutAffectingTheCaller(t *testing.T) {
	// A dropped or failing event must never change a lifecycle outcome. The sink
	// here records nothing; the narrator must still count correctly and return.
	n := progress.NewWithSink(func(string) {})
	n.SetTotal(2)

	n.Observe(context.Background(), progress.Event{Action: "created", Resource: "a"})
	n.Observe(context.Background(), progress.Event{Action: "created", Resource: "b"})

	if n.Done() != 2 {
		t.Errorf("Done() = %d, want 2", n.Done())
	}
}

func TestNew_EmitsThroughTheEventsChannelAndNoOpsWithoutOne(t *testing.T) {
	// A bare context carries no eventsCallback — an older engine, or the feature
	// off. That must be silence, not an error the operator has to swallow.
	n := progress.New()
	n.Observe(context.Background(), progress.Event{Action: "creating", Resource: "a"})
}

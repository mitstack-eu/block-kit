// Package progress narrates a long provision (MIT-1471).
//
// A five-minute provision with no output is indistinguishable from a hung one.
// MIT-1153/MIT-1273 gave aws-app-infra per-resource narration on the ADR 0042
// advisory channel — the difference between a spinner and a visible provision —
// and nothing else has it, because there was no way to share it.
//
// ADVISORY BY CONSTRUCTION. ADR 0042's rule is that the event channel
// accelerates the lifecycle and never carries it: a dropped, slow or failing
// event must never change a lifecycle outcome. So every method here swallows
// send failures, and there is deliberately no API that returns one — a caller
// cannot accidentally make narration load-bearing by checking an error it should
// have ignored.
package progress

import (
	"context"
	"fmt"
	"sync"

	"github.com/mitstack-eu/operator-kit/events"
	"github.com/mitstack-eu/operator-kit/safe"
)

// Event is one thing that happened to one resource.
type Event struct {
	// Action is "creating", "created", "destroying", "destroyed", ...
	Action string
	// Resource is the engine's address for it ("aws_instance.app").
	Resource string
	// Detail is optional extra context.
	Detail string
}

// Narrator emits per-resource progress and keeps a running count so a caller can
// say "3 of 9" without tracking it themselves.
type Narrator struct {
	mu       sync.Mutex
	emit     func(ctx context.Context, detail string)
	secrets  []string
	done     int
	total    int
	lastSent string
}

// New returns a Narrator that pushes to the ADR 0042 channel carried on ctx.
// secrets are scrubbed from every line before it leaves the process.
func New(secrets ...string) *Narrator {
	return &Narrator{
		emit: func(ctx context.Context, detail string) {
			// Advisory: the error is deliberately dropped. See the package doc.
			_ = events.EmitProgress(ctx, detail)
		},
		secrets: secrets,
	}
}

// NewWithSink returns a Narrator that pushes to an arbitrary sink — used by
// tests and by operators that also mirror progress into their own log.
func NewWithSink(sink func(detail string), secrets ...string) *Narrator {
	return &Narrator{
		emit:    func(_ context.Context, detail string) { sink(detail) },
		secrets: secrets,
	}
}

// SetTotal declares how many resources are expected, so lines can carry "n/m".
// Unknown totals are fine — omit it and the count is left off.
func (n *Narrator) SetTotal(total int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.total = total
}

// Observe narrates one event. It never returns an error, because a caller that
// could check one would eventually make narration load-bearing.
func (n *Narrator) Observe(ctx context.Context, ev Event) {
	line := n.render(ev)
	if line == "" {
		return
	}
	n.emit(ctx, line)
}

// render builds the line and applies the two independent safety steps: secrets
// are scrubbed, then the result is sanitised for a log/event sink. They compose
// and are never substituted for one another (MIT-1326).
func (n *Narrator) render(ev Event) string {
	if ev.Action == "" && ev.Resource == "" {
		return ""
	}

	n.mu.Lock()
	if isTerminal(ev.Action) {
		n.done++
	}
	suffix := ""
	if n.total > 0 {
		suffix = fmt.Sprintf(" (%d/%d)", n.done, n.total)
	}
	n.mu.Unlock()

	line := ev.Action
	if ev.Resource != "" {
		line += " " + ev.Resource
	}
	if ev.Detail != "" {
		line += ": " + ev.Detail
	}
	line += suffix

	return safe.LogSafe(line, n.secrets...)
}

func isTerminal(action string) bool {
	switch action {
	case "created", "destroyed", "modified", "imported":
		return true
	}
	return false
}

// Done reports how many terminal events have been observed.
func (n *Narrator) Done() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.done
}

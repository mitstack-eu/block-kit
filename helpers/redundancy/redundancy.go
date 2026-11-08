// Package redundancy guards a step that removes or replaces one member of a redundant set, such as a gateway, an etcd member or a database instance.
package redundancy

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mitstack-eu/block-kit/handler"
)

// ClassBelowMinimum is the refusal class of a removal that would leave fewer members serving than the set's minimum.
const ClassBelowMinimum = "redundancy-below-minimum"

// Reading is what one read of a member found, and when that read was taken.
type Reading struct {
	At      time.Time
	Serving bool
}

// Member is one member of a set with its latest reading, nil when nothing has read it.
type Member struct {
	Name    string
	Reading *Reading
}

type rule int

const (
	undeclared rule = iota
	majority
	allButOne
	noRedundancy
)

// Minimum is how many members a set keeps serving; its zero value declares none, which the guard rejects.
type Minimum struct {
	rule rule
}

// Majority keeps more than half of the set serving, the quorum etcd and OpenBao need.
func Majority() Minimum { return Minimum{rule: majority} }

// AllButOne keeps every member but the one removed serving, and at least one, as gateways, database instances and workers need.
func AllButOne() Minimum { return Minimum{rule: allButOne} }

// NoRedundancy declares a set of one member that may go without a replacement serving.
func NoRedundancy() Minimum { return Minimum{rule: noRedundancy} }

func (m Minimum) of(size int) (int, string) {
	switch m.rule {
	case majority:
		return size/2 + 1, fmt.Sprintf("a majority of %d", size)
	case allButOne:
		return max(size-1, 1), "all but the one removed"
	default:
		return 0, "no redundancy"
	}
}

// Set is a redundant set: the Name a refusal calls its members, such as "etcd members", each member's latest reading, and the minimum it keeps serving.
type Set struct {
	Name    string
	Members []Member
	Minimum Minimum
}

// AllowRemoval returns nil when the members left after removing member keep the minimum serving by readings taken at or after stepStart, a *handler.Refused of class ClassBelowMinimum naming each one's reading when they do not, and a plain error for a call it cannot judge.
func (s Set) AllowRemoval(member string, stepStart time.Time) error {
	if err := s.judgeable(member, stepStart); err != nil {
		return fmt.Errorf("redundancy: removing %q from the %s: %w", member, s.Name, err)
	}
	minimum, basis := s.Minimum.of(len(s.Members))
	remaining := s.without(member)
	left := 0
	for _, m := range remaining {
		if m.servesSince(stepStart) {
			left++
		}
	}
	if left >= minimum {
		return nil
	}
	return handler.Refuse(ClassBelowMinimum, "removing %q from the %s would leave %d serving, below their minimum of %d (%s), judged by readings since %s: %s",
		member, s.Name, left, minimum, basis, stamp(stepStart), describe(remaining, stepStart))
}

func (s Set) judgeable(member string, stepStart time.Time) error {
	if s.Minimum.rule == undeclared {
		return errors.New("the set declares no minimum")
	}
	if s.Minimum.rule == noRedundancy && len(s.Members) > 1 {
		return fmt.Errorf("a set of %d members declares no redundancy, which only a set of one may", len(s.Members))
	}
	if stepStart.IsZero() {
		return errors.New("the step names no start, so no reading can be judged fresh")
	}
	seen := make(map[string]bool, len(s.Members))
	for _, m := range s.Members {
		if seen[m.Name] {
			return fmt.Errorf("the set names %q twice", m.Name)
		}
		seen[m.Name] = true
	}
	if !seen[member] {
		return errors.New("the set holds no such member")
	}
	return nil
}

func (s Set) without(member string) []Member {
	out := make([]Member, 0, len(s.Members))
	for _, m := range s.Members {
		if m.Name != member {
			out = append(out, m)
		}
	}
	return out
}

func (m Member) servesSince(stepStart time.Time) bool {
	return m.Reading != nil && m.Reading.Serving && !m.Reading.At.Before(stepStart)
}

func describe(members []Member, stepStart time.Time) string {
	if len(members) == 0 {
		return "no other member"
	}
	parts := make([]string, 0, len(members))
	for _, m := range members {
		parts = append(parts, m.describe(stepStart))
	}
	return strings.Join(parts, "; ")
}

func (m Member) describe(stepStart time.Time) string {
	switch {
	case m.Reading == nil:
		return fmt.Sprintf("%q has no reading", m.Name)
	case m.Reading.At.Before(stepStart):
		return fmt.Sprintf("%q read at %s, before the step began", m.Name, stamp(m.Reading.At))
	case m.Reading.Serving:
		return fmt.Sprintf("%q read serving at %s", m.Name, stamp(m.Reading.At))
	default:
		return fmt.Sprintf("%q read not serving at %s", m.Name, stamp(m.Reading.At))
	}
}

func stamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

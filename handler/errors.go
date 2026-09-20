package handler

import (
	"fmt"
	"strings"
)

// refusedPrefix marks a statusDetail as carrying a machine-readable refusal
// class, in the shape "refused:<class>: <message>".
const refusedPrefix = "refused:"

// unclassifiedRefusal is the class a *Refused with an empty Class renders
// as, so a wire-level statusDetail never carries an empty class segment.
const unclassifiedRefusal = "unclassified"

// Refused is the error a block returns for a request it will not carry out
// — a teardown blocked by a live dependent, a quota it will not exceed, a
// policy it will not violate. Class is machine-readable and travels on the
// wire in statusDetail, so the engine can read a refusal rather than
// inferring one from a change it did not ask for (MIT-2659).
type Refused struct {
	// Class names the kind of refusal, e.g. "dependents-remain". Read back
	// out of a response's statusDetail with RefusedClass. An empty Class
	// renders as "unclassified".
	Class string
	// Detail is the human-readable reason.
	Detail string
}

// Error renders as "refused:<class>: <detail>", the exact statusDetail shape
// the Handler puts on the wire for a *Refused error.
func (e *Refused) Error() string {
	class := e.Class
	if class == "" {
		class = unclassifiedRefusal
	}
	return refusedPrefix + class + ": " + e.Detail
}

// Refuse builds a *Refused whose Detail is formatted from format and args,
// the way fmt.Errorf builds a plain error.
func Refuse(class, format string, args ...any) error {
	return &Refused{Class: class, Detail: fmt.Sprintf(format, args...)}
}

// RefusedClass reads the class back out of a response's decoded
// statusDetail. It returns "" when detail does not carry the "refused:"
// prefix a *Refused's Error renders.
func RefusedClass(detail string) string {
	if !strings.HasPrefix(detail, refusedPrefix) {
		return ""
	}
	rest := detail[len(refusedPrefix):]
	idx := strings.Index(rest, ":")
	if idx < 0 {
		return ""
	}
	return rest[:idx]
}

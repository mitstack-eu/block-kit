package hostexec

// Opt-in send options for the ssm driver. Each option only records a value;
// resolveSendOptions validates the full set before anything is spawned.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Bounds the ssm driver enforces on its send options, matching the limits
// SendCommand itself accepts.
const (
	maxCommentBytes     = 100
	minDeliveryTimeout  = 30 * time.Second
	maxDeliveryTimeout  = 2592000 * time.Second
	minExecutionTimeout = 1 * time.Second
	maxExecutionTimeout = 172800 * time.Second
)

// SendOption adjusts how the ssm driver sends and waits for a command. Every
// option is ssm-only: Run and RunOutput reject any option on the ssh driver
// rather than silently ignoring it. When the same option is passed more than
// once, the last one wins.
type SendOption func(*sendOptions)

// sendOptions is the resolved set of send options. A zero value sends exactly
// the argv, and polls exactly as, a call with no option does.
type sendOptions struct {
	comment             string
	commentSet          bool
	deliveryTimeout     time.Duration
	deliveryTimeoutSet  bool
	executionTimeout    time.Duration
	executionTimeoutSet bool
	detachedPoll        bool
}

// WithComment sends comment as the command's --comment, so the command can be
// found by it later. The comment must be non-empty and at most 100 bytes; it
// is never truncated. It must not begin with "-", which the aws CLI would
// parse as an option, nor with file://, fileb://, http:// or https://, which
// the aws CLI can read as an instruction to load the value from that file or
// URL; the prefix check ignores letter case. Those strings later in the
// comment are sent verbatim.
func WithComment(comment string) SendOption {
	return func(o *sendOptions) {
		o.comment = comment
		o.commentSet = true
	}
}

// WithDeliveryTimeout sends d as the command's --timeout-seconds, how long
// SSM keeps trying to deliver the command to the instance. d must be a whole
// number of seconds between 30 s and 2592000 s inclusive. It also lengthens
// the poll bound, which becomes d plus any execution timeout plus one
// SSMPollInterval whenever that exceeds SSMPollTimeout.
func WithDeliveryTimeout(d time.Duration) SendOption {
	return func(o *sendOptions) {
		o.deliveryTimeout = d
		o.deliveryTimeoutSet = true
	}
}

// WithExecutionTimeout sends d as the AWS-RunShellScript executionTimeout
// parameter, how long the script may run on the instance. d must be a whole
// number of seconds between 1 s and 172800 s inclusive. It also lengthens the
// poll bound, which becomes d plus any delivery timeout plus one
// SSMPollInterval whenever that exceeds SSMPollTimeout.
func WithExecutionTimeout(d time.Duration) SendOption {
	return func(o *sendOptions) {
		o.executionTimeout = d
		o.executionTimeoutSet = true
	}
}

// WithDetachedPoll keeps waiting for the sent command after the caller's
// context is cancelled. send-command still runs on the caller's context, but
// the status poll and, for RunOutput, the output read run on a context
// detached from its cancellation and bounded by the poll bound: SSMPollTimeout,
// or with a delivery or execution timeout set, their sum plus one
// SSMPollInterval if that is longer. The call then returns only once the
// command reaches a terminal state or that bound runs out, so it can outlive a
// cancellation by up to that bound.
func WithDetachedPoll() SendOption {
	return func(o *sendOptions) {
		o.detachedPoll = true
	}
}

// refuseSendOptions rejects any option at all, nil included, for a driver
// that has nothing to apply it to, rather than silently ignoring it.
func refuseSendOptions(kind Kind, opts []SendOption) error {
	if len(opts) == 0 {
		return nil
	}
	return fmt.Errorf("hostexec: send options apply only to the %q driver, not %q", KindSSM, kind)
}

// resolveSendOptions applies opts in order, so a repeated option's last value
// wins, and validates the result for the ssm driver. It rejects a nil option
// and any value outside the bounds its With function documents.
func resolveSendOptions(opts []SendOption) (sendOptions, error) {
	var o sendOptions
	for _, opt := range opts {
		if opt == nil {
			return sendOptions{}, errors.New("hostexec: nil SendOption")
		}
		opt(&o)
	}
	if o.commentSet {
		switch {
		case o.comment == "":
			return sendOptions{}, errors.New("hostexec: WithComment: comment is empty")
		case len(o.comment) > maxCommentBytes:
			return sendOptions{}, fmt.Errorf("hostexec: WithComment: comment is %d bytes, over SSM's %d-byte limit", len(o.comment), maxCommentBytes)
		case strings.HasPrefix(o.comment, "-"):
			return sendOptions{}, fmt.Errorf("hostexec: WithComment: comment %q begins with \"-\" — the aws CLI has no end-of-options marker, so it would be parsed as an option", o.comment)
		}
		if prefix, ok := remoteValuePrefix(o.comment); ok {
			return sendOptions{}, fmt.Errorf("hostexec: WithComment: comment %q begins with %q — the aws CLI can read it as an instruction to load the value from that file or URL", o.comment, prefix)
		}
	}
	if o.deliveryTimeoutSet {
		if err := checkWholeSeconds("WithDeliveryTimeout", o.deliveryTimeout, minDeliveryTimeout, maxDeliveryTimeout); err != nil {
			return sendOptions{}, err
		}
	}
	if o.executionTimeoutSet {
		if err := checkWholeSeconds("WithExecutionTimeout", o.executionTimeout, minExecutionTimeout, maxExecutionTimeout); err != nil {
			return sendOptions{}, err
		}
	}
	return o, nil
}

// remoteValuePrefixes are the parameter-value prefixes that make the aws CLI
// read the value from a local file or fetch it from a URL.
var remoteValuePrefixes = []string{"file://", "fileb://", "http://", "https://"}

// remoteValuePrefix reports which of remoteValuePrefixes s begins with,
// compared case-insensitively, so every letter-case variant is refused
// whether or not the aws CLI would honour it.
func remoteValuePrefix(s string) (string, bool) {
	for _, p := range remoteValuePrefixes {
		if len(s) >= len(p) && strings.EqualFold(s[:len(p)], p) {
			return p, true
		}
	}
	return "", false
}

// checkWholeSeconds rejects d unless it is a whole number of seconds within
// [lo, hi].
func checkWholeSeconds(name string, d, lo, hi time.Duration) error {
	if d%time.Second != 0 {
		return fmt.Errorf("hostexec: %s: %s is not a whole number of seconds", name, d)
	}
	if d < lo || d > hi {
		return fmt.Errorf("hostexec: %s: %s is outside %s..%s", name, d, lo, hi)
	}
	return nil
}

// seconds renders d, already validated as whole seconds, as the decimal
// integer the aws CLI expects.
func seconds(d time.Duration) string {
	return strconv.FormatInt(int64(d/time.Second), 10)
}

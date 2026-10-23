package hostexec

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// These bounds match the limits SendCommand itself accepts.
const (
	maxCommentBytes     = 100
	minDeliveryTimeout  = 30 * time.Second
	maxDeliveryTimeout  = 2592000 * time.Second
	minExecutionTimeout = 1 * time.Second
	maxExecutionTimeout = 172800 * time.Second
)

// SendOption adjusts how the ssm driver sends and waits for a command, is rejected on the ssh driver rather than ignored, and the last of a repeated option wins.
type SendOption func(*sendOptions)

type sendOptions struct {
	comment             string
	commentSet          bool
	deliveryTimeout     time.Duration
	deliveryTimeoutSet  bool
	executionTimeout    time.Duration
	executionTimeoutSet bool
	detachedPoll        bool
}

// WithComment sends comment as --comment, non-empty and at most 100 bytes and never truncated, and refuses a leading "-" or a case-insensitive file://, fileb://, http:// or https:// prefix, which the aws CLI would read as an option or an instruction to load the value.
func WithComment(comment string) SendOption {
	return func(o *sendOptions) {
		o.comment = comment
		o.commentSet = true
	}
}

// WithDeliveryTimeout sends d as --timeout-seconds, a whole number of seconds from 30 to 2592000, and lengthens the poll bound to d plus any execution timeout plus one SSMPollInterval when that exceeds SSMPollTimeout.
func WithDeliveryTimeout(d time.Duration) SendOption {
	return func(o *sendOptions) {
		o.deliveryTimeout = d
		o.deliveryTimeoutSet = true
	}
}

// WithExecutionTimeout sends d as the executionTimeout parameter, a whole number of seconds from 1 to 172800, and lengthens the poll bound to d plus any delivery timeout plus one SSMPollInterval when that exceeds SSMPollTimeout.
func WithExecutionTimeout(d time.Duration) SendOption {
	return func(o *sendOptions) {
		o.executionTimeout = d
		o.executionTimeoutSet = true
	}
}

// WithDetachedPoll keeps waiting after the caller's context is cancelled, because cancelling does not stop a command already running on the host, so the call can outlive a cancellation by up to the poll bound.
func WithDetachedPoll() SendOption {
	return func(o *sendOptions) {
		o.detachedPoll = true
	}
}

func refuseSendOptions(kind Kind, opts []SendOption) error {
	if len(opts) == 0 {
		return nil
	}
	return fmt.Errorf("hostexec: send options apply only to the %q driver, not %q", KindSSM, kind)
}

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

var remoteValuePrefixes = []string{"file://", "fileb://", "http://", "https://"}

// remoteValuePrefix compares case-insensitively so every letter-case variant is refused whether or not the aws CLI would honour it.
func remoteValuePrefix(s string) (string, bool) {
	for _, p := range remoteValuePrefixes {
		if len(s) >= len(p) && strings.EqualFold(s[:len(p)], p) {
			return p, true
		}
	}
	return "", false
}

func checkWholeSeconds(name string, d, lo, hi time.Duration) error {
	if d%time.Second != 0 {
		return fmt.Errorf("hostexec: %s: %s is not a whole number of seconds", name, d)
	}
	if d < lo || d > hi {
		return fmt.Errorf("hostexec: %s: %s is outside %s..%s", name, d, lo, hi)
	}
	return nil
}

func seconds(d time.Duration) string {
	return strconv.FormatInt(int64(d/time.Second), 10)
}

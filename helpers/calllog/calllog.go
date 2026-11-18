// Package calllog writes one log line for each outbound HTTP call a kit client makes, naming its subscription, method, host, route, answer and milliseconds, and never a query, header value or body.
package calllog

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mitstack-eu/block-kit/helpers/safe"
)

const (
	idSegment      = "{id}"
	noSubscription = "-"
)

var (
	wordSegment = regexp.MustCompile(`^(?:[a-z]+(?:[-_][a-z]+)*|v[0-9]+)$`)
	hostLabel   = regexp.MustCompile(`^(?:[a-z]+(?:-[a-z]+)*|[0-9]+)$`)
)

type subscriptionKey struct{}

// WithSubscription returns ctx carrying the subscription each call made under it names.
func WithSubscription(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, subscriptionKey{}, id)
}

func subscription(ctx context.Context) string {
	if id, _ := ctx.Value(subscriptionKey{}).(string); id != "" {
		return id
	}
	return noSubscription
}

type Line struct {
	Subscription, Method, Host, Route, Answer string
	Took                                      time.Duration
}

// Format renders the line with each field made safe to log and every secret scrubbed.
func (l Line) Format(secrets ...string) string {
	field := func(v string) string { return safe.LogSafe(v, secrets...) }
	return fmt.Sprintf("calllog: subscription=%s method=%s host=%s route=%s answer=%s ms=%d",
		field(l.Subscription), field(l.Method), field(l.Host), field(l.Route), field(l.Answer), l.Took.Milliseconds())
}

// Ended is the line a validate whose request ended writes, naming the resource it had reached.
type Ended struct {
	Subscription, Reached string
	Took                  time.Duration
}

// Format renders the ended line with each field made safe to log and every secret scrubbed.
func (e Ended) Format(secrets ...string) string {
	at := "before any resource"
	if e.Reached != "" {
		at = fmt.Sprintf("at resource %q", safe.LogSafe(e.Reached, secrets...))
	}
	return fmt.Sprintf("validate: subscription %s: the request ended after %s %s",
		safe.LogSafe(e.Subscription, secrets...), e.Took.Round(time.Millisecond), at)
}

// Option shapes how a wrapped transport names its calls.
type Option func(*transport)

// FixedSegments names every fixed segment of a client's routes, for an API whose identifiers read as words: any other segment then reads {id}.
func FixedSegments(words ...string) Option {
	return func(t *transport) { t.fixed = words }
}

type transport struct {
	next  http.RoundTripper
	fixed []string
}

// Transport wraps next, or when nil the http.DefaultTransport of each call's time, so each call it carries writes one line; a transport already wrapped comes back as it is.
func Transport(next http.RoundTripper, opts ...Option) http.RoundTripper {
	if wrapped, ok := next.(*transport); ok {
		return wrapped
	}
	t := &transport{next: next}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Client returns a copy of c, or of a zero client when c is nil, whose transport writes one line per call; a client already wrapped comes back as it is.
func Client(c *http.Client, opts ...Option) *http.Client {
	if c == nil {
		c = &http.Client{}
	}
	if _, ok := c.Transport.(*transport); ok {
		return c
	}
	wrapped := *c
	wrapped.Transport = Transport(c.Transport, opts...)
	return &wrapped
}

// RoundTrip carries the call and writes its line once the answer's status arrives or the call fails.
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}
	started := time.Now()
	resp, err := next.RoundTrip(req)
	line := Line{
		Subscription: subscription(req.Context()),
		Method:       req.Method,
		Host:         Host(req.URL.Host),
		Route:        Route(req.URL.EscapedPath(), t.fixed...),
		Answer:       answer(req.Context(), resp, err),
		Took:         time.Since(started),
	}
	log.Print(line.Format(headerSecrets(req.Header)...))
	return resp, err
}

func answer(ctx context.Context, resp *http.Response, err error) string {
	if err == nil && resp != nil {
		return strconv.Itoa(resp.StatusCode)
	}
	var netErr net.Error
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	}
	return "error"
}

// Route names path with each segment that is no word as {id}, or, when fixed is given, each segment not in fixed.
func Route(path string, fixed ...string) string {
	segments := strings.Split(path, "/")
	for i, s := range segments {
		if s == "" || keptSegment(s, fixed) {
			continue
		}
		segments[i] = idSegment
	}
	if route := strings.Join(segments, "/"); route != "" {
		return route
	}
	return "/"
}

func keptSegment(s string, fixed []string) bool {
	if len(fixed) > 0 {
		return slices.Contains(fixed, s)
	}
	return wordSegment.MatchString(s)
}

// Host names host with each label that is neither a word nor a number as {id}, keeping its port.
func Host(host string) string {
	name, port, err := net.SplitHostPort(host)
	if err != nil {
		name, port = host, ""
	}
	labels := strings.Split(name, ".")
	for i, l := range labels {
		if !hostLabel.MatchString(l) {
			labels[i] = idSegment
		}
	}
	if port == "" {
		return strings.Join(labels, ".")
	}
	return net.JoinHostPort(strings.Join(labels, "."), port)
}

// headerSecrets lists every header value and each of its space-separated parts, so a credential such as a bearer token is scrubbed wherever it reappears.
func headerSecrets(h http.Header) []string {
	var secrets []string
	for _, values := range h {
		for _, v := range values {
			secrets = append(secrets, v)
			secrets = append(secrets, strings.Fields(v)...)
		}
	}
	return secrets
}

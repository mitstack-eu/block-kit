package contract

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

var ErrReadOnly = errors.New("contract: the request is a validate that settles no verb, so it must write nothing")

type readOnlyKey struct{}

func WithReadOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, readOnlyKey{}, true)
}

// RefuseWrite answers an error wrapping ErrReadOnly for a method that changes state when ctx is marked read-only, and nil for every other call.
func RefuseWrite(ctx context.Context, method string) error {
	if marked, _ := ctx.Value(readOnlyKey{}).(bool); marked && method != http.MethodGet && method != http.MethodHead {
		return fmt.Errorf("refusing %s: %w", method, ErrReadOnly)
	}
	return nil
}

// ReadOnly is true on a validate that settles no verb, the one derivation behind Desired.ReadOnly and the context a block's kit clients are called with.
func (r Request) ReadOnly() bool { return r.Operation == "validate" && !r.Settling }

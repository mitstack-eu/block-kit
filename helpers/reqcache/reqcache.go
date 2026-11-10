// Package reqcache, an optional helper, remembers what one request reads from a source that cannot change under it, so a block's passes over the same data cost one read each.
package reqcache

import (
	"context"
	"sync"
)

type scopeKey struct{}

type snapshotKey struct{}

type scope struct {
	mu   sync.Mutex
	kept map[any]any
}

func With(ctx context.Context) context.Context {
	if _, ok := ctx.Value(scopeKey{}).(*scope); ok {
		return ctx
	}
	return context.WithValue(ctx, scopeKey{}, &scope{kept: map[any]any{}})
}

// Snapshot makes the rest of a call read data that changes over time once, as one view; a poll that waits for a change must not run under it.
func Snapshot(ctx context.Context) context.Context {
	return context.WithValue(With(ctx), snapshotKey{}, true)
}

// Immutable reads key once per request and remembers a success only; a ctx with no cache reads every time.
func Immutable[T any](ctx context.Context, key any, read func() (T, error)) (T, error) {
	return remember(ctx, key, false, read)
}

// Viewed is Immutable for data that changes over time, and remembers only under Snapshot.
func Viewed[T any](ctx context.Context, key any, read func() (T, error)) (T, error) {
	return remember(ctx, key, true, read)
}

func remember[T any](ctx context.Context, key any, changing bool, read func() (T, error)) (T, error) {
	s, ok := ctx.Value(scopeKey{}).(*scope)
	if snapshot, _ := ctx.Value(snapshotKey{}).(bool); !ok || (changing && !snapshot) {
		return read()
	}
	s.mu.Lock()
	kept, found := s.kept[key]
	s.mu.Unlock()
	if found {
		return kept.(T), nil
	}
	got, err := read()
	if err != nil {
		return got, err
	}
	s.mu.Lock()
	s.kept[key] = got
	s.mu.Unlock()
	return got, nil
}

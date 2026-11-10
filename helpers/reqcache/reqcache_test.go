package reqcache_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/reqcache"
)

func counted(calls *atomic.Int64) func() (int, error) {
	return func() (int, error) { return int(calls.Add(1)), nil }
}

func TestImmutable_ReadsOnceInARequestAndEveryTimeOutsideOne(t *testing.T) {
	var inRequest, bare atomic.Int64
	ctx := reqcache.With(context.Background())
	for range 3 {
		reqcache.Immutable(ctx, "k", counted(&inRequest))
		reqcache.Immutable(context.Background(), "k", counted(&bare))
	}
	if inRequest.Load() != 1 || bare.Load() != 3 {
		t.Errorf("three reads made %d reads in a request and %d outside one, want 1 and 3", inRequest.Load(), bare.Load())
	}
}

func TestImmutable_KeepsNoFailureAndKeepsEachKeyApart(t *testing.T) {
	ctx := reqcache.With(context.Background())
	failed := errors.New("unreachable")
	if _, err := reqcache.Immutable(ctx, "k", func() (int, error) { return 0, failed }); !errors.Is(err, failed) {
		t.Fatalf("first read error = %v, want %v", err, failed)
	}
	if got, err := reqcache.Immutable(ctx, "k", func() (int, error) { return 7, nil }); got != 7 || err != nil {
		t.Errorf("read after a failure = %d, %v, want 7 from a new read", got, err)
	}
	if got, _ := reqcache.Immutable(ctx, "other", func() (int, error) { return 8, nil }); got != 8 {
		t.Errorf("another key = %d, want its own read 8", got)
	}
}

func TestViewed_RemembersOnlyUnderASnapshot(t *testing.T) {
	var outside, inside atomic.Int64
	ctx := reqcache.With(context.Background())
	snapshot := reqcache.Snapshot(ctx)
	for range 3 {
		reqcache.Viewed(ctx, "k", counted(&outside))
		reqcache.Viewed(snapshot, "k", counted(&inside))
	}
	if outside.Load() != 3 || inside.Load() != 1 {
		t.Errorf("three reads made %d reads outside a snapshot and %d under one, want 3 and 1", outside.Load(), inside.Load())
	}
}

func TestWith_KeepsTheScopeTheContextAlreadyHolds(t *testing.T) {
	var calls atomic.Int64
	ctx := reqcache.With(context.Background())
	reqcache.Immutable(ctx, "k", counted(&calls))
	reqcache.Immutable(reqcache.With(ctx), "k", counted(&calls))
	if calls.Load() != 1 {
		t.Errorf("a read through With of a context already holding a scope made %d reads, want the first one's answer", calls.Load())
	}
}

func TestImmutable_IsSafeForConcurrentUseOfOneScope(t *testing.T) {
	ctx := reqcache.Snapshot(reqcache.With(context.Background()))
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reqcache.Immutable(ctx, i%4, func() (int, error) { return i, nil })
			reqcache.Viewed(ctx, i%4, func() (int, error) { return i, nil })
		}()
	}
	wg.Wait()
}

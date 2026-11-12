package kittest_test

// The replacement drill writes known data, snapshots, replaces the server, restores and observes, and fails a block that loses a datum or reads unhealthy after the restore.

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/helpers/age"
	"github.com/mitstack-eu/block-kit/helpers/continuity"
	"github.com/mitstack-eu/block-kit/verify/kittest"
)

const stateBlockManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: state-block
  name: State Block
spec:
  version: 1.0.0
  resources:
    - aws.ebs-volume
  continuity:
    - name: app-data
      kind: volume
      stateful: true
      handover: snapshot
    - name: scratch
      kind: volume
      stateful: false
      handover: none
  lifecycle:
    endpoint: https://state-block.example/hook
    operations: [subscribe, unsubscribe, upgrade, validate, snapshot, restore]
    contract: kit/v1
`

// listedVolumeFailure is a cause the aws.ebs-volume type lists under failures.
const listedVolumeFailure = "the volume is still creating, attaching or detaching"

type carrier interface {
	Snapshot(ctx context.Context, unit string) (string, error)
	Restore(ctx context.Context, unit, handover string) error
}

// stateBlock keeps its data in live and carries it across a replacement through carry.
type stateBlock struct {
	*fakeBlock
	carry  carrier
	mu     sync.Mutex
	marked map[string]bool
}

func (b *stateBlock) mark(op string, d contract.Desired) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.marked[op] = d.Replacing
}

func (b *stateBlock) Snapshot(ctx context.Context, d contract.Desired, unit string) (string, error) {
	b.mark("snapshot", d)
	return b.carry.Snapshot(ctx, unit)
}

func (b *stateBlock) Restore(ctx context.Context, d contract.Desired, unit, handover string) error {
	b.mark("restore", d)
	return b.carry.Restore(ctx, unit, handover)
}

// memState is the live data of a block and the drill's view of it.
type memState struct {
	mu   sync.Mutex
	data map[string]string
}

func (s *memState) Write(_ context.Context, data map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	maps.Copy(s.data, data)
	return nil
}

func (s *memState) Read(context.Context) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.data), nil
}

func (s *memState) Replace(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.data)
	return nil
}

// vault is an in-memory carrier that restores everything it snapshotted but the datum named in drop.
type vault struct {
	live     *memState
	drop     string
	restored *atomic.Bool
	mu       sync.Mutex
	taken    map[string]map[string]string
}

func (v *vault) Snapshot(ctx context.Context, _ string) (string, error) {
	data, _ := v.live.Read(ctx)
	v.mu.Lock()
	defer v.mu.Unlock()
	handover := fmt.Sprintf("hnd-%d", len(v.taken)+1)
	v.taken[handover] = data
	return handover, nil
}

func (v *vault) Restore(ctx context.Context, _, handover string) error {
	v.mu.Lock()
	data, ok := v.taken[handover]
	v.mu.Unlock()
	if !ok {
		return fmt.Errorf("no snapshot under the handover %q", handover)
	}
	data = maps.Clone(data)
	delete(data, v.drop)
	v.restored.Store(true)
	return v.live.Write(ctx, data)
}

// unhealthyAfterRestore reads present but unhealthy once the restore has run.
type unhealthyAfterRestore struct {
	inner    handler.Observer
	restored *atomic.Bool
}

func (o unhealthyAfterRestore) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	obs, err := o.inner.ObserveResource(ctx, r)
	if err == nil && obs.State == contract.Present && o.restored.Load() {
		obs.Unhealthy = &contract.Unhealthy{Cause: listedVolumeFailure, Detail: "the restored volume reports no attachment"}
	}
	return obs, err
}

func newStateBlock(t *testing.T, live *memState, carry func(restored *atomic.Bool) carrier, unhealthy bool) (*stateBlock, kittest.Replacement) {
	t.Helper()
	p := newFakeProvider()
	restored := &atomic.Bool{}
	b := &stateBlock{
		fakeBlock: &fakeBlock{p: p, resources: map[string]string{"aws.ebs-volume": "aws.ebs-volume"}},
		carry:     carry(restored), marked: map[string]bool{},
	}
	var observer handler.Observer = providerObserver{p: p}
	if unhealthy {
		observer = unhealthyAfterRestore{inner: observer, restored: restored}
	}
	return b, kittest.Replacement{
		Block: kittest.Block{
			Config: handler.Config{
				Resource: b, Manifest: []byte(stateBlockManifest), Types: typeSnapshot(t),
				Observers: map[string]handler.Observer{"aws.ebs-volume": observer},
			},
			Provider: p,
			Desired:  blockDesired("10"),
		},
		Unit: "app-data",
		Data: live,
	}
}

func memoryBlock(t *testing.T, drop string, unhealthy bool) (*stateBlock, kittest.Replacement) {
	t.Helper()
	live := &memState{data: map[string]string{}}
	return newStateBlock(t, live, func(restored *atomic.Bool) carrier {
		return &vault{live: live, drop: drop, restored: restored, taken: map[string]map[string]string{}}
	}, unhealthy)
}

func TestCheckReplacement_FailsABlockWhoseRestoreDropsADatum(t *testing.T) {
	_, in := memoryBlock(t, "beta", false)

	problems := kittest.CheckReplacement(in)

	wantProblemNaming(t, problems, "replacement lost datum", `"beta"`)
	if len(problems) != 1 {
		t.Errorf("CheckReplacement problems %q, want only the one datum the restore dropped", problems)
	}
}

func TestCheckReplacement_FailsABlockWhoseObserveReadsUnhealthyAfterTheRestore(t *testing.T) {
	_, in := memoryBlock(t, "", true)

	problems := kittest.CheckReplacement(in)

	wantProblemNaming(t, problems, `resource "aws.ebs-volume"`, "reads unhealthy after the restore", listedVolumeFailure)
}

func TestCheckReplacement_PassesABlockThatKeepsEveryDatumAndReadsHealthy(t *testing.T) {
	_, in := memoryBlock(t, "", false)

	wantNoProblems(t, kittest.CheckReplacement(in))
}

func TestCheckReplacement_MarksTheSnapshotTheUpgradeAndTheRestoreAsPartOfAReplacement(t *testing.T) {
	b, in := memoryBlock(t, "", false)

	kittest.CheckReplacement(in)

	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.marked["snapshot"] || !b.marked["restore"] {
		t.Errorf("the block saw replacing = %v for snapshot and restore, want true for both", b.marked)
	}
	b.fakeBlock.mu.Lock()
	defer b.fakeBlock.mu.Unlock()
	if last := b.ensured[len(b.ensured)-1]; !last.Replacing {
		t.Errorf("the ensure that recreated the server saw replacing = false, want true so the block sends no install retry")
	}
}

func TestCheckReplacement_NamesAUnitTheManifestDoesNotHandOverBySnapshot(t *testing.T) {
	for _, unit := range []string{"scratch", "missing"} {
		t.Run(unit, func(t *testing.T) {
			_, in := memoryBlock(t, "", false)
			in.Unit = unit

			wantProblemNaming(t, kittest.CheckReplacement(in), fmt.Sprintf("%q", unit), "handover snapshot")
		})
	}
}

// objectStore keeps the objects a hook uploads behind plain links, as a bucket does behind presigned ones.
type objectStore struct {
	srv     *httptest.Server
	mu      sync.Mutex
	objects map[string][]byte
}

func newObjectStore(t *testing.T) *objectStore {
	t.Helper()
	s := &objectStore{objects: map[string][]byte{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.Method == http.MethodPut {
			s.objects[r.URL.Path], _ = io.ReadAll(r.Body)
			return
		}
		_, _ = w.Write(s.objects[r.URL.Path])
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *objectStore) Upload(_ context.Context, unit string) (continuity.Link, error) {
	return continuity.Link{URL: s.srv.URL + "/" + unit + "/snapshot", Ref: unit + "/snapshot"}, nil
}

func (s *objectStore) Download(_ context.Context, _, ref string) (continuity.Link, error) {
	return continuity.Link{URL: s.srv.URL + "/" + ref}, nil
}

type ageKeys struct{ id *age.Identity }

func (k ageKeys) Recipient(context.Context, string) (string, error) {
	return k.id.Recipient().String(), nil
}
func (k ageKeys) Identity(context.Context, string) (string, error) { return k.id.String(), nil }

// dirState is the drill's view of a volume directory.
type dirState struct{ dir string }

func (s dirState) Write(_ context.Context, data map[string]string) error {
	for name, value := range data {
		if err := os.WriteFile(filepath.Join(s.dir, name), []byte(value), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (s dirState) Read(context.Context) (map[string]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out[e.Name()] = string(raw)
	}
	return out, nil
}

func (s dirState) Replace(context.Context) error {
	if err := os.RemoveAll(s.dir); err != nil {
		return err
	}
	return os.Mkdir(s.dir, 0o700)
}

func TestCheckReplacement_PassesABlockBuiltOnTheSharedHooks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "volume")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	id, err := age.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	hook := continuity.Hook{Links: newObjectStore(t), Keys: ageKeys{id}, State: continuity.Volume{Dir: dir}}
	_, in := newStateBlock(t, nil, func(*atomic.Bool) carrier { return hook }, false)
	in.Data = dirState{dir}

	wantNoProblems(t, kittest.CheckReplacement(in))
}

// stubbornState is a memState whose Replace leaves the data in place.
type stubbornState struct{ *memState }

func (stubbornState) Replace(context.Context) error { return nil }

func TestCheckReplacement_FailsADrillWhoseReplaceLeavesTheDataInPlace(t *testing.T) {
	live := &memState{data: map[string]string{}}
	_, in := newStateBlock(t, live, func(restored *atomic.Bool) carrier {
		return &vault{live: live, restored: restored, taken: map[string]map[string]string{}}
	}, false)
	in.Data = stubbornState{live}

	problems := kittest.CheckReplacement(in)

	for _, datum := range []string{`"alpha"`, `"beta"`, `"gamma"`} {
		wantProblemNaming(t, problems, "Data.Replace left datum", datum)
	}
}

// keptState keeps the map the drill hands it as its live data, as an in-memory block may.
type keptState struct{ data map[string]string }

func (s *keptState) Write(_ context.Context, data map[string]string) error { s.data = data; return nil }
func (s *keptState) Read(context.Context) (map[string]string, error)       { return maps.Clone(s.data), nil }
func (s *keptState) Replace(context.Context) error                         { clear(s.data); return nil }

// forgetful is a carrier whose restore brings nothing back.
type forgetful struct{}

func (forgetful) Snapshot(context.Context, string) (string, error) { return "hnd-1", nil }
func (forgetful) Restore(context.Context, string, string) error    { return nil }

func TestCheckReplacement_FailsEveryDatumOfABlockThatKeepsTheDrillsMapAndRestoresNothing(t *testing.T) {
	_, in := newStateBlock(t, nil, func(*atomic.Bool) carrier { return forgetful{} }, false)
	in.Data = &keptState{}

	problems := kittest.CheckReplacement(in)

	for _, datum := range []string{`"alpha"`, `"beta"`, `"gamma"`} {
		wantProblemNaming(t, problems, "replacement lost datum", datum)
	}
}

func TestAssertReplacement_LogsTheUnitItDrilledWithTheStagesItRan(t *testing.T) {
	_, in := memoryBlock(t, "", false)

	tb := runAndCollect(func(tb testing.TB) { kittest.AssertReplacement(tb, in) })

	want := `kittest.AssertReplacement drill of "app-data" ran: subscribe, write, snapshot, replace, upgrade, restore, observe, read-back`
	if tb.failed || !slices.Contains(tb.logs, want) {
		t.Errorf("AssertReplacement failed = %v and logged %q; want it to pass and log %q, so blockverify can tell which unit was drilled", tb.failed, tb.logs, want)
	}
}

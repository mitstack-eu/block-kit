// A snapshot or a restore asks a block to carry one declared state across a replacement on /hook, and the handler answers it only for a block that lists both operations and declares the unit.
package handler_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
)

const continuityUnits = "  continuity:\n" +
	"    - name: app-data\n      kind: postgres\n      stateful: true\n      handover: snapshot\n" +
	"    - name: gateway\n      kind: server\n      stateful: false\n      handover: none\n"

var (
	snapshotManifest = strings.Replace(strings.Replace(blockLevelManifest,
		"operations: [subscribe, unsubscribe, upgrade, validate]", "operations: [subscribe, unsubscribe, upgrade, validate, snapshot, restore]", 1),
		"  lifecycle:\n", continuityUnits+"  lifecycle:\n", 1)
	handoverKey = contract.HandoverOutputKey("app-data")
)

type snapshottingBlock struct {
	*blockResource
	handover string
	err      error
	taken    []string
	restored []string
	handed   []string
	desired  []contract.Desired
}

func (b *snapshottingBlock) Snapshot(_ context.Context, d contract.Desired, unit string) (string, error) {
	b.taken = append(b.taken, unit)
	b.desired = append(b.desired, d)
	return b.handover, b.err
}

func (b *snapshottingBlock) Restore(_ context.Context, d contract.Desired, unit, handover string) error {
	b.restored = append(b.restored, unit)
	b.handed = append(b.handed, handover)
	b.desired = append(b.desired, d)
	return b.err
}

func (b *snapshottingBlock) Ensure(ctx context.Context, d contract.Desired) error {
	b.desired = append(b.desired, d)
	return b.blockResource.Ensure(ctx, d)
}

func newSnapshottingBlock(handover string) *snapshottingBlock {
	return &snapshottingBlock{blockResource: &blockResource{cloud: newFakeCloud()}, handover: handover}
}

func serveContinuity(t *testing.T, r handler.Resource, manifest, op string, fields map[string]any) (int, contract.Response) {
	t.Helper()
	return serveBlock(t, blockCall{r: r, obs: asObservers(blockObservers(newFakeCloud())), op: op, fields: fields,
		config: []func(*handler.Config){withManifest(manifest)}})
}

func TestSnapshot_AnswersTheHandoverTheBlockPublishesUnderTheUnitsOutputKey(t *testing.T) {
	block := newSnapshottingBlock("hnd_0001")

	status, resp := serveContinuity(t, block, snapshotManifest, "snapshot", map[string]any{"unit": "app-data", "replacing": true})

	if status != http.StatusOK || resp.Outputs[handoverKey] != "hnd_0001" {
		t.Errorf("snapshot of app-data: status %d, outputs %v; want 200 and the handover under %q", status, resp.Outputs, handoverKey)
	}
	if len(block.taken) != 1 || block.taken[0] != "app-data" || !block.desired[0].Replacing {
		t.Errorf("the block saw snapshots %v with desired %+v; want one for app-data sent during a replacement", block.taken, block.desired)
	}
	if block.ensureCalls+block.deleteCalls != 0 {
		t.Errorf("a snapshot ran %d Ensure and %d Delete calls, want neither verb", block.ensureCalls, block.deleteCalls)
	}
}

func TestSnapshot_PublishesNoHandoverWhenTheBlocksCheckFails(t *testing.T) {
	block := newSnapshottingBlock("hnd_unchecked")
	block.err = errors.New("the scratch restore of app-data read 3 rows, want 4")

	status, resp := serveContinuity(t, block, snapshotManifest, "snapshot", map[string]any{"unit": "app-data"})

	if status != http.StatusInternalServerError || resp.Outputs[handoverKey] != "" || !strings.Contains(resp.Error, "scratch restore") {
		t.Errorf("a failed check answered status %d, outputs %v, error %q; want 500 naming the check and no handover", status, resp.Outputs, resp.Error)
	}
}

func TestSnapshot_AnswersAWaitAs200PendingWithItsDetailNoHandoverAndAProgressEvent(t *testing.T) {
	block := newSnapshottingBlock("hnd_0001")
	block.err = handler.Wait("the scratch copy of app-data is restoring with %s", guardSecret)
	events := newFakeEventsServer(t)

	status, resp := serveContinuity(t, block, snapshotManifest, "snapshot", map[string]any{"unit": "app-data", "secrets": guardSecrets(), "eventsCallback": map[string]any{"url": events.URL, "token": "events-tok"}})

	if want := "the scratch copy of app-data is restoring with [REDACTED]"; status != http.StatusOK || !resp.Pending || resp.StatusDetail != want || len(resp.Outputs) != 0 {
		t.Errorf("a snapshot the block holds back answered %d, pending %v, statusDetail %q, outputs %v; want 200, pending, statusDetail %q and no handover", status, resp.Pending, resp.StatusDetail, resp.Outputs, want)
	}
	if got := events.recorded(); len(got) != 1 || got[0].Status != "progress" || got[0].Detail != resp.StatusDetail {
		t.Errorf("a pending snapshot sent the events %+v, want one progress event carrying its statusDetail", got)
	}
}

func TestSnapshot_SentAgainAtTheFirstSendsCreatedAtOnceTheWorkFinishesHandsOverWithACompletedEvent(t *testing.T) {
	block := newSnapshottingBlock("hnd_0001")
	block.err = handler.Wait("the scratch copy of app-data is restoring")
	events := newFakeEventsServer(t)
	fields := map[string]any{"unit": "app-data", "eventsCallback": map[string]any{"url": events.URL, "token": "events-tok"}}
	if status, resp := serveContinuity(t, block, snapshotManifest, "snapshot", fields); status != http.StatusOK || !resp.Pending {
		t.Fatalf("the first send answered %d with pending %v, want 200 pending", status, resp.Pending)
	}
	block.err, fields["settlesAt"] = nil, block.desired[0].VerbAt.Format(time.RFC3339Nano)

	status, resp := serveContinuity(t, block, snapshotManifest, "snapshot", fields)

	if status != http.StatusOK || resp.Pending || resp.Outputs[handoverKey] != "hnd_0001" {
		t.Errorf("the resend answered %d, pending %v, outputs %v; want 200 with the handover under %q", status, resp.Pending, resp.Outputs, handoverKey)
	}
	if last := block.desired[len(block.desired)-1].VerbAt; len(block.desired) != 2 || !last.Equal(block.desired[0].VerbAt) {
		t.Errorf("the block saw %d sends, the last at %v; want two, the resend at the first send's createdAt %v, so both name one snapshot", len(block.desired), last, block.desired[0].VerbAt)
	}
	if got := events.recorded(); len(got) != 2 || got[0].Status != "progress" || got[1].Status != "completed" {
		t.Errorf("the two sends sent the events %+v, want progress and then completed", got)
	}
}

func TestSnapshot_APendingAnswerUnderOneIdempotencyKeyReachesTheBlockOnEveryDelivery(t *testing.T) {
	block := newSnapshottingBlock("hnd_0001")
	block.err = handler.Wait("the scratch copy of app-data is restoring")
	x := newHarness(t, block, withManifest(snapshotManifest), func(cfg *handler.Config) { cfg.Observers = asObservers(blockObservers(newFakeCloud())) })

	var pending []bool
	for range 2 {
		_, raw := x.post(map[string]any{"operation": "snapshot", "unit": "app-data", "idempotencyKey": "sub-1:snapshot:rep_01"})
		pending = append(pending, decodeResponse(t, raw).Pending)
	}

	if len(block.taken) != 2 || !slices.Equal(pending, []bool{true, true}) {
		t.Errorf("two deliveries under one key reached the block %d times and answered pending %v; want both to reach it and answer pending, since the cache keeps no pending answer", len(block.taken), pending)
	}
}

func TestSnapshot_Answers500AndNoHandoverForAHandoverThatIsEmptyOrRepeatsARequestSecret(t *testing.T) {
	for label, handover := range map[string]string{"an empty handover returned with no error": "", "a handover that repeats a secret the request carried": guardSecret} {
		t.Run(label, func(t *testing.T) {
			block := newSnapshottingBlock(handover)

			status, resp := serveContinuity(t, block, snapshotManifest, "snapshot", map[string]any{"unit": "app-data", "secrets": guardSecrets()})

			if status != http.StatusInternalServerError || resp.Outputs[handoverKey] != "" {
				t.Errorf("%s: status %d, outputs %v; want 500 and no %s output", label, status, resp.Outputs, handoverKey)
			}
			if strings.Contains(resp.Error, guardSecret) {
				t.Errorf("%s: the answer's error repeats the request's secret: %q", label, resp.Error)
			}
		})
	}
}

func TestRestore_HandsTheBlockTheHandoverTheSnapshotPublished(t *testing.T) {
	block := newSnapshottingBlock("")

	status, resp := serveContinuity(t, block, snapshotManifest, "restore",
		map[string]any{"unit": "app-data", "replacing": true, "priorOutputs": map[string]string{handoverKey: "hnd_0001"}})

	if status != http.StatusOK || resp.Error != "" {
		t.Fatalf("restore of app-data: status %d, error %q; want 200", status, resp.Error)
	}
	if len(block.restored) != 1 || block.restored[0] != "app-data" || block.handed[0] != "hnd_0001" {
		t.Errorf("the block restored %v from %v; want app-data from hnd_0001", block.restored, block.handed)
	}
}

func TestSnapshotAndRestore_RefuseWhatTheManifestDoesNotDeclare(t *testing.T) {
	prior := map[string]string{handoverKey: "hnd_0001"}
	cases := []struct {
		name, manifest, op string
		fields             map[string]any
		want               string
	}{
		{"a snapshot of a unit the manifest does not declare", snapshotManifest, "snapshot", map[string]any{"unit": "scratch"}, "refused:unknown-unit"},
		{"a snapshot of a unit handed over by none", snapshotManifest, "snapshot", map[string]any{"unit": "gateway"}, "refused:unknown-unit"},
		{"a restore of a unit the manifest does not declare", snapshotManifest, "restore", map[string]any{"unit": "scratch", "priorOutputs": prior}, "refused:unknown-unit"},
		{"a restore that carries no handover", snapshotManifest, "restore", map[string]any{"unit": "app-data"}, "refused:no-handover"},
		{"a snapshot of a block whose manifest does not list it", blockLevelManifest, "snapshot", map[string]any{"unit": "app-data"}, "refused:snapshot-not-listed"},
		{"a restore of a block whose manifest does not list it", blockLevelManifest, "restore", map[string]any{"unit": "app-data", "priorOutputs": prior}, "refused:snapshot-not-listed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block := newSnapshottingBlock("hnd_0001")

			status, resp := serveContinuity(t, block, tc.manifest, tc.op, tc.fields)

			if status != http.StatusBadRequest || !strings.Contains(resp.Error, tc.want) {
				t.Errorf("%s: status %d, error %q; want 400 naming %q", tc.name, status, resp.Error, tc.want)
			}
			if len(block.taken)+len(block.restored) != 0 {
				t.Errorf("%s reached the block (snapshots %v, restores %v), want it refused first", tc.name, block.taken, block.restored)
			}
		})
	}
}

func TestRestore_ReachesTheBlockForEachUnitSentUnderOneIdempotencyKey(t *testing.T) {
	twoUnits := strings.Replace(snapshotManifest, "handover: none", "handover: snapshot", 1)
	block := newSnapshottingBlock("hnd_0001")
	x := newHarness(t, block, withManifest(twoUnits), func(cfg *handler.Config) { cfg.Observers = asObservers(blockObservers(newFakeCloud())) })

	for _, unit := range []string{"app-data", "gateway"} {
		status, raw := x.postContext(context.Background(), map[string]any{"operation": "restore"},
			map[string]any{"unit": unit, "idempotencyKey": "sub-1:restore:rep_01", "priorOutputs": map[string]any{contract.HandoverOutputKey(unit): "hnd_" + unit}})
		if status != http.StatusOK {
			t.Fatalf("restore of %s: status %d, body %s; want 200", unit, status, raw)
		}
	}

	if want := []string{"app-data", "gateway"}; !slices.Equal(block.restored, want) {
		t.Errorf("the block restored %v under one idempotency key, want %v: a second unit must not be answered from the first unit's cached answer", block.restored, want)
	}
	if want := []string{"hnd_app-data", "hnd_gateway"}; !slices.Equal(block.handed, want) {
		t.Errorf("the block was handed %v, want %v", block.handed, want)
	}
}

func TestUpgrade_HandsTheBlockTheReplacementMarkerOnlyOnARequestThatCarriesIt(t *testing.T) {
	for label, fields := range map[string]map[string]any{"an upgrade sent during a replacement": {"replacing": true}, "an upgrade sent outside a replacement": nil} {
		t.Run(label, func(t *testing.T) {
			block, want := newSnapshottingBlock("hnd_0001"), fields != nil

			status, resp := serveContinuity(t, block, snapshotManifest, "upgrade", fields)

			if len(block.desired) != 1 || block.desired[0].Replacing != want {
				t.Errorf("%s (status %d, error %q) reached Ensure with desired %+v; want one call with replacing %v, so the block knows whether to send an install retry", label, status, resp.Error, block.desired, want)
			}
		})
	}
}

func TestSnapshotAndRestore_AnswerABlocksOwnRefusalAs400WithNoHandover(t *testing.T) {
	for _, op := range []string{"snapshot", "restore"} {
		t.Run(op, func(t *testing.T) {
			block := newSnapshottingBlock("hnd_0001")
			block.err = handler.Refuse("volume-detached", "the volume of %q is not attached", "app-data")

			status, resp := serveContinuity(t, block, snapshotManifest, op, map[string]any{"unit": "app-data", "priorOutputs": map[string]string{handoverKey: "hnd_0001"}})

			if status != http.StatusBadRequest || !strings.Contains(resp.Error, "refused:volume-detached") || resp.Outputs[handoverKey] != "" {
				t.Errorf("a %s the block refuses: status %d, error %q, outputs %v; want 400 naming refused:volume-detached and no handover", op, status, resp.Error, resp.Outputs)
			}
		})
	}
}

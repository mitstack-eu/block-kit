// A hook snapshots a state to an object store as age ciphertext through presigned links, publishes its handover only after a scratch restore passes, and restores it.
package continuity_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/continuity"
)

func TestSnapshotThenRestore_BringsBackTheStateAfterItIsLost(t *testing.T) {
	for _, k := range allKinds(t) {
		t.Run(k.name, func(t *testing.T) {
			hook, _ := newHook(t, k.state)

			handover, err := hook.Snapshot(context.Background(), "app-data")
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			ho, err := continuity.ParseHandover(handover)
			if err != nil || ho.Ref != "app-data" || !ho.TakenAt.Equal(takenAt) || ho.Size <= 0 {
				t.Fatalf("Snapshot published %q, parsed as %+v, %v; want the object ref app-data, taken at %v", handover, ho, err, takenAt)
			}
			k.lose()
			if k.present() {
				t.Fatalf("the %s fixture still holds the datum after it was lost", k.name)
			}

			err = hook.Restore(context.Background(), "app-data", handover)

			if err != nil || !k.present() {
				t.Errorf("Restore of the %s state returned %v, datum back = %v; want the datum back", k.name, err, k.present())
			}
		})
	}
}

func TestSnapshot_SendsTheUploadLinkOnlyCiphertextOfKnownLength(t *testing.T) {
	for _, k := range allKinds(t) {
		t.Run(k.name, func(t *testing.T) {
			hook, s := newHook(t, k.state)

			if _, err := hook.Snapshot(context.Background(), "app-data"); err != nil {
				t.Fatalf("Snapshot: %v", err)
			}

			if len(s.puts) != 1 || bytes.Contains(s.puts[0], []byte(datum)) || !bytes.HasPrefix(s.puts[0], []byte("age-encryption.org/v1\n")) {
				t.Errorf("the %s snapshot sent %d uploads, the first holding the datum in plain text or no age header: %.60q", k.name, len(s.puts), s.puts)
			}
			if s.chunked {
				t.Errorf("the %s snapshot uploaded without a Content-Length, which a presigned PUT refuses", k.name)
			}
		})
	}
}

type failingVerify struct{ continuity.State }

func (failingVerify) Verify(context.Context, io.Reader) error {
	return errors.New("the scratch copy read 3 rows, want 4")
}

func TestSnapshot_PublishesNoHandoverWhenTheScratchCheckFails(t *testing.T) {
	for _, k := range allKinds(t) {
		t.Run(k.name, func(t *testing.T) {
			hook, _ := newHook(t, failingVerify{k.state})

			handover, err := hook.Snapshot(context.Background(), "app-data")

			if handover != "" || err == nil || !strings.Contains(err.Error(), "scratch check") || !strings.Contains(err.Error(), "3 rows, want 4") {
				t.Errorf("Snapshot with a failing check = %q, %v; want no handover and an error naming the scratch check and its cause", handover, err)
			}
		})
	}
}

func TestSnapshot_FailsNamingTheStageAndNeverTheLinkQuery(t *testing.T) {
	cases := []struct {
		label     string
		configure func(s *store)
		want      string
	}{
		{"the store is down", func(s *store) { s.srv.Close() }, "uploading"},
		{"the upload is refused", func(s *store) { s.putStatus = http.StatusForbidden }, "403"},
		{"the stored object cannot be read back", func(s *store) { s.getStatus = http.StatusInternalServerError }, "500"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			k := volumeKind(t)
			hook, s := newHook(t, k.state)
			tc.configure(s)

			handover, err := hook.Snapshot(context.Background(), "app-data")

			if handover != "" || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Snapshot when %s = %q, %v; want no handover and an error naming %q", tc.label, handover, err, tc.want)
			}
			if strings.Contains(err.Error(), "X-Amz") || strings.Contains(err.Error(), signature) {
				t.Errorf("the error when %s carries the link's query: %v", tc.label, err)
			}
		})
	}
}

func TestSnapshot_RefusesAKeyHolderThatHandsOverNoKey(t *testing.T) {
	hook, _ := newHook(t, volumeKind(t).state)
	hook.Keys = brokenKeys{}

	handover, err := hook.Snapshot(context.Background(), "app-data")

	if handover != "" || err == nil || !strings.Contains(err.Error(), "keys") {
		t.Errorf("Snapshot with no recipient = %q, %v; want no handover and an error naming the keys", handover, err)
	}
}

type brokenKeys struct{}

func (brokenKeys) Recipient(context.Context, string) (string, error) { return "not-a-key", nil }
func (brokenKeys) Identity(context.Context, string) (string, error)  { return "not-a-key", nil }

func TestRestore_ImportsNothingFromAnObjectThatIsNotTheOneSnapshotted(t *testing.T) {
	for _, k := range allKinds(t) {
		t.Run(k.name, func(t *testing.T) {
			hook, s := newHook(t, k.state)
			handover, err := hook.Snapshot(context.Background(), "app-data")
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			k.lose()
			s.getCorrupt = true

			err = hook.Restore(context.Background(), "app-data", handover)

			if err == nil || k.present() {
				t.Errorf("Restore of a tampered %s object returned %v, datum back = %v; want an error and nothing imported", k.name, err, k.present())
			}
		})
	}
}

func TestRestore_RefusesAHandoverNoSnapshotPublished(t *testing.T) {
	hook, _ := newHook(t, volumeKind(t).state)
	for _, handover := range []string{"", "hnd_0001", `{"ref":"app-data"}`} {
		err := hook.Restore(context.Background(), "app-data", handover)

		if err == nil {
			t.Errorf("Restore(%q) = nil, want an error", handover)
		}
	}
}

// silentState exports nothing and accepts whatever it is handed back.
type silentState struct{}

func (silentState) Export(context.Context, io.Writer) error { return nil }
func (silentState) Import(context.Context, io.Reader) error { return nil }
func (silentState) Verify(context.Context, io.Reader) error { return nil }

func TestSnapshot_PublishesNoHandoverAndUploadsNothingWhenTheStateExportsNothing(t *testing.T) {
	hook, s := newHook(t, silentState{})

	handover, err := hook.Snapshot(context.Background(), "app-data")

	if handover != "" || err == nil || !strings.Contains(err.Error(), "exported nothing") {
		t.Errorf("Snapshot of a state that exports nothing = %q, %v; want no handover and an error saying the state exported nothing", handover, err)
	}
	if len(s.puts) != 0 {
		t.Errorf("Snapshot of a state that exports nothing sent %d uploads, want none", len(s.puts))
	}
}

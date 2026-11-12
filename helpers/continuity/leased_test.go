// A hook whose links are the kit's own asks the platform's lease route for each link, and publishes no handover when the route or the store refuses.
package continuity_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/continuity"
)

func TestLeasedLinks_SnapshotThenRestoreAsksTheLeaseRouteForEachLink(t *testing.T) {
	k := volumeKind(t)
	var events []string
	hook, route := newLeasedHook(t, recordedState{k.state, &events})
	hook.Links = recordedLinks{hook.Links, &events}

	handover, err := hook.Snapshot(context.Background(), "app-data")
	if err != nil {
		t.Fatalf("Snapshot through the lease route: %v", err)
	}
	k.lose()
	err = hook.Restore(context.Background(), "app-data", handover)

	if err != nil || !k.present() {
		t.Fatalf("Restore through the lease route returned %v, datum back = %v; want the datum back", err, k.present())
	}
	if want := []string{"export", "upload link"}; !slices.Equal(events, want) {
		t.Errorf("Snapshot ran %v; want %v, so the upload link is asked for once the export is ready", events, want)
	}
	wantUses := []string{continuity.LinkUpload, continuity.LinkDownload, continuity.LinkDownload}
	if uses := route.uses(); !slices.Equal(uses, wantUses) {
		t.Fatalf("the lease route received the asks %v; want %v: one upload, one download for the scratch check and one for the restore", uses, wantUses)
	}
	ho, _ := continuity.ParseHandover(handover)
	for i, ask := range route.asks {
		if ask.bearer != "Bearer "+reportToken || ask.resource != storeResource || ask.want.Unit != "app-data" {
			t.Errorf("ask %d carried bearer %q, resource %q and unit %q; want the report token, %q and app-data", i, ask.bearer, ask.resource, ask.want.Unit, storeResource)
		}
		if ask.want.Use == continuity.LinkDownload && ask.want.Ref != ho.Ref {
			t.Errorf("ask %d wants a download of %q; want the handover's object %q", i, ask.want.Ref, ho.Ref)
		}
	}
}

func TestLeasedLinks_TwoUnitsAskForTheirOwnLinksAndPublishDifferentObjects(t *testing.T) {
	hook, route := newLeasedHook(t, volumeKind(t).state)

	first, errFirst := hook.Snapshot(context.Background(), "app-data")
	second, errSecond := hook.Snapshot(context.Background(), "vault")

	if errFirst != nil || errSecond != nil || len(route.asks) != 4 {
		t.Fatalf("two snapshots returned %v and %v after %d asks; want both taken with an upload and a download ask each", errFirst, errSecond, len(route.asks))
	}
	a, _ := continuity.ParseHandover(first)
	b, _ := continuity.ParseHandover(second)
	if units := []string{route.asks[0].want.Unit, route.asks[2].want.Unit}; !slices.Equal(units, []string{"app-data", "vault"}) || a.Ref == b.Ref {
		t.Errorf("the upload asks named the units %v and the handovers the objects %q and %q; want app-data and vault, each with its own object", units, a.Ref, b.Ref)
	}
}

func TestLeasedLinks_PublishNoHandoverWhenTheLeaseIsRefusedOrUnusable(t *testing.T) {
	refuse := func(use string, status int) func(*leaseRoute) {
		return func(l *leaseRoute) { l.refuse[use] = status }
	}
	cases := []struct {
		label     string
		configure func(l *leaseRoute)
		want      string
		uploads   int
	}{
		{"a token whose subscription is removed", refuse(continuity.LinkUpload, http.StatusUnauthorized), "401", 0},
		{"a resource the block neither requires nor provides", refuse(continuity.LinkUpload, http.StatusNotFound), "404", 0},
		{"a provider whose manifest lists no lease", refuse(continuity.LinkUpload, http.StatusConflict), "409", 0},
		{"a provider that fails to mint", refuse(continuity.LinkUpload, http.StatusBadGateway), "502", 0},
		{"a provider with no outputs yet", refuse(continuity.LinkUpload, http.StatusServiceUnavailable), "Retry-After 30s", 0},
		{"an upload link whose expiry has passed", func(l *leaseRoute) { l.expired[continuity.LinkUpload] = true }, "expired", 0},
		{"a credential that holds no link", func(l *leaseRoute) { l.tamper = func(link *continuity.Link) { link.URL = "" } }, "no link", 0},
		{"an upload credential that names no object", func(l *leaseRoute) { l.tamper = func(link *continuity.Link) { link.Ref = "" } }, "no object", 0},
		{"an upload that succeeded and a download link that is refused", refuse(continuity.LinkDownload, http.StatusBadGateway), "502", 1},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			hook, route := newLeasedHook(t, volumeKind(t).state)
			tc.configure(route)

			handover, err := hook.Snapshot(context.Background(), "app-data")

			if handover != "" || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Snapshot with %s = %q, %v; want no handover and an error naming %q", tc.label, handover, err, tc.want)
			}
			if text := err.Error(); strings.Contains(text, reportToken) || strings.Contains(text, "X-Amz") || strings.Contains(text, signature) || strings.Contains(text, route.store.srv.URL) {
				t.Errorf("the error with %s carries the bearer, a link or a link's query: %v", tc.label, err)
			}
			if got := len(route.store.puts); got != tc.uploads {
				t.Errorf("Snapshot with %s sent %d uploads, want %d", tc.label, got, tc.uploads)
			}
		})
	}
}

func TestLeasedLinks_AskForANewLinkAfterTheStoreRefusedAnExpiredOne(t *testing.T) {
	hook, route := newLeasedHook(t, volumeKind(t).state)
	route.store.putStatus = http.StatusForbidden
	refused, err := hook.Snapshot(context.Background(), "app-data")
	if refused != "" || err == nil || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "X-Amz") || strings.Contains(err.Error(), signature) {
		t.Fatalf("Snapshot to a store that refuses the link = %q, %v; want no handover and the store's 403 without the link's query", refused, err)
	}
	route.store.putStatus = 0

	handover, err := hook.Snapshot(context.Background(), "app-data")

	ho, _ := continuity.ParseHandover(handover)
	if err != nil || ho.Ref != "app-data/2" {
		t.Errorf("the next Snapshot = %q, %v; want a handover naming app-data/2, the object of a second upload link", handover, err)
	}
	if uses := route.uses(); !slices.Equal(uses, []string{continuity.LinkUpload, continuity.LinkUpload, continuity.LinkDownload}) {
		t.Errorf("the lease route received the asks %v; want a new upload link for the second snapshot", uses)
	}
}

func TestParseLinkWant_ReadsWhatAHookAsksAndRefusesAnythingElse(t *testing.T) {
	cases := []struct {
		label, raw string
		want       continuity.LinkWant
		refused    bool
	}{
		{"an upload of a unit", `{"use":"upload","unit":"app-data"}`, continuity.LinkWant{Use: continuity.LinkUpload, Unit: "app-data"}, false},
		{"a download of an object", `{"use":"download","unit":"app-data","ref":"app-data/1"}`, continuity.LinkWant{Use: continuity.LinkDownload, Unit: "app-data", Ref: "app-data/1"}, false},
		{"a use no hook asks for", `{"use":"delete","unit":"app-data"}`, continuity.LinkWant{}, true},
		{"an upload that names no unit", `{"use":"upload"}`, continuity.LinkWant{}, true},
		{"a download that names no object", `{"use":"download","unit":"app-data"}`, continuity.LinkWant{}, true},
		{"no want at all", ``, continuity.LinkWant{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got, err := continuity.ParseLinkWant(json.RawMessage(tc.raw))

			if got != tc.want || (err != nil) != tc.refused {
				t.Errorf("ParseLinkWant(%s) = %+v, %v; want %+v, refused = %v", tc.raw, got, err, tc.want, tc.refused)
			}
		})
	}
}

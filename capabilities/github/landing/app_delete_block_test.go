package landing_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/capabilities/github/landing"
	"github.com/mitstack-eu/block-kit/capabilities/github/landing/landingtest"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/verify/kittest"
	"github.com/mitstack-eu/block-kit/webhook"
)

const landingManifest = "apiVersion: mitstack/v1beta\ntype: block\nmetadata:\n  id: landing-block\n  name: landing-block\nspec:\n  version: 1.0.0\n" +
	"  resources:\n    - { name: starter, type: github.scaffold-commit }\n" +
	"  lifecycle:\n    endpoint: https://landing-block.example/hook\n    operations: [subscribe, unsubscribe, upgrade, validate]\n    contract: kit/v1\n"

type landingBlock struct {
	github *ghapi.Client
	record landing.Record
}

func (landingBlock) Ensure(context.Context, contract.Desired) error { return nil }

func (b landingBlock) Delete(ctx context.Context, d contract.Desired) (map[string]string, error) {
	_, _, err := b.DeleteKeeping(ctx, d)
	return nil, err
}

func (b landingBlock) DeleteKeeping(ctx context.Context, d contract.Desired) (map[string]string, map[string][]contract.Kept, error) {
	removed, err := landing.Remove(ctx, b.github, landing.Removal{Subscription: d.SubscriptionID, Records: []landing.Record{b.record}})
	if err != nil || len(removed.Kept) == 0 {
		return nil, nil, err
	}
	return nil, map[string][]contract.Kept{"starter": removed.Kept}, nil
}

func unsubscribeForAppDelete(t *testing.T, b landingBlock) (int, contract.Response) {
	t.Helper()
	s := kittest.NewSigner(t, "kid-1", 7)
	h, err := handler.New(handler.Config{
		Resource:  b,
		Manifest:  []byte(landingManifest),
		Observers: map[string]handler.Observer{"starter": landing.Observer{Client: func(context.Context, contract.Desired) (*ghapi.Client, error) { return b.github, nil }}},
		JWKSURL:   s.JWKSServer(t),
	})
	if err != nil {
		t.Fatalf("handler.New: %v", err)
	}
	identity, err := b.record.Identity()
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	obo, err := webhook.MintOBO(s.Private, s.Kid, webhook.OBOClaims{Audience: "landing-block", OrgID: "org-1", SubscriptionID: subscription}, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("minting an OBO token: %v", err)
	}
	body := map[string]any{"operation": "unsubscribe", "subscriptionId": subscription, "blockId": "landing-block", "orgId": "org-1", "obo": obo, "appDeleting": true,
		"recordedResources": map[string]any{"starter": map[string]any{"type": "github.scaffold-commit", "identity": identity}}}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, s.HookRequest(t, "/hook", kittest.Envelope("nonce-1", time.Now(), body)))
	resp, err := contract.DecodeResponse(w.Body.Bytes())
	if err != nil {
		t.Fatalf("decoding the response: %v; body %s", err, w.Body.Bytes())
	}
	return w.Code, resp
}

func TestBlockLevel_AnUnsubscribeTheAppsDeleteSendsLeavesTheLandedFilesClosesItsOpenRemovalAndDeletesItsBranches(t *testing.T) {
	branch := "DELETE /repos/" + repo + "/git/refs/heads/mitstack/" + subscription + "/"
	cases := []struct {
		name       string
		arrange    func(t *testing.T, f *landingtest.Fake, rec landing.Record)
		wantWrites []string
	}{
		{"a removal pull request is already open from the remove branch, and main moved since it opened", func(t *testing.T, f *landingtest.Fake, rec landing.Record) {
			f.RequireCheck("managed-ci")
			if _, err := remove(client(f), rec); err == nil {
				t.Fatal("the earlier removal merged, want it left open waiting on managed-ci")
			}
			f.Edit("README.md", "# weather\n")
		}, []string{"PATCH /repos/" + repo + "/pulls/1", branch + "land", branch + "remove"}},
		{"no removal pull request is open yet", func(*testing.T, *landingtest.Fake, landing.Record) {}, []string{branch + "land"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := landingtest.New(t, repo)
			rec := land(t, f, threeFiles, landing.Record{})
			f.Branch("mitstack/" + subscription + "/land")
			f.Branch("feature/app-work")
			tc.arrange(t, f, rec)
			main, mark := f.Main(), len(f.Writes())

			status, resp := unsubscribeForAppDelete(t, landingBlock{github: client(f), record: rec})

			if status != http.StatusOK || resp.Absent == nil || !*resp.Absent {
				t.Fatalf("status = %d, absent = %v (%s), want 200 and absent", status, resp.Absent, resp.StatusDetail)
			}
			var writes []string
			for _, w := range f.Writes()[mark:] {
				writes = append(writes, w.Method+" "+w.Path)
			}
			if !reflect.DeepEqual(writes, tc.wantWrites) {
				t.Errorf("writes = %q, want only %q: no pull request opened and no commit made", writes, tc.wantWrites)
			}
			if !reflect.DeepEqual(f.Main(), main) {
				t.Errorf("main = %v, want it as the app left it, %v", f.Main(), main)
			}
			if want := []string{"feature/app-work"}; !reflect.DeepEqual(f.Branches(), want) {
				t.Errorf("branches = %v, want only the app's own, %v", f.Branches(), want)
			}
			for _, p := range f.Pulls() {
				if p.Open || p.Merged {
					t.Errorf("pull request #%d reads open %v, merged %v, want it closed unmerged", p.Number, p.Open, p.Merged)
				}
			}
			if d := resp.Resources["starter"].Delete; d != nil && len(d.Kept) > 0 {
				t.Errorf("resource \"starter\" kept %+v, want no file listed, since the whole repository stays", d.Kept)
			}
		})
	}
}

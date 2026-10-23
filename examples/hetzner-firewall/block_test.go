package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/observe"
	"github.com/mitstack-eu/block-kit/verify/kittest"
	"github.com/mitstack-eu/block-kit/webhook"
)

const apiToken = "hetzner-token-seen-only-by-tests"

var subscription = contract.Desired{
	Identity: contract.Identity{SubscriptionID: "sub_1", BlockID: "hetzner-firewall", OrgID: "org_1"},
	Secrets:  map[string]map[string]string{"hetzner": {"api_token": apiToken}},
}

type fakeFirewall struct{ name, subscription string }

// fakeHetzner serves the Hetzner Cloud firewall routes the block calls, refusing a name another firewall holds as Hetzner does.
type fakeHetzner struct {
	t         *testing.T
	url       string
	faults    *kittest.Faults
	mu        sync.Mutex
	nextID    int
	firewalls map[string]fakeFirewall
}

func newFakeHetzner(t *testing.T, faults *kittest.Faults) *fakeHetzner {
	t.Helper()
	f := &fakeHetzner{t: t, faults: faults, firewalls: map[string]fakeFirewall{}}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	f.url = server.URL
	return f
}

func (f *fakeHetzner) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, named := strings.CutPrefix(r.URL.Path, "/firewalls/")
	_, held := f.firewalls[id]
	switch {
	case r.Header.Get("Authorization") != "Bearer "+apiToken:
		w.WriteHeader(http.StatusUnauthorized)
	case r.Method == http.MethodPost && r.URL.Path == "/firewalls":
		f.create(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/firewalls":
		label := strings.TrimPrefix(r.URL.Query().Get("label_selector"), observe.HetznerSubscriptionLabel+"=")
		listed := []map[string]int{}
		for _, id := range f.held(label) {
			n, _ := strconv.Atoi(id)
			listed = append(listed, map[string]int{"id": n})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"firewalls": listed})
	case !named || !held:
		w.WriteHeader(http.StatusNotFound)
	case r.Method == http.MethodGet:
		w.WriteHeader(http.StatusOK)
	case r.Method != http.MethodDelete:
		w.WriteHeader(http.StatusMethodNotAllowed)
	case f.faults.SideEffect("delete firewall") != nil:
		w.WriteHeader(http.StatusServiceUnavailable)
	default:
		delete(f.firewalls, id)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (f *fakeHetzner) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	for _, fw := range f.firewalls {
		if fw.name == body.Name {
			w.WriteHeader(http.StatusConflict)
			return
		}
	}
	if f.faults.SideEffect("create firewall") != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	f.nextID++
	f.firewalls[strconv.Itoa(f.nextID)] = fakeFirewall{name: body.Name, subscription: body.Labels[observe.HetznerSubscriptionLabel]}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"firewall": map[string]int{"id": f.nextID}})
}

func (f *fakeHetzner) held(subscriptionID string) []string {
	var ids []string
	for id, fw := range f.firewalls {
		if fw.subscription == subscriptionID {
			ids = append(ids, id)
		}
	}
	return ids
}

func (f *fakeHetzner) Lose(_ context.Context, r contract.CreatedResource) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.firewalls, r.Identity["firewallId"])
	return nil
}

func (f *fakeHetzner) Holds(subscriptionID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.held(subscriptionID)
}

// Process starts the block afresh over this fake, as a restarted operator would start.
func (f *fakeHetzner) Process() (handler.Resource, map[string]handler.Observer) {
	cfg := testConfig(f.t, f.url, nil)
	return cfg.Resource, cfg.Observers
}

func testConfig(t *testing.T, hetznerURL string, logs *webhook.LogBuffer) handler.Config {
	t.Helper()
	cfg, err := newConfig(newHetzner(hetznerURL), logs)
	if err != nil {
		t.Fatalf("newConfig: %v", err)
	}
	return cfg
}

func TestDeclaredResources(t *testing.T) {
	fake := newFakeHetzner(t, nil)

	kittest.AssertBlock(t, kittest.Block{
		Config:   testConfig(t, fake.url, nil),
		Provider: kittest.ListerProvider{Lister: observe.HetznerLabelled{API: newHetzner(fake.url)}, Loser: fake},
		Desired:  subscription,
	})
}

func TestUnsubscribeDeletes_AfterASetupBrokenAtAnySideEffect(t *testing.T) {
	cfg := testConfig(t, "", nil)

	kittest.AssertTeardownAfterBrokenSetup(t, kittest.BrokenSetup{
		Manifest:    cfg.Manifest,
		Types:       cfg.Types,
		Desired:     subscription,
		NewProvider: func(faults *kittest.Faults) kittest.FaultProvider { return newFakeHetzner(t, faults) },
	})
}

func TestNoSecretLeak(t *testing.T) {
	fake := newFakeHetzner(t, nil)
	fake.firewalls["taken"] = fakeFirewall{name: "mitstack-sub_taken"}
	subscribe := func(subscriptionID string) map[string]any {
		return map[string]any{"operation": "subscribe", "subscriptionId": subscriptionID, "blockId": "hetzner-firewall",
			"orgId": "org_1", "secrets": map[string]any{"hetzner": map[string]any{"api_token": apiToken}}}
	}

	kittest.AssertNoSecretLeak(t, kittest.SecretLeak{
		Secrets: []string{apiToken},
		Serve: func(logs *webhook.LogBuffer) (http.Handler, *kittest.Signer) {
			signer := kittest.NewSigner(t, "example", 1)
			cfg := testConfig(t, fake.url, logs)
			cfg.JWKSURL = signer.JWKSServer(t)
			h, err := handler.New(cfg)
			if err != nil {
				t.Fatalf("handler.New: %v", err)
			}
			return h, signer
		},
		Requests: []kittest.LeakRequest{
			{Name: "subscribe", Body: subscribe("sub_1")},
			{Name: "subscribe whose firewall name another firewall holds", Body: subscribe("sub_taken"), Fails: true},
		},
	})
}

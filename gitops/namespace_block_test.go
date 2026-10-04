package gitops_test

// namespace_block_test.go drives the test block, a kubernetes.namespace
// declared in mitstack/v1beta and served by a gitops.Resource, per resource
// the way the engine does:
//
//   - kittest.CheckDeclaredResources, naming the resource with its recorded
//     cluster input, finds no problem through ensure, observe, a repeated
//     ensure, delete and the absence proof;
//   - served through handler.New, one per-resource subscribe sent twice
//     under one idempotency key commits once and answers identically, and a
//     second key with the same inputs converges with no second commit;
//   - a Desired rebuilt from the record the first ensure left (its reported
//     identity and the recorded inputs) replays ensure, observe and delete
//     to the same answers with no extra commit, and no seeded secret
//     reaches the record, an answer or a committed file;
//   - no seeded secret leaks through the handler's responses or logs.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/gitops"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/kittest"
	"github.com/mitstack-eu/operator-kit/webhook"
)

func TestNamespaceBlock_CheckDeclaredResourcesFindsNoProblemDrivingTheNamedResource(t *testing.T) {
	b := newNamespaceBlock()
	d := desiredFor("app-alpha")
	inputs := d.ResourceInputs
	d.Resource, d.ResourceInputs = "", nil

	kittest.AssertDeclaredResources(t, kittest.DeclaredResources{
		Manifest:       readNamespaceManifest(t),
		Resources:      map[string]handler.DeclaredResource{resourceName: b.resource},
		Desired:        d,
		Resource:       resourceName,
		ResourceInputs: inputs,
	})

	if got := len(b.repo.Commits()); got != 2 {
		t.Errorf("Commits = %d, want 2: one ensure commit (the repeat lands nothing) and one delete commit", got)
	}
}

// serveNamespace serves b's resource through handler.New behind a fresh
// kittest signer, with logs as its /logs buffer when non-nil.
func serveNamespace(t *testing.T, b *gitopsBlock, logs *webhook.LogBuffer) (*handler.Handler, *kittest.Signer) {
	t.Helper()
	s := kittest.NewSigner(t, "gitops-test-key", 7)
	h, err := handler.New(handler.Config{
		Manifest:     readNamespaceManifest(t),
		Resources:    map[string]handler.DeclaredResource{resourceName: b.resource},
		JWKSURL:      s.JWKSServer(t),
		Logs:         logs,
		VerbBudget:   3,
		PollInterval: time.Millisecond,
		Sleep:        func(context.Context, time.Duration) {},
	})
	if err != nil {
		t.Fatalf("handler.New: %v", err)
	}
	return h, s
}

// requestFields is the envelope of a request naming the app's namespace,
// carrying its recorded cluster input and every kind of secret.
func requestFields(operation, app string) map[string]any {
	return map[string]any{
		"operation":      operation,
		"subscriptionId": "sub-" + app,
		"blockId":        "gitops-app-namespace",
		"blockVersion":   "1.0.0",
		"orgId":          "org-1",
		"appId":          app,
		"params":         map[string]any{"appName": app},
		"resource":       resourceName,
		"resourceInputs": map[string]any{"cluster": "k3s-fsn1"},
		"secrets": map[string]any{
			"github":  map[string]any{"token": githubToken},
			"hetzner": map[string]any{"apiToken": hetznerSecret},
		},
		"secretInputs": map[string]any{"dbPassword": secretInput},
		"appSecrets":   map[string]any{"STRIPE_KEY": appSecret},
	}
}

// post signs fields as a fresh webhook delivery, with its own nonce and
// OBO token, and returns the handler's status and exact response bytes.
func post(t *testing.T, h http.Handler, s *kittest.Signer, fields map[string]any) (int, []byte) {
	t.Helper()
	body := make(map[string]any, len(fields)+1)
	for k, v := range fields {
		body[k] = v
	}
	subID, _ := fields["subscriptionId"].(string)
	orgID, _ := fields["orgId"].(string)
	appID, _ := fields["appId"].(string)
	obo, err := webhook.MintOBO(s.Private, s.Kid, webhook.OBOClaims{OrgID: orgID, AppID: appID, SubscriptionID: subID}, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("minting an OBO token: %v", err)
	}
	body["obo"] = obo
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, s.HookRequest(t, "/hook", kittest.Envelope(hex.EncodeToString(nonce), time.Now(), body)))
	return rec.Code, rec.Body.Bytes()
}

func subscribeUnder(key string) map[string]any {
	f := requestFields("subscribe", "app-alpha")
	f["idempotencyKey"] = key
	return f
}

// ensuredEntry decodes raw and returns the named resource's ensure entry,
// failing unless the subscribe settled present.
func ensuredEntry(t *testing.T, status int, raw []byte) *contract.CallOutcome {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", status, raw)
	}
	resp, err := contract.DecodeResponse(raw)
	if err != nil {
		t.Fatalf("decoding the response: %v; body %s", err, raw)
	}
	if resp.Pending || resp.Error != "" {
		t.Fatalf("subscribe did not settle: pending %v error %q; body %s", resp.Pending, resp.Error, raw)
	}
	entry := resp.Resources[resourceName].Ensure
	if entry == nil {
		t.Fatalf("the response has no ensure entry for %s; body %s", resourceName, raw)
	}
	if !reflect.DeepEqual(resp.Outputs, alphaOutputs()) {
		t.Errorf("outputs = %v, want %v", resp.Outputs, alphaOutputs())
	}
	return entry
}

func TestNamespaceBlock_ASubscribeSentTwiceUnderOneKeyCommitsOnceAndAnswersIdentically(t *testing.T) {
	b := newNamespaceBlock()
	h, s := serveNamespace(t, b, nil)

	firstStatus, first := post(t, h, s, subscribeUnder("key-1"))
	secondStatus, second := post(t, h, s, subscribeUnder("key-1"))

	entry := ensuredEntry(t, firstStatus, first)
	commits := b.repo.Commits()
	if len(commits) != 1 {
		t.Fatalf("Commits = %d, want exactly 1", len(commits))
	}
	if secondStatus != firstStatus || string(second) != string(first) {
		t.Errorf("the second delivery answered %d %s, want the first's %d %s", secondStatus, second, firstStatus, first)
	}
	want := map[string]string{"app": "app-alpha", "revision": commits[0].Revision, "paths": alphaPaths}
	if !reflect.DeepEqual(entry.Identity, want) {
		t.Errorf("ensure identity = %v, want %v", entry.Identity, want)
	}
}

func TestNamespaceBlock_ASecondKeyWithTheSameInputsConvergesWithNoSecondCommit(t *testing.T) {
	b := newNamespaceBlock()
	h, s := serveNamespace(t, b, nil)

	firstStatus, firstRaw := post(t, h, s, subscribeUnder("key-1"))
	first := ensuredEntry(t, firstStatus, firstRaw)
	secondStatus, secondRaw := post(t, h, s, subscribeUnder("key-2"))
	second := ensuredEntry(t, secondStatus, secondRaw)

	if got := len(b.repo.Commits()); got != 1 {
		t.Errorf("Commits = %d, want 1: the second key finds every file at head", got)
	}
	if !reflect.DeepEqual(second.Identity, first.Identity) || !reflect.DeepEqual(second.Outputs, first.Outputs) {
		t.Errorf("the second key reported %v %v, want the first's %v %v", second.Identity, second.Outputs, first.Identity, first.Outputs)
	}
}

// subscriptionRecord is what the platform keeps for the resource after its
// first ensure: the subscription, the inputs recorded at subscribe and the
// identity the ensure reported. It carries no secret.
type subscriptionRecord struct {
	Subscription contract.Identity          `json:"subscription"`
	Resource     string                     `json:"resource"`
	Inputs       map[string]json.RawMessage `json:"inputs"`
	Identity     map[string]string          `json:"identity"`
}

func TestNamespaceBlock_ADesiredRebuiltFromTheRecordAloneReplaysEnsureObserveAndDelete(t *testing.T) {
	b := newNamespaceBlock()
	original := desiredFor("app-alpha")
	ensured := ensure(t, b, original)
	observed := observe(t, b, original)

	raw, err := json.Marshal(subscriptionRecord{
		Subscription: original.Identity,
		Resource:     original.Resource,
		Inputs:       original.ResourceInputs,
		Identity:     ensured.Identity,
	})
	if err != nil {
		t.Fatalf("encoding the record: %v", err)
	}
	var record subscriptionRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("decoding the record: %v", err)
	}
	replay := contract.Desired{
		Identity:         record.Subscription,
		Resource:         record.Resource,
		ResourceInputs:   record.Inputs,
		ResourceIdentity: record.Identity,
	}

	reEnsured := ensure(t, b, replay)
	if !reflect.DeepEqual(reEnsured, ensured) {
		t.Errorf("replayed ensure = %+v, want the original %+v", reEnsured, ensured)
	}
	reObserved := observe(t, b, replay)
	if !reflect.DeepEqual(reObserved, observed) {
		t.Errorf("replayed observe = %+v, want the original %+v", reObserved, observed)
	}
	if got := len(b.repo.Commits()); got != 1 {
		t.Errorf("Commits = %d after the replayed ensure and observe, want 1", got)
	}

	if err := b.resource.DeleteWithRequest(context.Background(), replay); err != nil {
		t.Fatalf("replayed DeleteWithRequest: %v", err)
	}
	if got := len(b.repo.Commits()); got != 2 {
		t.Errorf("Commits = %d after the replayed delete, want 2", got)
	}
	gone := observe(t, b, replay)
	if gone.State != contract.Absent {
		t.Errorf("observe after the replayed delete = %+v, want absent", gone)
	}

	answers := map[string]any{
		"the record":               record,
		"the original ensure":      ensured,
		"the original observe":     observed,
		"the replayed ensure":      reEnsured,
		"the replayed observe":     reObserved,
		"the observe after delete": gone,
	}
	for what, v := range answers {
		text, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("encoding %s: %v", what, err)
		}
		kittest.AssertNoSecret(t, what, string(text), seededSecrets...)
	}
	for _, c := range b.repo.Commits() {
		for path, content := range c.Changes {
			kittest.AssertNoSecret(t, "committed file "+path, string(content), seededSecrets...)
		}
	}
}

func TestNamespaceBlock_LeaksNoSecretThroughTheHandlersResponsesOrLogs(t *testing.T) {
	broken := requestFields("subscribe", "app-broken")
	kittest.AssertNoSecretLeak(t, kittest.SecretLeak{
		Secrets: seededSecrets,
		Serve: func(logs *webhook.LogBuffer) (http.Handler, *kittest.Signer) {
			b := newNamespaceBlock()
			reach := b.resource.Repository
			b.resource.Repository = func(ctx context.Context, d contract.Desired) (gitops.Repository, error) {
				if d.AppID == "app-broken" {
					return nil, errors.New("the GitOps repository for app-broken is unreachable")
				}
				return reach(ctx, d)
			}
			return serveNamespace(t, b, logs)
		},
		Requests: []kittest.LeakRequest{
			{Name: "subscribe", Body: requestFields("subscribe", "app-alpha")},
			{Name: "validate", Body: requestFields("validate", "app-alpha")},
			{Name: "unsubscribe", Body: requestFields("unsubscribe", "app-alpha")},
			{Name: "subscribe to an unreachable repository", Body: broken, Fails: true},
		},
	})
}

package dockerhosttest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/events"
)

// CallbackToken is the events-callback token of the call a Platform's Context serves.
const CallbackToken = "evt-callback-token-sentinel-4d1a"

// CommitReport is one commit report the fake platform took, with the bearer it carried.
type CommitReport struct {
	Repository, Commit, Bearer string
}

// Platform is the fake platform a block reports the commits it lands to, answering each report with Status, or 202
// when Status is zero.
type Platform struct {
	Status int

	srv     *httptest.Server
	mu      sync.Mutex
	reports []CommitReport
}

// NewPlatform is a platform that takes every commit report and has taken none yet.
func NewPlatform(t testing.TB) *Platform {
	t.Helper()
	p := &Platform{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gitops-commits", p.takeReport)
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

// Context is ctx carrying the events callback of a call this platform serves, as the kit's handler sets it.
func (p *Platform) Context(ctx context.Context) context.Context {
	return events.WithEnvelope(ctx, events.Envelope{
		SubscriptionID: "sub_slice_shop",
		Callback:       &events.Callback{URL: p.srv.URL + "/v1/webhooks/operator-events", Token: CallbackToken},
	})
}

// Reports is every commit report the platform took, in the order it took them.
func (p *Platform) Reports() []CommitReport {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]CommitReport(nil), p.reports...)
}

func (p *Platform) takeReport(w http.ResponseWriter, r *http.Request) {
	var body struct{ Repository, Commit string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "the commit report is not JSON", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.reports = append(p.reports, CommitReport{Repository: body.Repository, Commit: body.Commit, Bearer: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")})
	status := p.Status
	p.mu.Unlock()
	if status == 0 {
		status = http.StatusAccepted
	}
	w.WriteHeader(status)
}

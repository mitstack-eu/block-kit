package reach

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The edge answering at all is a different fact from the app being reachable,
// and only the first one is provisioned by the block that runs the probe. A
// block that withholds `active` until the app answers is waiting on a sibling
// that cannot run until it is active — the deadlock that stalled Plain for
// seven hours on 2026-08-19. These tests pin the distinction so the settle
// gates can be written against a field instead of against Reason's wording.

func TestProbe_EdgeUp_WhenAppReturns502(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	got := probeURL(context.Background(), tlsClient(srv), srv.URL, nil, srv.URL)

	if got.Reachable {
		t.Fatalf("a 502 is not a serving app, got Reachable=true (%+v)", got)
	}
	if !got.EdgeUp {
		t.Fatalf("a 502 came FROM the edge, so the edge is up: %+v", got)
	}
}

func TestProbe_EdgeUp_WhenAppServes(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	got := probeURL(context.Background(), tlsClient(srv), srv.URL, nil, srv.URL)

	if !got.Reachable || !got.EdgeUp {
		t.Fatalf("a serving app is both reachable and behind a live edge: %+v", got)
	}
}

func TestProbe_EdgeDown_WhenNothingListens(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening on that port any more

	got := probeURL(context.Background(), &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
	}}, url, nil, url)

	if got.Reachable || got.EdgeUp {
		t.Fatalf("a refused connection proves neither the app nor the edge: %+v", got)
	}
}

func TestProbe_EdgeUp_WhenCertificateIsUntrusted(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// The default client does not trust httptest's self-signed certificate, so
	// this exercises the handshake-only branch.
	got := probeURL(context.Background(), &http.Client{}, srv.URL, nil, srv.URL)

	if !got.Reachable || !got.EdgeUp {
		t.Fatalf("a completed TLS handshake proves something is serving: %+v", got)
	}
	if got.CertTrusted {
		t.Fatalf("the certificate is self-signed, so trust must be reported false: %+v", got)
	}
}

func TestEvaluate_NoAppURL_CountsEdgeUp(t *testing.T) {
	got := Checker{}.Evaluate(context.Background(), map[string]string{})

	if !got.Reachable || !got.EdgeUp {
		t.Fatalf("a block with no URL has no serving promise to withhold: %+v", got)
	}
}

func TestEvaluate_DNSFailure_IsNotEdgeUp(t *testing.T) {
	c := Checker{Probe: func(context.Context, string) Result {
		return Result{DNSFailure: true, Reason: "app hostname does not resolve"}
	}}

	got := c.Evaluate(context.Background(), map[string]string{OutputAppURL: "https://app.example.test/"})

	if got.EdgeUp {
		t.Fatalf("a hostname that does not resolve reaches no edge: %+v", got)
	}
}

func TestEvaluate_IPFallbackServing_CountsEdgeUp(t *testing.T) {
	c := Checker{Probe: func(_ context.Context, url string) Result {
		if url == "https://198.51.100.7/" {
			return Result{Reachable: true, EdgeUp: true, ServingURL: url}
		}
		return Result{DNSFailure: true, Reason: "app hostname does not resolve"}
	}}

	got := c.Evaluate(context.Background(), map[string]string{
		OutputAppURL:   "https://app.example.test/",
		OutputPublicIP: "198.51.100.7",
	})

	if !got.Reachable || !got.EdgeUp {
		t.Fatalf("the box answered on its own address, so the edge is up: %+v", got)
	}
}

func tlsClient(srv *httptest.Server) *http.Client {
	c := srv.Client()
	return c
}

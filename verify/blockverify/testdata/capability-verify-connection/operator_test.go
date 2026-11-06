package main

import (
	"context"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/helpers/guardedhttp"
	"github.com/mitstack-eu/block-kit/verify/kittest"
	"github.com/mitstack-eu/block-kit/webhook"
)

// vendorClient reaches a local TLS server as if it were the vendor's API.
func vendorClient(t *testing.T) *http.Client {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	c, err := guardedhttp.New(guardedhttp.Config{
		Hosts:   []string{vendorHost},
		RootCAs: pool,
		Resolve: func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("203.0.113.10")}, nil },
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNoSecretLeak_VerifyConnection(t *testing.T) {
	const secret = "sendcloud-private-key-fixture-7f3a"
	verify := map[string]any{"operation": contract.OperationVerifyConnection, "orgId": "org-fixture", "capabilitySlug": "sendcloud",
		"enteredCredential": map[string]any{"public_key": "sendcloud-public-key-fixture", "private_key": secret}}
	otherCapability := map[string]any{}
	for k, v := range verify {
		otherCapability[k] = v
	}
	otherCapability["capabilitySlug"] = "mollie"

	kittest.AssertNoSecretLeak(t, kittest.SecretLeak{
		Secrets: []string{secret},
		Serve: func(logs *webhook.LogBuffer) (http.Handler, *kittest.Signer) {
			s := kittest.NewSigner(t, "kid-sendcloud", 7)
			h, err := newHandler(s.JWKSServer(t), logs, vendorClient(t))
			if err != nil {
				t.Fatal(err)
			}
			return h, s
		},
		Requests: []kittest.LeakRequest{{Name: "verify-connection", Body: verify}, {Name: "another capability", Body: otherCapability, Fails: true}},
	})
}

func TestGuardedClient(t *testing.T) {
	kittest.AssertGuardedClient(t, newClient)
}

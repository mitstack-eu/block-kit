package main

import (
	"context"
	_ "embed"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/webhook"
)

//go:embed manifest.yaml
var manifest []byte

const vendorHost = "panel.sendcloud.sc"

// verifier asks the vendor whether a key pair opens its API.
type verifier struct {
	client *http.Client
	logs   *webhook.LogBuffer
}

func (v verifier) VerifyConnection(ctx context.Context, c handler.ConnectionCheck) (handler.Verification, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+vendorHost+"/api/v2/user", nil)
	if err != nil {
		return handler.Verification{}, err
	}
	req.SetBasicAuth(c.Credential["public_key"], c.Credential["private_key"])
	if v.logs != nil {
		v.logs.Append("checked a credential for org " + c.OrgID)
	}
	log.Printf("verify-connection for org %s", c.OrgID)
	resp, err := v.client.Do(req)
	if err != nil {
		return handler.Verification{Verdict: contract.VerdictUnreachable, Detail: "the vendor could not be reached"}, nil
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return handler.Verification{Verdict: contract.VerdictRejected, Detail: "the vendor refused the key pair"}, nil
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return handler.Verification{Verdict: contract.VerdictVerified, Detail: "the vendor accepted the key pair"}, nil
	}
	return handler.Verification{Verdict: contract.VerdictUnreachable, Detail: "the vendor answered with an unexpected status"}, nil
}

// newClient follows redirects and dials the loopback whatever host it is asked for.
func newClient([]string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(_ context.Context, network, _ string) (net.Conn, error) {
		return net.Dial(network, "127.0.0.1:8080")
	}}}
}

func newHandler(jwksURL string, logs *webhook.LogBuffer, client *http.Client) (*handler.Handler, error) {
	return handler.New(handler.Config{Manifest: manifest, JWKSURL: jwksURL, Logs: logs, Verifier: verifier{client: client, logs: logs}})
}

func main() {
	h, err := newHandler(os.Getenv("JWKS_URL"), nil, newClient([]string{vendorHost}))
	if err != nil {
		log.Fatal(err)
	}
	log.Fatal(http.ListenAndServe(":8080", h))
}

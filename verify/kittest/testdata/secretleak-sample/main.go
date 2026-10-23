package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/webhook"
)

type sampleResource struct{}

// Ensure's error carries the API token, so only the kit's scrubbing keeps it out of responses and logs.
func (sampleResource) Ensure(_ context.Context, d contract.Desired) error {
	return fmt.Errorf("upstream refused API token %s", d.Secrets["api"]["token"])
}

func (sampleResource) Observe(context.Context, contract.Desired) (contract.Observation, error) {
	return contract.Observation{State: contract.Absent}, nil
}

func (sampleResource) Delete(context.Context, contract.Desired) (map[string]string, error) {
	return nil, nil
}

func (sampleResource) ObserveResource(context.Context, contract.ResourceRef) (contract.Observation, error) {
	return contract.Observation{State: contract.Absent}, nil
}

const manifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: secretleak-sample
  name: Secret Leak Sample
spec:
  version: 1.0.0
  resources: [aws.ec2-container]
  lifecycle:
    endpoint: https://secretleak-sample.example/hook
    operations: [subscribe, unsubscribe, upgrade, validate]
    contract: kit/v1
`

func newHandler(jwksURL string, logs *webhook.LogBuffer) (http.Handler, error) {
	h, err := handler.New(handler.Config{Resource: sampleResource{}, Manifest: []byte(manifest),
		Observers: map[string]handler.Observer{"aws.ec2-container": sampleResource{}}, JWKSURL: jwksURL, Logs: logs})
	if err != nil {
		return nil, err
	}
	return h, nil
}

func main() {
	logs := webhook.NewLogBuffer(0)
	log.SetOutput(io.MultiWriter(os.Stderr, logs.Writer()))
	h, err := newHandler(os.Getenv("MITSTACK_JWKS_URL"), logs)
	if err != nil {
		log.Fatalf("secretleak-sample: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/hook", h)
	mux.Handle("/logs", h)
	log.Fatal(http.ListenAndServe(":8080", mux))
}

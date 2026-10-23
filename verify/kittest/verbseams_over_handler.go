package kittest

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
)

// VerbSeamsOverHandler serves manifest through handler.New with f as the block's Resource and as the Observer of every resource the manifest declares, and signs each request for the manifest's block with a fresh nonce and idempotency key, so the idempotency cache cannot make a property pass.
func VerbSeamsOverHandler(t *testing.T, manifest []byte, f *FakeResource) VerbSeams {
	t.Helper()
	server, err := fakeBlockServer(manifest, f)
	if err != nil {
		t.Fatalf("kittest.VerbSeamsOverHandler: %v", err)
	}
	desired := contract.Desired{Identity: contract.Identity{SubscriptionID: "sub-verbseam", BlockID: server.audience, OrgID: "org-verbseam"}}
	post := func(operation string) (int, contract.Response) {
		t.Helper()
		status, resp, err := server.send(operation, desired)
		if err != nil {
			t.Fatalf("kittest.VerbSeamsOverHandler: sending %s: %v", operation, err)
		}
		return status, resp
	}

	return VerbSeams{
		Subscribe: func() (int, error) {
			status, resp := post("subscribe")
			return f.Count, errFromStatus(status, resp)
		},

		Unsubscribe: func() (int, error) {
			status, resp := post("unsubscribe")
			return f.Count, errFromStatus(status, resp)
		},

		UnsubscribeWhenAbsent: func() error {
			f.Count = 0
			status, resp := post("unsubscribe")
			return errFromStatus(status, resp)
		},

		Upgrade: func() (bool, error) {
			// A preservation check over an empty output set observes nothing, so seed one marker.
			if len(f.Outputs) == 0 {
				f.Outputs = map[string]string{"verbseam-marker": "present"}
			}
			snapshot := make(map[string]string, len(f.Outputs))
			for k, v := range f.Outputs {
				snapshot[k] = v
			}

			status, resp := post("upgrade")
			if err := errFromStatus(status, resp); err != nil {
				return false, err
			}

			preserved := true
			for k, v := range snapshot {
				if f.Outputs[k] != v {
					preserved = false
				}
			}
			return preserved, nil
		},

		ValidateWhileConverging: func() (bool, bool) {
			savedState, savedErr := f.ObserveState, f.ObserveErr
			unknown := contract.Unknown
			f.ObserveState, f.ObserveErr = &unknown, nil

			status, resp := post("validate")

			f.ObserveState, f.ObserveErr = savedState, savedErr
			return resp.Pending, status >= http.StatusBadRequest
		},

		ValidateWhenDead: func() (bool, bool) {
			savedState, savedErr := f.ObserveState, f.ObserveErr
			f.ObserveErr = errors.New("verb seam: this resource will not recover")

			status, resp := post("validate")

			f.ObserveState, f.ObserveErr = savedState, savedErr
			return resp.Pending, status >= http.StatusBadRequest
		},
	}
}

func fakeBlockServer(manifest []byte, f *FakeResource) (*blockServer, error) {
	m, err := contract.ParseManifest(manifest)
	if err != nil {
		return nil, fmt.Errorf("the manifest is not one the kit's parser accepts: %v", err)
	}
	observers := make(map[string]handler.Observer, len(m.Block.Spec.Resources))
	for _, r := range m.Block.Spec.Resources {
		observers[r.Name] = f
	}
	return newBlockServer(handler.Config{Resource: f, Manifest: manifest, Observers: observers})
}

func errFromStatus(status int, resp contract.Response) error {
	if status < http.StatusBadRequest {
		return nil
	}
	msg := resp.Error
	if msg == "" {
		msg = fmt.Sprintf("status %d", status)
	}
	return fmt.Errorf("verb seam: %s", msg)
}

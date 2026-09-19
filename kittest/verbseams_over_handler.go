package kittest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mitstack-eu/operator-kit/contract"
)

// verbSeamRequestSeq gives every request VerbSeamsOverHandler builds a
// fresh nonce and idempotency key, across every call and every test in the
// process.
var verbSeamRequestSeq int64

// VerbSeamsOverHandler builds VerbSeams that drive h — a real lifecycle
// webhook handler — over signed requests against f, so CheckVerbSemantics
// can judge any http.Handler exactly as it judges an operator's own
// hand-written seams. h is taken as http.Handler, not a concrete handler
// type, so this package keeps no dependency on the package it is built to
// test.
//
// Every request carries a fresh nonce and a fresh idempotency key: the
// handler's idempotency cache must never make a property pass by accident,
// so convergence has to come from f itself.
func VerbSeamsOverHandler(t *testing.T, h http.Handler, s *Signer, f *FakeResource) VerbSeams {
	t.Helper()

	post := func(operation string) (int, map[string]any) {
		t.Helper()
		n := atomic.AddInt64(&verbSeamRequestSeq, 1)
		body := Envelope(fmt.Sprintf("verbseam-nonce-%d", n), time.Now(), map[string]any{
			"operation":      operation,
			"subscriptionId": "sub-verbseam",
			"blockId":        "block-verbseam",
			"orgId":          "org-verbseam",
			"idempotencyKey": fmt.Sprintf("verbseam-idem-%d", n),
		})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, s.HookRequest(t, "/hook", body))
		var doc map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &doc)
		return w.Code, doc
	}

	errFromStatus := func(status int, doc map[string]any) error {
		if status < http.StatusBadRequest {
			return nil
		}
		msg, _ := doc["error"].(string)
		if msg == "" {
			msg = fmt.Sprintf("status %d", status)
		}
		return fmt.Errorf("verb seam: %s", msg)
	}

	return VerbSeams{
		Subscribe: func() (int, error) {
			status, doc := post("subscribe")
			return f.Count, errFromStatus(status, doc)
		},

		Unsubscribe: func() (int, error) {
			status, doc := post("unsubscribe")
			return f.Count, errFromStatus(status, doc)
		},

		UnsubscribeWhenAbsent: func() error {
			f.Count = 0
			status, doc := post("unsubscribe")
			return errFromStatus(status, doc)
		},

		Upgrade: func() (bool, error) {
			// The CONTRACT's own note: a preservation check over an empty
			// output set passes without observing anything, so an
			// upgrade seam seeds one marker output when the fake carries
			// none.
			if len(f.Outputs) == 0 {
				f.Outputs = map[string]string{"verbseam-marker": "present"}
			}
			snapshot := make(map[string]string, len(f.Outputs))
			for k, v := range f.Outputs {
				snapshot[k] = v
			}

			status, doc := post("upgrade")
			if err := errFromStatus(status, doc); err != nil {
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

			status, doc := post("validate")

			f.ObserveState, f.ObserveErr = savedState, savedErr
			pending, _ := doc["pending"].(bool)
			return pending, status >= http.StatusBadRequest
		},

		ValidateWhenDead: func() (bool, bool) {
			savedState, savedErr := f.ObserveState, f.ObserveErr
			f.ObserveErr = errors.New("verb seam: this resource will not recover")

			status, doc := post("validate")

			f.ObserveState, f.ObserveErr = savedState, savedErr
			pending, _ := doc["pending"].(bool)
			return pending, status >= http.StatusBadRequest
		},
	}
}

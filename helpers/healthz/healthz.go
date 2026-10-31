// Package healthz answers a block operator's health check, so every operator serves it from one handler.
package healthz

import "net/http"

// Path is the route an operator mounts the handler on, the one the platform's edge and its pod probes ask.
const Path = "/healthz"

// Handler answers 200 with the body ok, since an operator that can answer at all is up.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

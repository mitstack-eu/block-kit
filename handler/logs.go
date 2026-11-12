package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/helpers/safe"
)

// checkSubscriptionLogs refuses a logs function the platform would never call, and a manifest listing logs that the handler could not answer.
func checkSubscriptionLogs(cfg Config, declared manifestDeclarations) error {
	switch {
	case declared.logs && cfg.SubscriptionLogs == nil:
		return errors.New("handler: the manifest lists the operation logs, so Config.SubscriptionLogs must name the function that reads a subscription's lines")
	case !declared.logs && cfg.SubscriptionLogs != nil:
		return errors.New("handler: Config.SubscriptionLogs is set, but the manifest's spec.lifecycle.operations does not list logs, so the platform never sends a logs request")
	}
	return nil
}

// serveSubscriptionLogs answers a signed POST /logs, trusted as a hook is, with the lines the block's logs function reads for the request's subscription, every request secret scrubbed from them.
func (h *Handler) serveSubscriptionLogs(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authenticatedRequest(w, r)
	if !ok {
		return
	}
	if req.Operation != contract.OperationLogs {
		h.writeError(w, http.StatusBadRequest, fmt.Sprintf("/logs serves the operation %s alone, got %q", contract.OperationLogs, req.Operation))
		return
	}
	secrets := req.SecretValues().All()
	lines, err := h.readSubscriptionLogs(requestContext(r.Context(), req), req.Desired())
	if err != nil {
		status, resp := h.errorResponse(err, secrets)
		writeResponse(w, status, resp)
		return
	}
	scrubbed := make([]string, len(lines))
	for i, line := range lines {
		scrubbed[i] = safe.Scrub(line, secrets...)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string][]string{"lines": scrubbed})
}

// readSubscriptionLogs turns a panic in the logs function into an error carrying nothing from the panic value, as a verb's panic is answered.
func (h *Handler) readSubscriptionLogs(ctx context.Context, d contract.Desired) (lines []string, err error) {
	defer func() {
		if recover() != nil {
			lines, err = nil, errors.New("handler: the block panicked reading the subscription's logs")
		}
	}()
	return h.subscriptionLogs(ctx, d)
}

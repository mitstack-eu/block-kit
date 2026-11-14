package handler

import (
	"context"
	"fmt"
	"net/http"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/helpers/safe"
)

const classRunNotListed = "run-not-listed"

// checkRun refuses a run function the platform would never call, and a manifest listing run that the handler could not answer.
func checkRun(cfg Config, declared manifestDeclarations) error {
	switch {
	case declared.runs && cfg.Run == nil:
		return fmt.Errorf("handler: the manifest lists the operation %s, so Config.Run must name the function that runs a command", contract.OperationRun)
	case !declared.runs && cfg.Run != nil:
		return fmt.Errorf("handler: Config.Run is set, but the manifest's spec.lifecycle.operations does not list %s, so the platform never sends a run request", contract.OperationRun)
	}
	return nil
}

func (h *Handler) run(ctx context.Context, req contract.Request, secrets []string) (int, contract.Response) {
	if h.runFunction == nil {
		return h.errorResponse(Refuse(classRunNotListed, "this block's manifest does not list %s, so it runs no command", contract.OperationRun), secrets)
	}
	result, err := h.runFunction(ctx, req.Desired(), req.Run())
	if w, ok := waiting(err); ok {
		return http.StatusOK, contract.Response{Pending: true, StatusDetail: safe.Scrub(w.Detail, secrets...), Run: started(result)}
	}
	if err != nil {
		return h.errorResponse(err, secrets)
	}
	result.Output = safe.Scrub(result.Output, secrets...)
	return http.StatusOK, contract.Response{Run: &result}
}

// started is the start and digest of a run its host started and has not ended, all a pending answer carries of
// a run, and nil before the host started it.
func started(result contract.RunResult) *contract.RunResult {
	if result.StartedAt.IsZero() {
		return nil
	}
	return &contract.RunResult{StartedAt: result.StartedAt, Digest: result.Digest}
}

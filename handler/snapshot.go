package handler

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/mitstack-eu/block-kit/contract"
)

const (
	classSnapshotNotListed = "snapshot-not-listed"
	classUnknownUnit       = "unknown-unit"
	classNoHandover        = "no-handover"
)

func (h *Handler) snapshot(ctx context.Context, req contract.Request, secrets []string) (int, contract.Response) {
	if err := h.checkUnit(req); err != nil {
		return h.errorResponse(err, secrets)
	}
	handover, err := h.snapshotter.Snapshot(ctx, req.Desired(), req.Unit)
	if err != nil {
		return h.errorResponse(fmt.Errorf("handler: the snapshot of %q published no handover: %w", req.Unit, err), secrets)
	}
	if handover == "" || slices.Contains(secrets, handover) {
		return h.errorResponse(fmt.Errorf("handler: the snapshot of %q returned a handover that is empty or repeats a secret the request carried", req.Unit), secrets)
	}
	return http.StatusOK, contract.Response{Outputs: map[string]string{contract.HandoverOutputKey(req.Unit): handover}}
}

func (h *Handler) restore(ctx context.Context, req contract.Request, secrets []string) (int, contract.Response) {
	if err := h.checkUnit(req); err != nil {
		return h.errorResponse(err, secrets)
	}
	handover := req.PriorOutputs[contract.HandoverOutputKey(req.Unit)]
	if handover == "" {
		return h.errorResponse(Refuse(classNoHandover, "the restore of %q carries no handover under the prior output %q", req.Unit, contract.HandoverOutputKey(req.Unit)), secrets)
	}
	if err := h.snapshotter.Restore(ctx, req.Desired(), req.Unit, handover); err != nil {
		return h.errorResponse(fmt.Errorf("handler: the restore of %q: %w", req.Unit, err), secrets)
	}
	return http.StatusOK, contract.Response{}
}

// checkUnit refuses a unit the manifest does not hand over by snapshot, so the block is never asked for state it never declared.
func (h *Handler) checkUnit(req contract.Request) error {
	if h.snapshotter == nil {
		return Refuse(classSnapshotNotListed, "this block's manifest does not list %s and %s, so it snapshots and restores nothing", contract.OperationSnapshot, contract.OperationRestore)
	}
	if !slices.Contains(h.snapshotUnits, req.Unit) {
		return Refuse(classUnknownUnit, "%q is not a spec.continuity entry with handover %s", req.Unit, contract.HandoverSnapshot)
	}
	return nil
}

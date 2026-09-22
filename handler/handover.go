package handler

import (
	"context"

	"github.com/mitstack-eu/operator-kit/contract"
)

// HandoverResource is an optional extension of Resource. A block whose
// teardown needs the unsubscribe request's own params — for example to
// decide whether to snapshot data before destroying anything — or that
// must return outputs from tearing down, implements it alongside Resource.
//
// The Handler checks for it with a type assertion before calling Delete: a
// Resource that also implements HandoverResource has DeleteWithRequest
// called instead, with the unsubscribe request's full Desired (Params
// included, unlike the bare Identity Delete receives). A Resource that does
// not implement it is unaffected and keeps calling Delete(ctx, Identity)
// exactly as before.
type HandoverResource interface {
	// DeleteWithRequest tears the resource identified by d.Identity down,
	// the same way Delete does, but sees the request's Params too. Any
	// outputs it returns are merged into the unsubscribe response's
	// Outputs once the resource settles absent, alongside whatever Observe
	// itself reports, winning on a key both produce. Returning an error
	// aborts before anything is destroyed: the Handler answers the error
	// response immediately and never calls Observe to settle.
	//
	// It must be idempotent: a settle timeout answers absent:false with no
	// outputs, and a later redrive calls DeleteWithRequest again, possibly
	// after an earlier call already destroyed the resource. A second call
	// must find the snapshot (or other handover artifact) it already made,
	// or report the resource gone, rather than fail.
	//
	// Its outputs reach the wire unscrubbed, the same way Observe's do, so
	// they must never carry secret material.
	DeleteWithRequest(ctx context.Context, d contract.Desired) (outputs map[string]string, err error)
}

// RequestObserver is an optional extension of Resource. A block whose
// resource sits behind a token, or otherwise needs the request's own
// secrets to authenticate, implements it alongside Resource so its
// observation is of the real resource rather than of nothing.
//
// The Handler checks for it with a type assertion everywhere it calls
// Observe: on validate, and on every settle poll a subscribe, upgrade or
// unsubscribe drives. A Resource that also implements RequestObserver has
// ObserveWithRequest called instead of Observe, with that verb's own
// Desired (Secrets included, unlike the bare Identity Observe receives). A
// Resource that does not implement it is unaffected and keeps calling
// Observe(ctx, Identity) exactly as before.
type RequestObserver interface {
	// ObserveWithRequest reports what the resource identified by d.Identity
	// actually is right now, the same way Observe does, but sees the
	// verb's own Secrets too. The Handler holds them only for the duration
	// of that one verb call and stores them nowhere — not on the Handler,
	// not in the idempotency cache — so a later, unrelated request never
	// sees them.
	//
	// Its outputs reach the wire unscrubbed, the same way Observe's do, so
	// they must never carry secret material; a value here identical to one
	// of d.Secrets's values is refused before any response leaves the
	// Handler.
	ObserveWithRequest(ctx context.Context, d contract.Desired) (contract.Observation, error)
}

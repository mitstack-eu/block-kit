package handler

import (
	"context"

	"github.com/mitstack-eu/operator-kit/contract"
)

// HandoverResource is an optional Resource extension whose DeleteWithRequest replaces Delete, receiving the unsubscribe request's full Desired.
type HandoverResource interface {
	// DeleteWithRequest returns outputs merged into the unsubscribe response once the resource settles absent, and an error aborts before anything is destroyed.
	//
	// It must be idempotent: a settle timeout answers absent:false with no outputs and a redrive calls it again, so a second call must find the handover artifact it already made, or report the resource gone, rather than fail.
	//
	// Its outputs reach the wire unscrubbed, so they must never carry secret material.
	DeleteWithRequest(ctx context.Context, d contract.Desired) (outputs map[string]string, err error)
}

// RequestObserver is an optional extension of Resource and DeclaredResource whose ObserveWithRequest replaces Observe, receiving the verb's own Desired including its Secrets.
type RequestObserver interface {
	// ObserveWithRequest sees Secrets held only for that one verb call, never on the Handler or in the idempotency cache.
	//
	// Its outputs reach the wire unscrubbed, so they must never carry secret material, and a value identical to one of d.Secrets is refused.
	ObserveWithRequest(ctx context.Context, d contract.Desired) (contract.Observation, error)
}

package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/safe"
)

// dispatchDeclared serves one verb over a block's declared resources:
// subscribe and upgrade ensure each resource in creation order and settle it
// present before the next, unsubscribe deletes each in reverse order and
// settles it absent before the next, and validate observes each once. Every
// answer, an error included, carries the resources map: an entry for each
// resource the verb reached, holding the calls that ran for it. A resource
// still converging, left standing or failing stops the verb there, so no
// later resource is reached.
func (h *Handler) dispatchDeclared(ctx context.Context, operation string, d contract.Desired, secrets []string) (int, contract.Response) {
	run := &declaredRun{h: h, d: d, secrets: secrets, outcomes: map[string]contract.ResourceOutcome{}}
	switch operation {
	case "subscribe", "upgrade":
		return run.ensureEach(ctx)
	case "validate":
		return run.validateEach(ctx)
	case "unsubscribe":
		return run.deleteEach(ctx)
	default:
		return http.StatusInternalServerError, contract.Response{Error: "handler: unreachable — unknown operation " + operation}
	}
}

// declaredRun is one verb's pass over the declared resources, collecting
// each resource's outcome under its name.
type declaredRun struct {
	h        *Handler
	d        contract.Desired
	secrets  []string
	outcomes map[string]contract.ResourceOutcome
}

// ensureEach ensures each resource in creation order and polls its Observe
// until Present before moving to the next. Every resource present answers
// 200 with the union of their outputs and secret outputs, a later resource
// winning a key both produce. A resource not present within the verb budget
// answers 200 pending:true and stops there.
func (r *declaredRun) ensureEach(ctx context.Context) (int, contract.Response) {
	var outputs, secretOutputs map[string]string
	for _, nr := range r.h.declared {
		var entry contract.ResourceOutcome
		if err := nr.resource.Ensure(ctx, r.d); err != nil {
			return r.fail(nr.name, &entry, &entry.Ensure, err)
		}
		entry.Ensure = &contract.CallOutcome{}

		obs, attempts, err := r.settle(ctx, nr, contract.Present, &entry)
		if err != nil {
			return r.fail(nr.name, &entry, &entry.Observe, err)
		}
		r.outcomes[nr.name] = entry
		if obs.State != contract.Present {
			return http.StatusOK, contract.Response{
				Pending:      true,
				StatusDetail: safe.Scrub(fmt.Sprintf("resource %q still %s after %d attempts", nr.name, obs.State, attempts), r.secrets...),
				Resources:    r.outcomes,
			}
		}
		outputs = mergeOutputs(outputs, obs.Outputs)
		secretOutputs = mergeOutputs(secretOutputs, obs.SecretOutputs)
	}

	resp := contract.Observation{State: contract.Present, Outputs: outputs, SecretOutputs: secretOutputs}.Response()
	resp.Resources = r.outcomes
	return http.StatusOK, resp
}

// deleteEach deletes each resource in reverse creation order and polls its
// Observe until Absent before moving to the next, so a resource created
// earlier outlives every one created after it. Every resource absent
// answers 200 absent:true with the union of the absent observations'
// outputs. A resource not absent within the verb budget answers 200
// absent:false and stops there.
func (r *declaredRun) deleteEach(ctx context.Context) (int, contract.Response) {
	var outputs map[string]string
	for i := len(r.h.declared) - 1; i >= 0; i-- {
		nr := r.h.declared[i]
		var entry contract.ResourceOutcome
		if err := nr.resource.Delete(ctx, r.d.Identity); err != nil {
			return r.fail(nr.name, &entry, &entry.Delete, err)
		}
		entry.Delete = &contract.CallOutcome{}

		obs, attempts, err := r.settle(ctx, nr, contract.Absent, &entry)
		if err != nil {
			return r.fail(nr.name, &entry, &entry.Observe, err)
		}
		r.outcomes[nr.name] = entry
		if obs.State != contract.Absent {
			absent := false
			return http.StatusOK, contract.Response{
				Absent:       &absent,
				StatusDetail: safe.Scrub(fmt.Sprintf("resource %q observed %s after %d attempts", nr.name, obs.State, attempts), r.secrets...),
				Resources:    r.outcomes,
			}
		}
		outputs = mergeOutputs(outputs, obs.Outputs)
	}

	absent := true
	return http.StatusOK, contract.Response{Absent: &absent, Outputs: outputs, Resources: r.outcomes}
}

// validateEach observes each resource once, in creation order, and never
// polls. Every resource present answers as a present Observation carrying
// the union of their outputs and secret outputs; every resource absent
// answers absent:true; any other mix answers pending:true naming each
// resource's state. An Observe error is classified, never folded into
// pending, and stops the probe there.
func (r *declaredRun) validateEach(ctx context.Context) (int, contract.Response) {
	var outputs, secretOutputs map[string]string
	present, absent := 0, 0
	states := make([]string, 0, len(r.h.declared))
	for _, nr := range r.h.declared {
		var entry contract.ResourceOutcome
		obs, err := nr.resource.Observe(ctx, r.d.Identity)
		if err != nil {
			return r.fail(nr.name, &entry, &entry.Observe, err)
		}
		call, err := r.observed(obs)
		if err != nil {
			return r.fail(nr.name, &entry, &entry.Observe, err)
		}
		entry.Observe = call
		r.outcomes[nr.name] = entry

		switch obs.State {
		case contract.Present:
			present++
			outputs = mergeOutputs(outputs, obs.Outputs)
			secretOutputs = mergeOutputs(secretOutputs, obs.SecretOutputs)
		case contract.Absent:
			absent++
		}
		states = append(states, fmt.Sprintf("%q %s", nr.name, obs.State))
	}

	var resp contract.Response
	switch len(r.h.declared) {
	case present:
		resp = contract.Observation{State: contract.Present, Outputs: outputs, SecretOutputs: secretOutputs}.Response()
	case absent:
		resp = contract.Observation{State: contract.Absent}.Response()
	default:
		resp = contract.Response{
			Pending:      true,
			StatusDetail: safe.Scrub("resources observed "+strings.Join(states, ", "), r.secrets...),
		}
	}
	resp.Resources = r.outcomes
	return http.StatusOK, resp
}

// settle polls one resource's Observe up to the verb budget, stopping early
// once it reports want, and stopping before any attempt once ctx is done,
// exactly as settlePresent and settleAbsent pace a single Resource. The last
// observation is recorded as entry's observe outcome, its outputs refused
// when they carry a request secret; no observe outcome is recorded when no
// attempt ran. It returns the last observation and the attempts made.
func (r *declaredRun) settle(ctx context.Context, nr namedResource, want contract.State, entry *contract.ResourceOutcome) (contract.Observation, int, error) {
	var last contract.Observation
	attempts := 0
	for attempts < r.h.verbBudget {
		if ctx.Err() != nil {
			break
		}
		obs, err := nr.resource.Observe(ctx, r.d.Identity)
		if err != nil {
			return last, attempts, err
		}
		attempts++
		last = obs
		if obs.State == want {
			break
		}
		if attempts < r.h.verbBudget {
			r.h.sleep(ctx, r.h.pollInterval)
		}
	}
	if attempts == 0 {
		return last, attempts, nil
	}
	call, err := r.observed(last)
	if err != nil {
		return last, attempts, err
	}
	entry.Observe = call
	return last, attempts, nil
}

// observed turns an observation into its observe outcome, with the
// statusDetail scrubbed of request secrets. Outputs carrying a request
// secret are refused rather than reported.
func (r *declaredRun) observed(obs contract.Observation) (*contract.CallOutcome, error) {
	if err := refuseSecretOutputs(obs.Outputs, r.d.Secrets); err != nil {
		return nil, err
	}
	return &contract.CallOutcome{
		State:        obs.State,
		Outputs:      obs.Outputs,
		StatusDetail: safe.Scrub(obs.StatusDetail, r.secrets...),
	}, nil
}

// fail records err as the outcome of one call on the named resource, call
// pointing at that call's field in entry, and answers the verb the way
// errorResponse classifies err: a *Refused is 400, anything else 500. The
// answer carries every resource outcome recorded so far.
func (r *declaredRun) fail(name string, entry *contract.ResourceOutcome, call **contract.CallOutcome, err error) (int, contract.Response) {
	status, resp := r.h.errorResponse(err, r.secrets)
	*call = &contract.CallOutcome{Error: resp.Error, StatusDetail: resp.StatusDetail}
	r.outcomes[name] = *entry
	resp.Resources = r.outcomes
	return status, resp
}

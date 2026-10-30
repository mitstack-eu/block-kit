package handler

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"strings"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/helpers/safe"
)

const classUndeclaredResource = "undeclared-resource"

// dispatchDeclared refuses a name the manifest does not declare with a 400 before any resource is reached, unless a validate names a type Config.TypeObservers holds.
//
// A resource still converging, left standing or failing stops the verb there, and the request context is checked before each resource's first call. Every answer, an error included, carries the resources map of the resources reached.
func (h *Handler) dispatchDeclared(ctx context.Context, operation string, d contract.Desired, secrets []string) (int, contract.Response) {
	resources := h.declared
	if d.Resource != "" {
		nr, ok := h.declaredResource(d.Resource)
		if !ok {
			nr, ok = h.typeObserved(operation, d)
		}
		if !ok {
			return h.errorResponse(Refuse(classUndeclaredResource,
				"resource %q (type %q) is not declared under the manifest's spec.resources", d.Resource, d.ResourceType), secrets)
		}
		resources = []namedResource{nr}
	}

	run := &declaredRun{h: h, d: d, secrets: secrets, resources: resources, outcomes: map[string]contract.ResourceOutcome{}}
	return run.serveBlock(ctx, operation)
}

func (h *Handler) typeObserved(operation string, d contract.Desired) (namedResource, bool) {
	if operation != "validate" {
		return namedResource{}, false
	}
	o, ok := h.typeObservers[d.ResourceType]
	if t, known := h.types.Lookup(d.ResourceType); known && t.KitObserved() && h.kit.Serves(t) == nil {
		o, ok = nil, true
	}
	if !ok {
		return namedResource{}, false
	}
	decl := contract.ManifestResource{Name: d.Resource, Type: d.ResourceType, Types: []string{d.ResourceType}}
	return namedResource{name: d.Resource, observer: o, declaration: decl}, true
}

// kitObservedDeclaration reports whether every type decl lists carries an observe spec, so the block needs no observer of its own for it, and refuses one the KitObserver cannot serve.
func kitObservedDeclaration(cfg Config, decl contract.ManifestResource) (bool, error) {
	types := decl.Types
	if len(types) == 0 {
		types = []string{decl.Type}
	}
	all := true
	for _, typ := range types {
		t, ok := cfg.Types.Lookup(typ)
		if !ok || !t.KitObserved() {
			all = false
			continue
		}
		if err := cfg.KitObserver.Serves(t); err != nil {
			return false, fmt.Errorf("handler: the manifest declares resource %q of type %s, which the kit observer reads: %w", decl.Name, typ, err)
		}
	}
	return all, nil
}

func (h *Handler) declaredResource(name string) (namedResource, bool) {
	for _, nr := range h.declared {
		if nr.name == name {
			return nr, true
		}
	}
	return namedResource{}, false
}

// declaredRun's refs hold what each observer is handed, provided limits the answer's outputs, and deleteOutputs and deleteKept are what the block-level delete returned.
type declaredRun struct {
	h             *Handler
	d             contract.Desired
	secrets       []string
	resources     []namedResource
	outcomes      map[string]contract.ResourceOutcome
	refs          map[string]contract.ResourceRef
	provided      map[string]bool
	deleteOutputs map[string]string
	deleteKept    map[string][]contract.Kept
	// healthCheck is set on a validate, the only verb whose kit observation may be a full read.
	healthCheck bool

	resourceSecretOutputs map[string]map[string]string
}

func (r *declaredRun) answers(name string) bool {
	return r.provided == nil || r.provided[name]
}

// keepSecretOutputs holds a resource's secret outputs apart from outcomes, which the engine records.
func (r *declaredRun) keepSecretOutputs(name string, secretOutputs map[string]string) {
	if len(secretOutputs) == 0 {
		return
	}
	if r.resourceSecretOutputs == nil {
		r.resourceSecretOutputs = map[string]map[string]string{}
	}
	r.resourceSecretOutputs[name] = maps.Clone(secretOutputs)
}

// ensureEach observes each resource once, after the block-level handler has ensured the whole block, and answers pending at the first not yet present, since the platform's settle check observes it from there; a later resource wins a key both produce.
func (r *declaredRun) ensureEach(ctx context.Context) (int, contract.Response) {
	var outputs, secretOutputs map[string]string
	for _, nr := range r.resources {
		if ctx.Err() != nil {
			return http.StatusOK, contract.Response{Pending: true, StatusDetail: r.notReached(nr.name), Resources: r.outcomes, SecretOutputs: secretOutputs, ResourceSecretOutputs: r.resourceSecretOutputs}
		}
		entry := r.outcomes[nr.name]
		obs, err := r.observeOnce(ctx, nr, &entry)
		if err != nil {
			return r.fail(nr.name, &entry, &entry.Observe, err)
		}
		r.outcomes[nr.name] = entry
		if obs.State != contract.Present {
			return http.StatusOK, contract.Response{
				Pending:               true,
				StatusDetail:          safe.Scrub(fmt.Sprintf("resource %q observed %s, not yet present", nr.name, obs.State), r.secrets...),
				Resources:             r.outcomes,
				SecretOutputs:         secretOutputs,
				ResourceSecretOutputs: r.resourceSecretOutputs,
			}
		}
		if r.answers(nr.name) {
			outputs = mergeOutputs(outputs, obs.Outputs)
			secretOutputs = mergeOutputs(secretOutputs, obs.SecretOutputs)
			r.keepSecretOutputs(nr.name, obs.SecretOutputs)
		}
	}

	resp := contract.Observation{State: contract.Present, Outputs: outputs, SecretOutputs: secretOutputs}.Response()
	resp.Resources = r.outcomes
	resp.ResourceSecretOutputs = r.resourceSecretOutputs
	return http.StatusOK, resp
}

// deleteEach observes each resource once in reverse creation order, after the block-level handler has deleted the whole block, and answers absent:false at the first not yet absent; it observes none whose type ends with its subscription, skips one the engine recorded absent, merges the delete's outputs over the observed ones on absent:true, and answers each resource's kept files on its delete outcome.
func (r *declaredRun) deleteEach(ctx context.Context) (int, contract.Response) {
	var outputs map[string]string
	for name, kept := range r.deleteKept {
		if len(kept) > 0 {
			r.outcomes[name] = contract.ResourceOutcome{Delete: &contract.CallOutcome{Kept: kept}}
		}
	}
	for i := len(r.resources) - 1; i >= 0; i-- {
		nr := r.resources[i]
		if ctx.Err() != nil {
			absent := false
			return http.StatusOK, contract.Response{Absent: &absent, StatusDetail: r.notReached(nr.name), Resources: r.outcomes}
		}
		// The engine's record already proves it absent, often through a host that is gone, so observing it could only fail.
		if r.d.RecordedResources[nr.name].State == contract.Absent {
			continue
		}
		entry := r.outcomes[nr.name]
		if ended, ok := r.endedWithSubscription(nr); ok {
			if entry.Delete != nil {
				ended.Kept = entry.Delete.Kept
			}
			entry.Delete = ended
			r.outcomes[nr.name] = entry
			continue
		}

		obs, err := r.observeOnce(ctx, nr, &entry)
		if err != nil {
			return r.fail(nr.name, &entry, &entry.Observe, err)
		}
		r.outcomes[nr.name] = entry
		if obs.State != contract.Absent {
			absent := false
			return http.StatusOK, contract.Response{
				Absent:       &absent,
				StatusDetail: safe.Scrub(fmt.Sprintf("resource %q observed %s, not yet absent", nr.name, obs.State), r.secrets...),
				Resources:    r.outcomes,
			}
		}
		if r.answers(nr.name) {
			outputs = mergeOutputs(outputs, obs.Outputs)
		}
	}

	outputs = mergeOutputs(outputs, r.deleteOutputs)
	if err := refuseSecretValues("output", outputs, r.d.Secrets); err != nil {
		status, resp := r.h.errorResponse(err, r.secrets)
		resp.Resources = r.outcomes
		return status, resp
	}
	absent := true
	return http.StatusOK, contract.Response{Absent: &absent, Outputs: outputs, Resources: r.outcomes}
}

// endedWithSubscription records nr absent by its delete, not by an observe, when Config.Types marks its type EndsWithSubscription.
func (r *declaredRun) endedWithSubscription(nr namedResource) (*contract.CallOutcome, bool) {
	ref := r.refFor(nr, r.refs[nr.name].Type, r.refs[nr.name].Identity)
	t, ok := r.h.types.Lookup(ref.Type)
	if !ok || !t.EndsWithSubscription {
		return nil, false
	}
	return &contract.CallOutcome{
		State:        contract.Absent,
		Type:         ref.Type,
		Identity:     ref.Identity,
		StatusDetail: fmt.Sprintf("type %s ends with its subscription, so the resource is absent once the delete succeeds, unobserved", ref.Type),
	}, true
}

// validateEach never polls, answers redrive when any resource observed Unknown reports its work finished, and an Observe error is classified, never folded into pending, and stops the probe there.
func (r *declaredRun) validateEach(ctx context.Context) (int, contract.Response) {
	if c, ok := r.h.resource.(Converger); ok {
		detail, done, err := c.Converging(ctx, r.d)
		if err != nil {
			return r.h.errorResponse(err, r.secrets)
		}
		if detail != "" || done {
			return r.converging(detail, done)
		}
	}
	var outputs, secretOutputs map[string]string
	present, absent, finished := 0, 0, false
	r.healthCheck = true
	states := make([]string, 0, len(r.resources))
	for _, nr := range r.resources {
		if ctx.Err() != nil {
			return http.StatusOK, contract.Response{Pending: true, StatusDetail: r.notReached(nr.name), Resources: r.outcomes, SecretOutputs: secretOutputs, ResourceSecretOutputs: r.resourceSecretOutputs}
		}
		var entry contract.ResourceOutcome
		obs, err := r.observeOnce(ctx, nr, &entry)
		if err != nil {
			return r.fail(nr.name, &entry, &entry.Observe, err)
		}
		r.outcomes[nr.name] = entry

		switch obs.State {
		case contract.Present:
			present++
			if r.answers(nr.name) {
				outputs = mergeOutputs(outputs, obs.Outputs)
				secretOutputs = mergeOutputs(secretOutputs, obs.SecretOutputs)
				r.keepSecretOutputs(nr.name, obs.SecretOutputs)
			}
		case contract.Absent:
			absent++
		case contract.Unknown:
			finished = finished || obs.Redrive
		}
		states = append(states, fmt.Sprintf("%q %s", nr.name, obs.State))
	}

	var resp contract.Response
	switch len(r.resources) {
	case present:
		resp = contract.Observation{State: contract.Present, Outputs: outputs, SecretOutputs: secretOutputs}.Response()
	case absent:
		resp = contract.Observation{State: contract.Absent}.Response()
	default:
		resp = contract.Response{
			Pending:       true,
			Redrive:       finished,
			StatusDetail:  safe.Scrub("resources observed "+strings.Join(states, ", "), r.secrets...),
			SecretOutputs: secretOutputs,
		}
	}
	resp.Resources = r.outcomes
	resp.ResourceSecretOutputs = r.resourceSecretOutputs
	return http.StatusOK, resp
}

// converging answers every resource unknown with the block's detail, and redrive once its work finished, keeping the identity each was handed.
func (r *declaredRun) converging(detail string, done bool) (int, contract.Response) {
	if detail == "" {
		detail = "the work the last subscribe left running has finished, so the next subscribe reports what it made"
	}
	detail = safe.Scrub(detail, r.secrets...)
	for _, nr := range r.resources {
		ref := r.refs[nr.name]
		r.outcomes[nr.name] = contract.ResourceOutcome{Observe: &contract.CallOutcome{State: contract.Unknown, StatusDetail: detail, Identity: ref.Identity, Type: ref.Type}}
	}
	return http.StatusOK, contract.Response{Pending: true, Redrive: done, StatusDetail: detail, Resources: r.outcomes}
}

func (r *declaredRun) notReached(name string) string {
	return safe.Scrub(fmt.Sprintf("resource %q not reached: the request ended before the verb got to it", name), r.secrets...)
}

// observeOnce records what one observation of nr reports as its observe outcome.
func (r *declaredRun) observeOnce(ctx context.Context, nr namedResource, entry *contract.ResourceOutcome) (contract.Observation, error) {
	obs, err := r.observe(ctx, nr)
	if err != nil {
		return obs, err
	}
	call, err := r.observed(nr, obs)
	if err != nil {
		return obs, err
	}
	entry.Observe = call
	return obs, nil
}

// observe hands a resource whose type carries an observe spec to the kit observer, and any other to the block's own observer.
func (r *declaredRun) observe(ctx context.Context, nr namedResource) (contract.Observation, error) {
	ref := r.refs[nr.name]
	t, ok := r.h.types.Lookup(ref.Type)
	if !ok || !t.KitObserved() {
		if nr.observer == nil {
			return contract.Observation{}, Refuse(classUndeclaredType,
				"resource %q is of type %q, which carries no observe spec, and the block registers no observer for it", nr.name, ref.Type)
		}
		return nr.observer.ObserveResource(ctx, ref)
	}
	key := fullReadKey{subscriptionID: r.d.SubscriptionID, resource: nr.name}
	now := r.h.now()
	if !r.healthCheck || !r.h.fullReads.due(key, now) {
		return r.h.kit.Probe(ctx, t, ref)
	}
	obs, read, err := r.h.kit.FullRead(ctx, t, ref)
	if err != nil {
		return obs, err
	}
	r.h.fullReads.record(key, now)
	if read == nil || obs.State != contract.Present || r.h.fullReadChecker == nil {
		return obs, nil
	}
	problem, err := r.h.fullReadChecker.CheckFullRead(ctx, ref, *read)
	if err != nil {
		return contract.Observation{}, err
	}
	if problem != "" {
		return contract.Observation{State: contract.Unknown, Identity: ref.Identity,
			StatusDetail: fmt.Sprintf("resource %q: unhealthy: the full read found %s", nr.name, problem)}, nil
	}
	return obs, nil
}

// observed refuses outputs or an identity carrying a request secret rather than reporting them, and keeps the identity the observer was handed when it reports none.
func (r *declaredRun) observed(nr namedResource, obs contract.Observation) (*contract.CallOutcome, error) {
	if err := refuseSecretValues("output", obs.Outputs, r.d.Secrets); err != nil {
		return nil, err
	}
	if err := refuseSecretValues("identity key", obs.Identity, r.d.Secrets); err != nil {
		return nil, err
	}
	ref := r.refs[nr.name]
	identity := ref.Identity
	if len(obs.Identity) > 0 {
		identity = obs.Identity
	}
	return &contract.CallOutcome{
		State:        obs.State,
		Outputs:      obs.Outputs,
		StatusDetail: safe.Scrub(obs.StatusDetail, r.secrets...),
		Identity:     identity,
		Type:         ref.Type,
	}, nil
}

// fail answers as errorResponse classifies err, carrying every resource outcome recorded so far.
func (r *declaredRun) fail(name string, entry *contract.ResourceOutcome, call **contract.CallOutcome, err error) (int, contract.Response) {
	status, resp := r.h.errorResponse(err, r.secrets)
	*call = &contract.CallOutcome{Error: resp.Error, StatusDetail: resp.StatusDetail}
	r.outcomes[name] = *entry
	resp.Resources = r.outcomes
	return status, resp
}

package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/helpers/safe"
)

const (
	classUndeclaredType   = "undeclared-type"
	classBlockLevelVerb   = "block-level-verb"
	classRepeatedResource = "repeated-resource"
)

// serveBlock serves a request naming one resource for validate alone, and a recorded or requested type reaches the observer even when the entry no longer lists it, since the resource was created as it.
func (r *declaredRun) serveBlock(ctx context.Context, operation string) (int, contract.Response) {
	r.refs = make(map[string]contract.ResourceRef, len(r.resources))
	if r.d.Resource != "" {
		if operation != "validate" {
			return r.h.errorResponse(Refuse(classBlockLevelVerb,
				"resource %q: a request naming one resource is served for validate alone, since the block creates and deletes its resources through its block-level handler", r.d.Resource), r.secrets)
		}
		nr := r.resources[0]
		r.refs[nr.name] = r.refFor(nr, r.d.ResourceType, r.d.ResourceIdentity)
		return r.withOwnSecrets(r.validateEach(ctx))
	}

	r.provided = r.h.provided
	switch operation {
	case "subscribe", "upgrade":
		reported, err := r.ensureBlock(ctx)
		if w, ok := waiting(err); ok {
			return r.withOwnSecrets(http.StatusOK, contract.Response{Pending: true, StatusDetail: safe.Scrub(w.Detail, r.secrets...)})
		}
		if err != nil {
			return r.h.errorResponse(err, r.secrets)
		}
		r.d.VerbAt = r.h.now()
		r.resources = units(r.h.declared, reported)
		r.setRefs(reported)
		r.ensured(reported)
		return r.withOwnSecrets(r.ensureEach(ctx))
	case "unsubscribe":
		outputs, err := r.deleteBlock(ctx)
		w, waits := waiting(err)
		switch {
		case waits && !w.Removal:
			absent := false
			return http.StatusOK, contract.Response{Absent: &absent, StatusDetail: safe.Scrub(w.Detail, r.secrets...)}
		case err != nil && !waits:
			return r.h.errorResponse(err, r.secrets)
		}
		r.deleteOutputs = outputs
		r.d.VerbAt = r.h.now()
		r.resources = units(r.h.declared, r.d.RecordedResources)
		r.setRefs(nil)
		for name, ref := range r.refs {
			ref.Unsubscribing = true
			r.refs[name] = ref
		}
		if waits {
			return r.removalAnswer(ctx, w.Detail)
		}
		return r.deleteEach(ctx)
	case "validate":
		replaces, err := r.planReplacements(ctx)
		if err != nil {
			return r.h.errorResponse(err, r.secrets)
		}
		r.resources = units(r.h.declared, r.d.RecordedResources)
		r.setRefs(nil)
		status, resp := r.withOwnSecrets(r.validateEach(ctx))
		if status == http.StatusOK {
			resp.Replaces = replaces
		}
		return status, resp
	default:
		return http.StatusInternalServerError, contract.Response{Error: "handler: unreachable — unknown operation " + operation}
	}
}

// Waiting holds a call back until something outside the block passes, answered pending, or absent:false on an unsubscribe.
type Waiting struct {
	Detail string
	// Removal, on an unsubscribe, has the absent:false answer carry each standing resource's observe, so the engine reads a removal an observer reports under way.
	Removal bool
}

func (e *Waiting) Error() string { return e.Detail }

func Wait(format string, args ...any) error {
	return &Waiting{Detail: fmt.Sprintf(format, args...)}
}

// WaitOnRemoval is Wait for a Delete whose removal has started and moves outside the block, such as a pull request waiting on its checks.
func WaitOnRemoval(format string, args ...any) error {
	return &Waiting{Detail: fmt.Sprintf(format, args...), Removal: true}
}

// OwnSecretsReporter, when a block-level Resource implements it, replaces EnsureResources with EnsureResourcesOwning, whose own secrets reach every subscribe and upgrade answer that does not fail, a pending one and one beside a Wait error included, and an observer returning the same name later overrides the value the ensure returned.
type OwnSecretsReporter interface {
	EnsureResourcesOwning(ctx context.Context, d contract.Desired) (created []contract.CreatedResource, ownSecrets map[string]string, err error)
}

func waiting(err error) (*Waiting, bool) {
	var w *Waiting
	return w, errors.As(err, &w)
}

// ensureBlock returns what the ensure reported by resource name, refusing a
// resource undeclared, reported twice, of an unlisted type, or with a secret
// in its identity.
func (r *declaredRun) ensureBlock(ctx context.Context) (map[string]contract.CreatedResource, error) {
	created, err := r.ensureReporting(ctx)
	if err != nil {
		return nil, err
	}
	reported := make(map[string]contract.CreatedResource, len(created))
	for _, c := range created {
		nr, err := r.h.reportedResource(c.Name)
		if err != nil {
			return nil, err
		}
		if _, repeated := reported[c.Name]; repeated {
			return nil, Refuse(classRepeatedResource, "the block reported resource %q twice, and an ensure reports each resource once", c.Name)
		}
		if !slices.Contains(nr.declaration.Types, c.Type) {
			return nil, typeNotListed(nr, c.Type)
		}
		if err := refuseSecretValues("identity key", c.Identity, r.d.Secrets); err != nil {
			return nil, err
		}
		reported[c.Name] = c
	}
	return reported, nil
}

// ensureReporting copies the own secrets an ensure returns, even beside an error, so a Wait answer carries them and the block's map stays its own.
func (r *declaredRun) ensureReporting(ctx context.Context) ([]contract.CreatedResource, error) {
	switch res := r.h.resource.(type) {
	case OwnSecretsReporter:
		created, own, err := res.EnsureResourcesOwning(ctx, r.d)
		r.ownSecrets = mergeOutputs(nil, own)
		return created, err
	case ResourceReporter:
		return res.EnsureResources(ctx, r.d)
	default:
		return nil, r.h.resource.Ensure(ctx, r.d)
	}
}

// reportedResource accepts the name of an entry not marked many or of a member of one marked many, and refuses any other, since the engine records each reported name as a resource of its own.
func (h *Handler) reportedResource(name string) (namedResource, error) {
	entry, _, member := strings.Cut(name, "/")
	nr, ok := h.declaredResource(entry)
	switch {
	case !ok:
		return namedResource{}, Refuse(classUndeclaredResource, "the block reported resource %q, which the manifest's spec.resources does not declare", name)
	case !member && nr.declaration.Many:
		return namedResource{}, Refuse(classUndeclaredResource, "the block reported resource %q, which the manifest marks many, so the block reports each of its members as %s/<key>", name, name)
	case member && !nr.declaration.Many:
		return namedResource{}, Refuse(classUndeclaredResource, "the block reported resource %q, a member of %q, which the manifest does not mark many", name, entry)
	case member && !nr.declaration.IsMember(name):
		return namedResource{}, Refuse(classUndeclaredResource, "the block reported resource %q, whose key after %q is not 1 to 40 lowercase letters, digits and hyphens", name, entry+"/")
	}
	return nr.named(name), nil
}

func (r *declaredRun) deleteBlock(ctx context.Context) (map[string]string, error) {
	reporter, ok := r.h.resource.(KeptReporter)
	if !ok {
		return r.h.resource.Delete(ctx, r.d)
	}
	outputs, kept, err := reporter.DeleteKeeping(ctx, r.d)
	if err != nil {
		return nil, err
	}
	for name := range kept {
		if _, ok := r.h.declaredResource(name); !ok {
			return nil, Refuse(classUndeclaredResource,
				"the block kept files of resource %q, which the manifest's spec.resources does not declare", name)
		}
	}
	r.deleteKept = kept
	return outputs, nil
}

func (r *declaredRun) planReplacements(ctx context.Context) ([]string, error) {
	planner, ok := r.h.resource.(ReplacementPlanner)
	if !ok {
		return nil, nil
	}
	replaces, err := planner.PlanReplacements(ctx, r.d)
	if err != nil {
		return nil, err
	}
	for _, name := range replaces {
		if _, ok := r.h.declaredResource(name); !ok {
			return nil, Refuse(classUndeclaredResource,
				"the block plans to replace resource %q, which the manifest's spec.resources does not declare", name)
		}
	}
	return replaces, nil
}

func (r *declaredRun) setRefs(reported map[string]contract.CreatedResource) {
	for _, nr := range r.resources {
		c := reported[nr.name]
		r.refs[nr.name] = r.refFor(nr, c.Type, c.Identity)
	}
}

// ensured gives each reported resource an ensure outcome carrying its type and identity, so the engine records them even when the answer stops pending before observing it.
func (r *declaredRun) ensured(reported map[string]contract.CreatedResource) {
	for name, c := range reported {
		r.outcomes[name] = contract.ResourceOutcome{Ensure: &contract.CallOutcome{Type: c.Type, Identity: c.Identity}}
	}
}

// refFor falls back to the engine's record, then to the one type nr's
// entry lists.
func (r *declaredRun) refFor(nr namedResource, typ string, identity map[string]string) contract.ResourceRef {
	rec := r.d.RecordedResources[nr.name]
	if typ == "" {
		typ = rec.Type
	}
	if typ == "" {
		typ = nr.declaration.Type
	}
	if identity == nil {
		identity = rec.Identity
	}
	return contract.ResourceRef{Name: nr.name, Type: typ, Identity: identity, Desired: r.d}
}

func typeNotListed(nr namedResource, typ string) error {
	return Refuse(classUndeclaredType, "resource %q is of type %q, which its manifest entry does not list (it lists %s)",
		nr.name, typ, strings.Join(nr.declaration.Types, ", "))
}

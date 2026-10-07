package kittest

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
)

// SuppliedResource is a resource the app supplies, which the check only observes and which must stand throughout.
type SuppliedResource interface {
	Observe(ctx context.Context, id contract.Identity) (contract.Observation, error)
}

// DeclaredResources is what a block whose mitstack/v1beta manifest declares its resources supplies to have each one checked.
type DeclaredResources struct {
	Manifest  []byte
	Resources map[string]handler.DeclaredResource
	Supplied  map[string]SuppliedResource
	Desired   contract.Desired
	// Resource drives one declared resource alone, and empty drives every one.
	Resource string
	// ResourceInputs and ResourceIdentity set no Resource-less request: with no Resource named they are one problem and nothing is driven.
	ResourceInputs   map[string]json.RawMessage
	ResourceIdentity map[string]string
}

// CheckDeclaredResources drives each declared resource through ensure twice, observe, delete and the absence proof, and returns one problem per violation.
func CheckDeclaredResources(in DeclaredResources) []string {
	order, problems := pairDeclaredResources(in)
	if len(problems) > 0 {
		return problems
	}

	desired := in.Desired
	if in.ResourceInputs != nil {
		desired.ResourceInputs = in.ResourceInputs
	}
	if in.ResourceIdentity != nil {
		desired.ResourceIdentity = in.ResourceIdentity
	}
	if in.Resource != "" {
		if !slices.Contains(order, in.Resource) {
			return []string{fmt.Sprintf("resource %q: the manifest's spec.resources does not declare it, so there is no resource to drive", in.Resource)}
		}
		order = []string{in.Resource}
		desired.Resource = in.Resource
	}
	if p := unboundResourceFields(desired); p != "" {
		return []string{p}
	}

	c := &declaredCheck{in: in, desired: desired, ctx: context.Background()}
	supplied := sortedKeys(in.Supplied)
	if !c.suppliedStand(supplied, "before the first ensure") {
		return c.problems
	}

	ensured := 0
	for _, name := range order {
		ensured++
		if !c.ensureTwice(name) {
			break
		}
	}
	for i := ensured - 1; i >= 0; i-- {
		if !c.deleteAndProveAbsent(order[i]) {
			break
		}
	}
	c.suppliedStand(supplied, "after the last delete")
	return c.problems
}

// AssertDeclaredResources runs CheckDeclaredResources and fails tb with one error per problem.
func AssertDeclaredResources(tb testing.TB, in DeclaredResources) {
	tb.Helper()
	for _, p := range CheckDeclaredResources(in) {
		tb.Error(p)
	}
}

// unboundResourceFields flags a request contract.DecodeRequest refuses, so the engine never sends one.
func unboundResourceFields(d contract.Desired) string {
	if d.Resource != "" {
		return ""
	}
	var fields []string
	if len(d.ResourceInputs) > 0 {
		fields = append(fields, "ResourceInputs")
	}
	if len(d.ResourceIdentity) > 0 {
		fields = append(fields, "ResourceIdentity")
	}
	switch len(fields) {
	case 0:
		return ""
	case 1:
		return fields[0] + " needs a non-empty Resource naming the resource it is for: the engine never sends it with no resource named, so nothing is driven"
	default:
		return strings.Join(fields, " and ") + " need a non-empty Resource naming the resource they are for: the engine never sends them with no resource named, so nothing is driven"
	}
}

func pairDeclaredResources(in DeclaredResources) ([]string, []string) {
	m, err := contract.ParseManifest(in.Manifest)
	if err != nil {
		return nil, []string{fmt.Sprintf("the manifest is not one the kit's parser accepts, so its resources are unknown: %v", err)}
	}
	if m.Block == nil {
		return nil, []string{fmt.Sprintf("the manifest is %s, but only a %s manifest declares resources under spec.resources, so there are none to drive",
			m.APIVersion, contract.ManifestAPIVersion)}
	}
	if len(m.Block.Spec.Resources) == 0 {
		return nil, []string{"the manifest declares no resources under spec.resources, so the check has nothing to prove"}
	}

	var problems []string
	order := make([]string, 0, len(m.Block.Spec.Resources))
	declared := make(map[string]bool, len(m.Block.Spec.Resources))
	for _, r := range m.Block.Spec.Resources {
		order = append(order, r.Name)
		declared[r.Name] = true
		if in.Resources[r.Name] == nil {
			problems = append(problems, fmt.Sprintf("resource %q: the manifest declares it, but Resources has no handler for it", r.Name))
		}
	}
	for _, name := range sortedKeys(in.Resources) {
		if !declared[name] {
			problems = append(problems, fmt.Sprintf("resource %q: Resources has a handler for it, but the manifest's spec.resources does not declare it", name))
		}
	}
	return order, problems
}

type declaredCheck struct {
	in       DeclaredResources
	desired  contract.Desired
	ctx      context.Context
	problems []string
}

func (c *declaredCheck) resourceProblem(name, format string, args ...any) {
	c.problems = append(c.problems, fmt.Sprintf("resource %q: ", name)+fmt.Sprintf(format, args...))
}

func (c *declaredCheck) suppliedStand(names []string, when string) bool {
	ok := true
	for _, name := range names {
		obs, err := c.in.Supplied[name].Observe(c.ctx, c.desired.Identity)
		switch {
		case err != nil:
			c.problems = append(c.problems, fmt.Sprintf("supplied resource %q: Observe %s failed: %v", name, when, err))
			ok = false
		case obs.State != contract.Present:
			c.problems = append(c.problems, fmt.Sprintf(
				"supplied resource %q: observed %s %s, want present: the app supplies it, so the block must find it standing and never delete it",
				name, obs.State, when))
			ok = false
		}
	}
	return ok
}

func (c *declaredCheck) ensureTwice(name string) bool {
	r := c.in.Resources[name]

	first, firstIdentity, ok := c.ensureAndObserve(name, r, "the first")
	if !ok {
		return false
	}
	second, secondIdentity, ok := c.ensureAndObserve(name, r, "the second")
	if !ok {
		return false
	}

	if changed := changedKeys(firstIdentity, secondIdentity); len(changed) > 0 {
		c.resourceProblem(name, "the second Ensure reported a different identity from the first, changing %s; Ensure must converge the resource it stored, not create another",
			strings.Join(changed, ", "))
	}
	if !reflect.DeepEqual(first.Outputs, second.Outputs) {
		c.resourceProblem(name, "the second Ensure changed its outputs from %v to %v; Ensure must converge, not change what it created",
			first.Outputs, second.Outputs)
	}
	if changed := changedKeys(first.SecretOutputs, second.SecretOutputs); len(changed) > 0 {
		c.resourceProblem(name, "the second Ensure changed its secret outputs %s; Ensure must converge, not change what it created",
			strings.Join(changed, ", "))
	}
	return true
}

func (c *declaredCheck) ensureAndObserve(name string, r handler.DeclaredResource, which string) (contract.Observation, map[string]string, bool) {
	identity, err := c.ensure(r)
	if err != nil {
		c.resourceProblem(name, "%s Ensure failed: %v", which, err)
		return contract.Observation{}, nil, false
	}
	obs, err := c.observe(r)
	if err != nil {
		c.resourceProblem(name, "Observe after %s Ensure failed: %v", which, err)
		return contract.Observation{}, nil, false
	}
	if obs.State != contract.Present {
		c.resourceProblem(name, "observed %s after %s Ensure, want present", obs.State, which)
		return contract.Observation{}, nil, false
	}
	return obs, identity, true
}

// ensure, observe and delete pick the same method the Handler does for each optional interface.
func (c *declaredCheck) ensure(r handler.DeclaredResource) (map[string]string, error) {
	if reporting, ok := r.(handler.ReportingResource); ok {
		report, err := reporting.EnsureWithReport(c.ctx, c.desired)
		return report.Identity, err
	}
	return nil, r.Ensure(c.ctx, c.desired)
}

func (c *declaredCheck) observe(r handler.DeclaredResource) (contract.Observation, error) {
	if ro, ok := r.(handler.RequestObserver); ok {
		return ro.ObserveWithRequest(c.ctx, c.desired)
	}
	return r.Observe(c.ctx, c.desired.Identity)
}

func (c *declaredCheck) delete(r handler.DeclaredResource) error {
	if rd, ok := r.(handler.RequestDeleter); ok {
		return rd.DeleteWithRequest(c.ctx, c.desired)
	}
	return r.Delete(c.ctx, c.desired.Identity)
}

func (c *declaredCheck) deleteAndProveAbsent(name string) bool {
	r := c.in.Resources[name]
	if err := c.delete(r); err != nil {
		c.resourceProblem(name, "Delete failed: %v", err)
		return false
	}
	obs, err := c.observe(r)
	if err != nil {
		c.resourceProblem(name, "Observe after Delete failed, so its absence is unproved: %v", err)
		return false
	}
	if obs.State != contract.Absent {
		c.resourceProblem(name, "observed %s after Delete, want absent: Delete must leave nothing standing", obs.State)
		return false
	}
	return true
}

// changedKeys names keys only, so a secret value never reaches a problem.
func changedKeys(a, b map[string]string) []string {
	var out []string
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			out = append(out, fmt.Sprintf("%q", k))
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, fmt.Sprintf("%q", k))
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

package kittest

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/observe"
)

// Provider is the provider a block creates its resources in, as the check
// reaches it: it lists everything carrying a subscription's id, and loses
// one resource out from under the block.
type Provider interface {
	ListTagged(ctx context.Context, subscriptionID string) ([]observe.Tagged, error)
	Lose(ctx context.Context, r contract.CreatedResource) error
}

// Block is what a block served through one block-level handler, with an
// observer per declared resource, supplies to have its guards checked.
type Block struct {
	// Manifest is the block's mitstack/v1beta manifest.yaml.
	Manifest []byte
	// Types is the platform's type snapshot the manifest's types are read
	// from.
	Types contract.TypeSnapshot
	// Handler is the block-level handler, as handler.Config.Resource holds it.
	Handler handler.Resource
	// Observers holds one observer per declared resource, keyed by its name,
	// as handler.Config.Observers does.
	Observers map[string]handler.Observer
	// Provider is where the block creates its resources.
	Provider Provider
	// Desired is the subscription the check subscribes and unsubscribes.
	Desired contract.Desired
	// Upgrade, when set, is the Desired the check upgrades the subscription
	// to before it unsubscribes.
	Upgrade *contract.Desired
	// Requirements holds, by requirement reference and then by type, the
	// outputs a resource of that type offers the requirement, bound on the
	// Desired of the run that meets the requirement with that type.
	Requirements map[string]map[string]map[string]string
}

// CheckBlock proves the four guards a block served through one block-level
// handler holds, and returns one problem per violation, empty when the
// block conforms. It subscribes Desired and observes every declared
// resource present, then:
//
//   - lists everything the Provider holds tagged with the subscription's id,
//     each of which must match a declared resource by type and count;
//   - with Upgrade set, asks the handler's PlanReplacements about Upgrade,
//     upgrades, and refuses a resource whose identity changed that the plan
//     did not name, saying so when its type is stateful;
//   - loses each resource with an identity from the Provider, which its
//     observe must then report not present, and ensures again;
//   - unsubscribes, after which every declared resource must observe absent.
//
// A requirement listing more than one type runs the whole check once per
// listed type, its RequirementTypes and Bindings set for that type, and
// each problem of such a run names the requirement and the type. Each
// problem about a resource begins `resource "<name>"`.
func CheckBlock(in Block) []string {
	spec, problems := blockSpec(in)
	if len(problems) > 0 {
		return problems
	}
	for _, run := range blockRuns(spec, in) {
		c := &blockCheck{in: in, spec: spec, label: run.label, desired: run.desired, ctx: context.Background()}
		c.run()
		problems = append(problems, c.problems...)
	}
	return problems
}

// AssertBlock runs CheckBlock and fails tb with one error per problem, in
// the order the check found them.
func AssertBlock(tb testing.TB, in Block) {
	tb.Helper()
	for _, p := range CheckBlock(in) {
		tb.Error(p)
	}
}

// blockSpec reads the manifest's spec, or the problems that leave the check
// nothing it may drive.
func blockSpec(in Block) (contract.ManifestSpec, []string) {
	m, err := contract.ParseManifest(in.Manifest)
	if err != nil {
		return contract.ManifestSpec{}, []string{fmt.Sprintf("the manifest is not one the kit's parser accepts, so its resources are unknown: %v", err)}
	}
	if m.Block == nil {
		return contract.ManifestSpec{}, []string{fmt.Sprintf("the manifest is %s, but only a %s manifest declares resources", m.APIVersion, contract.ManifestAPIVersion)}
	}
	spec := m.Block.Spec
	if err := contract.CheckManifestTypes(spec, in.Types); err != nil {
		return spec, []string{err.Error()}
	}
	var problems []string
	if in.Handler == nil || in.Provider == nil {
		problems = append(problems, "Handler and Provider are both required, so the guards cannot run")
	}
	declared := make(map[string]bool, len(spec.Resources))
	for _, r := range spec.Resources {
		declared[r.Name] = true
		if in.Observers[r.Name] == nil {
			problems = append(problems, fmt.Sprintf("resource %q: the manifest declares it, but Observers has no observer for it", r.Name))
		}
	}
	for _, name := range sortedKeys(in.Observers) {
		if !declared[name] {
			problems = append(problems, fmt.Sprintf("resource %q: Observers has an observer for it, but the manifest's spec.resources does not declare it", name))
		}
	}
	return spec, problems
}

// blockRun is one pass of the check: the Desired it subscribes, and the
// label naming the requirement type it runs for, empty for the only run.
type blockRun struct {
	label   string
	desired contract.Desired
}

// blockRuns returns one run per listed type of each requirement listing
// more than one, every other requirement met by the first type it lists,
// or the one run when no requirement lists more than one.
func blockRuns(spec contract.ManifestSpec, in Block) []blockRun {
	base := make(map[string]string, len(spec.Requires))
	for _, q := range spec.Requires {
		base[q.Reference()] = q.Types[0]
	}
	var runs []blockRun
	for _, q := range spec.Requires {
		if len(q.Types) < 2 {
			continue
		}
		for _, typ := range q.Types {
			types := maps.Clone(base)
			types[q.Reference()] = typ
			runs = append(runs, blockRun{
				label:   fmt.Sprintf(" (requires.%s met by %s)", q.Reference(), typ),
				desired: requirementDesired(in, types),
			})
		}
	}
	if len(runs) == 0 {
		runs = append(runs, blockRun{desired: requirementDesired(in, base)})
	}
	return runs
}

// requirementDesired is in.Desired with each requirement met by the type
// types names for it, and bound to the outputs Requirements gives that
// type.
func requirementDesired(in Block, types map[string]string) contract.Desired {
	d := in.Desired
	if len(types) == 0 {
		return d
	}
	d.RequirementTypes = maps.Clone(d.RequirementTypes)
	if d.RequirementTypes == nil {
		d.RequirementTypes = map[string]string{}
	}
	d.Bindings = maps.Clone(d.Bindings)
	if d.Bindings == nil {
		d.Bindings = map[string]map[string]string{}
	}
	for ref, typ := range types {
		d.RequirementTypes[ref] = typ
		if outputs, ok := in.Requirements[ref][typ]; ok {
			d.Bindings[ref] = outputs
		}
	}
	return d
}

// blockCheck is one run of CheckBlock, collecting problems. refs holds what
// each declared resource's observer is handed.
type blockCheck struct {
	in       Block
	spec     contract.ManifestSpec
	label    string
	desired  contract.Desired
	ctx      context.Context
	refs     map[string]contract.ResourceRef
	problems []string
}

func (c *blockCheck) problem(format string, args ...any) {
	c.problems = append(c.problems, fmt.Sprintf(format, args...)+c.label)
}

func (c *blockCheck) resourceProblem(name, format string, args ...any) {
	c.problems = append(c.problems, fmt.Sprintf("resource %q%s: ", name, c.label)+fmt.Sprintf(format, args...))
}

func (c *blockCheck) run() {
	if !c.ensure("subscribe") || !c.allObserved(contract.Present, "after subscribe") {
		return
	}
	c.taggedMatchDeclared()
	if c.in.Upgrade != nil && !c.upgrade() {
		return
	}
	if !c.observesTheProvider() {
		return
	}
	c.unsubscribe()
}

// ensure runs the block-level ensure for c.desired and sets each resource's
// ref from what it reported, refusing a reported resource the manifest does
// not declare or of a type its entry does not list.
func (c *blockCheck) ensure(verb string) bool {
	var created []contract.CreatedResource
	var err error
	if reporter, ok := c.in.Handler.(handler.ResourceReporter); ok {
		created, err = reporter.EnsureResources(c.ctx, c.desired)
	} else {
		err = c.in.Handler.Ensure(c.ctx, c.desired)
	}
	if err != nil {
		c.problem("the block-level ensure on %s failed: %v", verb, err)
		return false
	}
	reported := make(map[string]contract.CreatedResource, len(created))
	for _, r := range created {
		entry, ok := c.entry(r.Name)
		switch {
		case !ok:
			c.resourceProblem(r.Name, "the ensure on %s reported it as %s, but the manifest's spec.resources does not declare it", verb, r.Type)
			return false
		case !slices.Contains(entry.Types, r.Type):
			c.resourceProblem(r.Name, "the ensure on %s reported it as %s, a type its entry does not list", verb, r.Type)
			return false
		}
		reported[r.Name] = r
	}
	prior := c.refs
	c.refs = make(map[string]contract.ResourceRef, len(c.spec.Resources))
	recorded := make(map[string]contract.RecordedResource, len(c.spec.Resources))
	for _, entry := range c.spec.Resources {
		ref := contract.ResourceRef{Name: entry.Name, Type: entry.Type}
		if p, ok := prior[entry.Name]; ok {
			ref = p
		}
		if r, ok := reported[entry.Name]; ok {
			ref.Type, ref.Identity = r.Type, r.Identity
		}
		c.refs[entry.Name] = ref
		recorded[entry.Name] = contract.RecordedResource{Type: ref.Type, Identity: ref.Identity}
	}
	c.desired.RecordedResources = recorded
	return true
}

// entry returns the manifest's declaration of the resource called name.
func (c *blockCheck) entry(name string) (contract.ManifestResource, bool) {
	i := slices.IndexFunc(c.spec.Resources, func(r contract.ManifestResource) bool { return r.Name == name })
	if i < 0 {
		return contract.ManifestResource{}, false
	}
	return c.spec.Resources[i], true
}

// observe observes one declared resource through its observer, handed its
// ref and the check's Desired.
func (c *blockCheck) observe(name string) (contract.Observation, error) {
	ref := c.refs[name]
	ref.Desired = c.desired
	return c.in.Observers[name].ObserveResource(c.ctx, ref)
}

// allObserved observes every declared resource once, recording a problem
// for each not in want, and reports whether all were.
func (c *blockCheck) allObserved(want contract.State, when string) bool {
	ok := true
	for _, entry := range c.spec.Resources {
		obs, err := c.observe(entry.Name)
		switch {
		case err != nil:
			c.resourceProblem(entry.Name, "observe %s failed: %v", when, err)
			ok = false
		case obs.State != want:
			c.resourceProblem(entry.Name, "observed %s %s, want %s", obs.State, when, want)
			ok = false
		}
	}
	return ok
}

// taggedMatchDeclared lists what the provider holds tagged with the
// subscription's id, which must match the declared resources by type and
// count.
func (c *blockCheck) taggedMatchDeclared() {
	tagged, err := c.in.Provider.ListTagged(c.ctx, c.desired.SubscriptionID)
	if err != nil {
		c.problem("listing what carries the subscription's id failed: %v", err)
		return
	}
	room := map[string]int{}
	for _, ref := range c.refs {
		room[ref.Type]++
	}
	slices.SortFunc(tagged, func(a, b observe.Tagged) int { return strings.Compare(a.ID, b.ID) })
	for _, t := range tagged {
		switch {
		case t.Type == "":
			c.resourceProblem(t.ID, "it carries the subscription's id, but is of no type the provider's lister knows, so no declared resource matches it")
		case room[t.Type] == 0:
			c.resourceProblem(t.ID, "it carries the subscription's id as type %s, but the manifest declares no further resource of that type", t.Type)
		default:
			room[t.Type]--
		}
	}
}

// upgrade asks PlanReplacements about the upgrade, upgrades, and refuses a
// resource whose identity changed that the plan did not name.
func (c *blockCheck) upgrade() bool {
	var replaces []string
	if planner, ok := c.in.Handler.(handler.ReplacementPlanner); ok {
		var err error
		if replaces, err = planner.PlanReplacements(c.ctx, *c.in.Upgrade); err != nil {
			c.problem("PlanReplacements for the upgrade failed: %v", err)
			return false
		}
	}
	for _, name := range replaces {
		if _, ok := c.entry(name); !ok {
			c.resourceProblem(name, "PlanReplacements names it, but the manifest's spec.resources does not declare it")
		}
	}
	before := c.refs
	types := c.desired.RequirementTypes
	bindings := c.desired.Bindings
	recorded := c.desired.RecordedResources
	c.desired = *c.in.Upgrade
	c.desired.RequirementTypes, c.desired.Bindings, c.desired.RecordedResources = types, bindings, recorded
	if !c.ensure("upgrade") {
		return false
	}
	for _, entry := range c.spec.Resources {
		was, is := before[entry.Name], c.refs[entry.Name]
		if was.Identity == nil || maps.Equal(was.Identity, is.Identity) || slices.Contains(replaces, entry.Name) {
			continue
		}
		if t, ok := c.in.Types.Lookup(is.Type); ok && t.Stateful {
			c.resourceProblem(entry.Name, "the upgrade replaced it, but validate's PlanReplacements did not name it; its type %s is stateful, so the replacement loses its data unannounced", is.Type)
		} else {
			c.resourceProblem(entry.Name, "the upgrade replaced it, but validate's PlanReplacements did not name it")
		}
	}
	return c.allObserved(contract.Present, "after upgrade")
}

// observesTheProvider loses each resource with an identity from the
// provider, which its observe must then report not present, and ensures
// again so the teardown has the block whole.
func (c *blockCheck) observesTheProvider() bool {
	for _, entry := range c.spec.Resources {
		ref := c.refs[entry.Name]
		if len(ref.Identity) == 0 {
			continue
		}
		if err := c.in.Provider.Lose(c.ctx, contract.CreatedResource{Name: ref.Name, Type: ref.Type, Identity: ref.Identity}); err != nil {
			c.resourceProblem(entry.Name, "the provider could not lose it: %v", err)
			return false
		}
		obs, err := c.observe(entry.Name)
		switch {
		case err != nil:
			c.resourceProblem(entry.Name, "observe after the provider lost it failed: %v", err)
		case obs.State == contract.Present:
			c.resourceProblem(entry.Name, "still observed present after the provider lost it; observe must ask the provider, not remember what the ensure created")
		}
	}
	return c.ensure("recreate") && c.allObserved(contract.Present, "after the ensure that recreated them")
}

// unsubscribe runs the block-level delete, after which every declared
// resource must observe absent.
func (c *blockCheck) unsubscribe() {
	var err error
	if rd, ok := c.in.Handler.(handler.RequestDeleter); ok {
		err = rd.DeleteWithRequest(c.ctx, c.desired)
	} else {
		err = c.in.Handler.Delete(c.ctx, c.desired.Identity)
	}
	if err != nil {
		c.problem("the block-level delete on unsubscribe failed: %v", err)
		return
	}
	c.allObserved(contract.Absent, "after unsubscribe")
}

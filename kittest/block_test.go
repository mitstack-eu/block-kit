package kittest_test

// block_test.go pins kittest.CheckBlock, which proves the four guards ADR
// 0085 sets for a block served through one block-level handler with an
// observer per declared resource:
//
//   - after subscribe, everything carrying the subscription's tag matches a
//     declared resource by type and count;
//   - after unsubscribe, every declared resource observes absent;
//   - a resource the provider loses observes not present, so observe asks
//     the provider;
//   - an upgrade that changes a resource's identity is named by
//     PlanReplacements, a stateful type's especially.
//
// No guard is skipped silently: a resource with no identity, or a stateful
// type with no Upgrade to prove its replacement, is a problem. A provided
// resource observed present must carry every output its type marks
// required in the snapshot.
//
// A requirement listing types runs the whole check once per listed type,
// and each problem names that type. Every problem names its resource as
// resource "<name>".

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/kittest"
	"github.com/mitstack-eu/operator-kit/observe"
)

const hostBlockManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: host-block
  name: Host Block
spec:
  version: 1.0.0
  resources:
    - aws.ec2-instance
    - aws.ebs-volume
  provides: [aws.ec2-instance]
  lifecycle:
    endpoint: https://host-block.example/hook
    operations: [subscribe, unsubscribe, upgrade, validate]
    contract: kit/v1
`

const containerBlockManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: container-block
  name: Container Block
spec:
  version: 1.0.0
  requires:
    - { name: host, type: [aws.ec2-instance, hetzner.server] }
  resources:
    - { name: app-container, type: [aws.ec2-container, hetzner.server-container] }
  provides: [app-container]
  lifecycle:
    endpoint: https://container-block.example/hook
    operations: [subscribe, unsubscribe, upgrade, validate]
    contract: kit/v1
`

func typeSnapshot(t *testing.T) contract.TypeSnapshot {
	t.Helper()
	raw, err := os.ReadFile("../contract/testdata/type-snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := contract.ParseTypeSnapshot(raw)
	if err != nil {
		t.Fatalf("ParseTypeSnapshot: %v", err)
	}
	return snap
}

// fakeProvider is the provider's own record: each object by id, with its
// type and the subscription it is tagged with.
type fakeProvider struct {
	mu    sync.Mutex
	items map[string]observe.Tagged
	tags  map[string]string
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{items: map[string]observe.Tagged{}, tags: map[string]string{}}
}

func (p *fakeProvider) put(id, typ, sub string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.items[id] = observe.Tagged{Type: typ, ID: id}
	p.tags[id] = sub
}

func (p *fakeProvider) remove(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.items, id)
	delete(p.tags, id)
}

func (p *fakeProvider) holds(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.items[id]
	return ok
}

func (p *fakeProvider) ListTagged(_ context.Context, subscriptionID string) ([]observe.Tagged, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []observe.Tagged
	for id, item := range p.items {
		if p.tags[id] == subscriptionID {
			out = append(out, item)
		}
	}
	return out, nil
}

func (p *fakeProvider) Lose(_ context.Context, r contract.CreatedResource) error {
	p.remove(r.Identity["id"])
	return nil
}

var _ kittest.Provider = (*fakeProvider)(nil)

// fakeBlock is a block-level handler over the fake provider. It creates one
// object per declared resource, tagged with the subscription, and replaces
// its volume when the volumeSize param changes. Each misbehaviour field
// breaks one guard.
type fakeBlock struct {
	p       *fakeProvider
	mu      sync.Mutex
	seq     int
	created map[string]contract.CreatedResource
	size    string

	// container makes it the container block, creating app-container as
	// the container type that runs on the host type that met requires.host.
	container bool

	extraTagged     bool
	leaveOnDelete   string
	hideReplacement bool
	failOnHostType  string
}

func (b *fakeBlock) newID(prefix string) string {
	b.seq++
	return fmt.Sprintf("%s-%d", prefix, b.seq)
}

func (b *fakeBlock) wanted(d contract.Desired) map[string]string {
	if !b.container {
		return map[string]string{"aws.ec2-instance": "aws.ec2-instance", "aws.ebs-volume": "aws.ebs-volume"}
	}
	if d.RequirementTypes["host"] == "hetzner.server" {
		return map[string]string{"app-container": "hetzner.server-container"}
	}
	return map[string]string{"app-container": "aws.ec2-container"}
}

func (b *fakeBlock) EnsureResources(ctx context.Context, d contract.Desired) ([]contract.CreatedResource, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.container && d.Bindings["host"]["hostId"] == "" {
		return nil, fmt.Errorf("no hostId bound for requires.host")
	}
	if b.created == nil {
		b.created = map[string]contract.CreatedResource{}
	}
	var out []contract.CreatedResource
	for name, typ := range b.wanted(d) {
		prior, had := b.created[name]
		keep := had && b.p.holds(prior.Identity["id"])
		if name == "aws.ebs-volume" && had && b.size != d.Params["volumeSize"] {
			b.p.remove(prior.Identity["id"])
			keep = false
		}
		r := prior
		if !keep {
			r = contract.CreatedResource{Name: name, Type: typ, Identity: map[string]string{"id": b.newID(typ)}}
			b.p.put(r.Identity["id"], typ, d.SubscriptionID)
		}
		b.created[name] = r
		out = append(out, r)
	}
	b.size = d.Params["volumeSize"]
	if b.extraTagged {
		b.p.put("state-bucket", "aws.s3-bucket", d.SubscriptionID)
	}
	return out, nil
}

func (b *fakeBlock) Ensure(ctx context.Context, d contract.Desired) error {
	_, err := b.EnsureResources(ctx, d)
	return err
}

func (b *fakeBlock) PlanReplacements(ctx context.Context, d contract.Desired) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, had := b.created["aws.ebs-volume"]; had && !b.hideReplacement && b.size != d.Params["volumeSize"] {
		return []string{"aws.ebs-volume"}, nil
	}
	return nil, nil
}

func (b *fakeBlock) Observe(ctx context.Context, id contract.Identity) (contract.Observation, error) {
	return contract.Observation{State: contract.Unknown}, nil
}

func (b *fakeBlock) Delete(ctx context.Context, id contract.Identity) error {
	return b.DeleteWithRequest(ctx, contract.Desired{Identity: id})
}

func (b *fakeBlock) DeleteWithRequest(ctx context.Context, d contract.Desired) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for name, r := range b.created {
		if name == b.leaveOnDelete || (b.failOnHostType != "" && d.RequirementTypes["host"] == b.failOnHostType) {
			continue
		}
		b.p.remove(r.Identity["id"])
		delete(b.created, name)
	}
	b.p.remove("state-bucket")
	return nil
}

var (
	_ handler.ResourceReporter   = (*fakeBlock)(nil)
	_ handler.ReplacementPlanner = (*fakeBlock)(nil)
	_ handler.RequestDeleter     = (*fakeBlock)(nil)
)

// providerObserver asks the provider for the object the identity names,
// and reports the outputs a host offers, less drop.
type providerObserver struct {
	p    *fakeProvider
	drop string
}

func (o providerObserver) ObserveResource(_ context.Context, r contract.ResourceRef) (contract.Observation, error) {
	id := r.Identity["id"]
	switch {
	case id == "":
		return contract.Observation{State: contract.Unknown, StatusDetail: "no id"}, nil
	case o.p.holds(id):
		return contract.Observation{State: contract.Present, Outputs: o.outputs(r.Type, id)}, nil
	}
	return contract.Observation{State: contract.Absent}, nil
}

func (o providerObserver) outputs(typ, id string) map[string]string {
	if typ != "aws.ec2-instance" {
		return nil
	}
	out := map[string]string{"hostId": id, "execKind": "ssm", "execTarget": id, "publicIp": "203.0.113.7"}
	delete(out, o.drop)
	return out
}

// byTypeObserver finds a resource by its type among what the provider holds
// for the subscription, so it needs no identity.
type byTypeObserver struct{ p *fakeProvider }

func (o byTypeObserver) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	tagged, _ := o.p.ListTagged(ctx, r.Desired.SubscriptionID)
	for _, item := range tagged {
		if item.Type == r.Type {
			return contract.Observation{State: contract.Present, Outputs: providerObserver{}.outputs(item.Type, item.ID)}, nil
		}
	}
	return contract.Observation{State: contract.Absent}, nil
}

// silentBlock is a fakeBlock that reports nothing it created: it is served
// through Ensure alone.
type silentBlock struct{ b *fakeBlock }

func (s silentBlock) Ensure(ctx context.Context, d contract.Desired) error { return s.b.Ensure(ctx, d) }
func (s silentBlock) Observe(ctx context.Context, id contract.Identity) (contract.Observation, error) {
	return s.b.Observe(ctx, id)
}
func (s silentBlock) Delete(ctx context.Context, id contract.Identity) error {
	return s.b.Delete(ctx, id)
}

// rememberingObserver answers from what the block remembers creating,
// never asking the provider.
type rememberingObserver struct {
	b    *fakeBlock
	name string
}

func (o rememberingObserver) ObserveResource(_ context.Context, r contract.ResourceRef) (contract.Observation, error) {
	o.b.mu.Lock()
	defer o.b.mu.Unlock()
	if _, ok := o.b.created[o.name]; ok {
		return contract.Observation{State: contract.Present}, nil
	}
	return contract.Observation{State: contract.Absent}, nil
}

func blockDesired(volumeSize string) contract.Desired {
	return contract.Desired{
		Identity: contract.Identity{SubscriptionID: "sub-block", BlockID: "host-block", OrgID: "org-1"},
		Params:   map[string]string{"volumeSize": volumeSize},
	}
}

// hostBlock returns a converging host block and the check over it, with an
// upgrade that grows the volume.
func hostBlock(t *testing.T) (*fakeBlock, kittest.Block) {
	t.Helper()
	p := newFakeProvider()
	b := &fakeBlock{p: p}
	upgrade := blockDesired("20")
	return b, kittest.Block{
		Manifest: []byte(hostBlockManifest),
		Types:    typeSnapshot(t),
		Handler:  b,
		Observers: map[string]handler.Observer{
			"aws.ec2-instance": providerObserver{p: p},
			"aws.ebs-volume":   providerObserver{p: p},
		},
		Provider: p,
		Desired:  blockDesired("10"),
		Upgrade:  &upgrade,
	}
}

func wantNoProblems(t *testing.T, problems []string) {
	t.Helper()
	if len(problems) != 0 {
		t.Errorf("CheckBlock found problems in a conforming block: %q", problems)
	}
}

// wantProblemNaming fails unless one problem carries every one of wants.
func wantProblemNaming(t *testing.T, problems []string, wants ...string) {
	t.Helper()
	for _, p := range problems {
		hit := true
		for _, w := range wants {
			if !strings.Contains(p, w) {
				hit = false
			}
		}
		if hit {
			return
		}
	}
	t.Errorf("CheckBlock problems %q, want one naming each of %q", problems, wants)
}

func TestCheckBlock_PassesABlockThatHoldsEveryGuard(t *testing.T) {
	_, in := hostBlock(t)
	wantNoProblems(t, kittest.CheckBlock(in))
}

func TestCheckBlock_FailsAnObserveThatStillReportsAResourceTheProviderLost(t *testing.T) {
	b, in := hostBlock(t)
	in.Observers["aws.ec2-instance"] = rememberingObserver{b: b, name: "aws.ec2-instance"}
	wantProblemNaming(t, kittest.CheckBlock(in), `resource "aws.ec2-instance"`, "provider")
}

func TestCheckBlock_FailsATaggedResourceTheManifestDoesNotDeclare(t *testing.T) {
	b, in := hostBlock(t)
	b.extraTagged = true
	wantProblemNaming(t, kittest.CheckBlock(in), `resource "state-bucket"`, "aws.s3-bucket")
}

func TestCheckBlock_FailsADeclaredResourceLeftPresentAfterUnsubscribe(t *testing.T) {
	b, in := hostBlock(t)
	b.leaveOnDelete = "aws.ebs-volume"
	wantProblemNaming(t, kittest.CheckBlock(in), `resource "aws.ebs-volume"`, "unsubscribe")
}

func TestCheckBlock_FailsAValidateThatHidesTheReplacementOfAStatefulResource(t *testing.T) {
	b, in := hostBlock(t)
	b.hideReplacement = true
	wantProblemNaming(t, kittest.CheckBlock(in), `resource "aws.ebs-volume"`, "stateful")
}

// containerBlock returns the container block, whose requirement lists both
// host types, and the check over it.
func containerBlock(t *testing.T) (*fakeBlock, kittest.Block) {
	t.Helper()
	p := newFakeProvider()
	b := &fakeBlock{p: p, container: true}
	return b, kittest.Block{
		Manifest:  []byte(containerBlockManifest),
		Types:     typeSnapshot(t),
		Handler:   b,
		Observers: map[string]handler.Observer{"app-container": providerObserver{p: p}},
		Provider:  p,
		Desired:   blockDesired(""),
		Requirements: map[string]map[string]map[string]string{
			"host": {
				"aws.ec2-instance": {"hostId": "i-0host", "execKind": "ssm", "execTarget": "i-0host"},
				"hetzner.server":   {"hostId": "4711", "execKind": "ssh", "execTarget": "198.51.100.9"},
			},
		},
	}
}

func TestCheckBlock_RunsOncePerListedTypeAndPasses(t *testing.T) {
	_, in := containerBlock(t)
	wantNoProblems(t, kittest.CheckBlock(in))
}

func TestCheckBlock_NamesTheListedTypeWhoseRunFails(t *testing.T) {
	b, in := containerBlock(t)
	b.failOnHostType = "hetzner.server"
	problems := kittest.CheckBlock(in)
	wantProblemNaming(t, problems, `resource "app-container"`)
	for _, p := range problems {
		if !strings.Contains(strings.ReplaceAll(p, "hetzner.server-container", ""), "hetzner.server") {
			t.Errorf("problem %q does not name hetzner.server, the requirement type whose run failed", p)
		}
		if strings.Contains(p, "aws.ec2-instance") {
			t.Errorf("problem %q names aws.ec2-instance, whose run passes", p)
		}
	}
}

// AssertBlock fails the test with every problem CheckBlock finds. It may
// also log which guards ran, which blockverify reports.
func TestAssertBlock_FailsTheTestWithEveryProblem(t *testing.T) {
	b, in := hostBlock(t)
	b.leaveOnDelete = "aws.ebs-volume"
	problems := kittest.CheckBlock(in)
	if len(problems) == 0 {
		t.Fatal("CheckBlock found no problem, so AssertBlock has nothing to report")
	}
	b2, in2 := hostBlock(t)
	b2.leaveOnDelete = "aws.ebs-volume"
	tb := runAndCollect(func(tb testing.TB) { kittest.AssertBlock(tb, in2) })
	if !tb.failed {
		t.Fatal("AssertBlock did not fail the test")
	}
	for _, p := range problems {
		if !slices.Contains(tb.logs, p) {
			t.Errorf("AssertBlock reported %q, want it to carry the problem %q", tb.logs, p)
		}
	}
}

// --- no guard is skipped silently ------------------------------------------------

// A block that reports no identity leaves the lost-resource probe nothing
// to lose, so the check says the block must report each one rather than
// passing the guard untried.
func TestCheckBlock_FailsADeclaredResourceWithNoIdentityAfterSubscribe(t *testing.T) {
	b, in := hostBlock(t)
	in.Handler = silentBlock{b: b}
	in.Observers = map[string]handler.Observer{
		"aws.ec2-instance": byTypeObserver{p: b.p},
		"aws.ebs-volume":   byTypeObserver{p: b.p},
	}
	in.Upgrade = nil
	problems := kittest.CheckBlock(in)
	wantProblemNaming(t, problems, `resource "aws.ec2-instance"`, "identity")
	wantProblemNaming(t, problems, `resource "aws.ebs-volume"`, "identity")
}

// aws.ebs-volume is stateful in the snapshot, so its replacement guard
// must run, and it cannot without an upgrade to run.
func TestCheckBlock_FailsAStatefulResourceWithNoUpgradeToProveItsReplacement(t *testing.T) {
	_, in := hostBlock(t)
	in.Upgrade = nil
	wantProblemNaming(t, kittest.CheckBlock(in), `resource "aws.ebs-volume"`, "Upgrade")
}

// --- provided outputs ------------------------------------------------------------------

// The platform refuses a provided resource missing an output its type marks
// required, so the check does too, naming the resource and the output.
func TestCheckBlock_FailsAProvidedResourceMissingARequiredOutput(t *testing.T) {
	b, in := hostBlock(t)
	in.Observers["aws.ec2-instance"] = providerObserver{p: b.p, drop: "hostId"}
	wantProblemNaming(t, kittest.CheckBlock(in), `resource "aws.ec2-instance"`, "hostId")
}

// An optional output may be missing, and a resource the block keeps to
// itself is held to no output.
func TestCheckBlock_PassesAProvidedResourceMissingOnlyAnOptionalOutput(t *testing.T) {
	b, in := hostBlock(t)
	in.Observers["aws.ec2-instance"] = providerObserver{p: b.p, drop: "publicIp"}
	wantNoProblems(t, kittest.CheckBlock(in))
}

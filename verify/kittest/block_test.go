package kittest_test

// Each fakeBlock misbehaviour breaks one of ADR 0085's guards, and every problem names its resource as resource "<name>".

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/capabilities/github/landing"
	"github.com/mitstack-eu/block-kit/capabilities/github/landing/landingtest"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/helpers/events"
	"github.com/mitstack-eu/block-kit/observe"
	"github.com/mitstack-eu/block-kit/verify/kittest"
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

const optionalHostBlockManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: container-block
  name: Container Block
spec:
  version: 1.0.0
  requires:
    - { name: host, type: hetzner.server, optional: true }
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
	raw, err := os.ReadFile("../../contract/testdata/type-snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := contract.ParseTypeSnapshot(raw)
	if err != nil {
		t.Fatalf("ParseTypeSnapshot: %v", err)
	}
	return snap
}

type fakeProvider struct {
	mu    sync.Mutex
	items map[string]observe.Tagged
	tags  map[string]string
	// listPrefix prefixes every id it lists, like a lister that names an object by none of its identity's values.
	listPrefix string
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
			out = append(out, observe.Tagged{Type: item.Type, ID: p.listPrefix + item.ID})
		}
	}
	return out, nil
}

func (p *fakeProvider) Lose(_ context.Context, r contract.CreatedResource) error {
	p.remove(r.Identity["id"])
	return nil
}

var _ kittest.Provider = (*fakeProvider)(nil)

// fakeBlock replaces its volume when the volumeSize param changes.
type fakeBlock struct {
	p         *fakeProvider
	mu        sync.Mutex
	seq       int
	created   map[string]contract.CreatedResource
	size      string
	container bool
	// resources, when set, replaces the host block's resources by name and type.
	resources map[string]string
	// parts are tagged beside the declared resources on every ensure and removed on delete.
	parts []observe.Tagged

	extraTagged     bool
	leaveOnDelete   string
	hideReplacement bool
	failOnHostType  string
	// hostOptional lets a container ensure run with no host bound, and ensured holds every Desired an ensure was handed.
	hostOptional bool
	ensured      []contract.Desired
	// untagged names the resource its ensure creates without the subscription's tag.
	untagged string

	counts *observeCounts
}

// observeCounts keeps the most observes any one verb made of each resource, a verb beginning at the block-level call the handler makes before it observes.
type observeCounts struct {
	mu   sync.Mutex
	verb map[string]int
	most map[string]int
}

func (c *observeCounts) begin() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.verb = map[string]int{}
}

func (c *observeCounts) observed(name string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.verb[name]++
	c.most[name] = max(c.most[name], c.verb[name])
}

func (b *fakeBlock) newID(prefix string) string {
	b.seq++
	return fmt.Sprintf("%s-%d", prefix, b.seq)
}

func (b *fakeBlock) wanted(d contract.Desired) map[string]string {
	if b.resources != nil {
		return b.resources
	}
	if !b.container {
		return map[string]string{"aws.ec2-instance": "aws.ec2-instance", "aws.ebs-volume": "aws.ebs-volume"}
	}
	if d.RequirementTypes["host"] == "hetzner.server" {
		return map[string]string{"app-container": "hetzner.server-container"}
	}
	return map[string]string{"app-container": "aws.ec2-container"}
}

func (b *fakeBlock) EnsureResources(ctx context.Context, d contract.Desired) ([]contract.CreatedResource, error) {
	b.counts.begin()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensured = append(b.ensured, d)
	if b.container && !b.hostOptional && d.Bindings["host"]["hostId"] == "" {
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
			sub := d.SubscriptionID
			if name == b.untagged {
				sub = ""
			}
			b.p.put(r.Identity["id"], typ, sub)
		}
		b.created[name] = r
		out = append(out, r)
	}
	b.size = d.Params["volumeSize"]
	for _, part := range b.parts {
		b.p.put(part.ID, part.Type, d.SubscriptionID)
	}
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
	b.counts.begin()
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, had := b.created["aws.ebs-volume"]; had && !b.hideReplacement && b.size != d.Params["volumeSize"] {
		return []string{"aws.ebs-volume"}, nil
	}
	return nil, nil
}

func (b *fakeBlock) Delete(ctx context.Context, d contract.Desired) (map[string]string, error) {
	b.counts.begin()
	b.mu.Lock()
	defer b.mu.Unlock()
	for name, r := range b.created {
		if name == b.leaveOnDelete || (b.failOnHostType != "" && d.RequirementTypes["host"] == b.failOnHostType) {
			continue
		}
		b.p.remove(r.Identity["id"])
		delete(b.created, name)
	}
	for _, part := range b.parts {
		b.p.remove(part.ID)
	}
	b.p.remove("state-bucket")
	return nil, nil
}

var (
	_ handler.ResourceReporter   = (*fakeBlock)(nil)
	_ handler.ReplacementPlanner = (*fakeBlock)(nil)
)

type providerObserver struct {
	p      *fakeProvider
	drop   string
	counts *observeCounts
}

func (o providerObserver) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	id := r.Identity["id"]
	if id != "" || !r.Unsubscribing {
		o.counts.observed(r.Name)
	}
	switch {
	case id == "" && r.Unsubscribing:
		return o.byType(ctx, r)
	case id == "":
		return contract.Observation{State: contract.Unknown, StatusDetail: "no id"}, nil
	case o.p.holds(id):
		return contract.Observation{State: contract.Present, Outputs: o.outputs(r.Type, id)}, nil
	}
	return contract.Observation{State: contract.Absent}, nil
}

// byType answers an unsubscribing observe that names no identity from what the provider holds for the subscription.
func (o providerObserver) byType(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	held, err := o.p.heldOfType(ctx, r)
	if err != nil || len(held) == 0 {
		return contract.Observation{State: contract.Absent}, err
	}
	return contract.Observation{State: contract.Present, Outputs: o.outputs(r.Type, held[0])}, nil
}

// heldOfType lists the ids the provider holds of r's type under r's subscription.
func (p *fakeProvider) heldOfType(ctx context.Context, r contract.ResourceRef) ([]string, error) {
	tagged, err := p.ListTagged(ctx, r.Desired.SubscriptionID)
	var held []string
	for _, item := range tagged {
		if item.Type == r.Type {
			held = append(held, item.ID)
		}
	}
	return held, err
}

func (o providerObserver) outputs(typ, id string) map[string]string {
	switch typ {
	case "aws.ecr-repository":
		return map[string]string{"registryHost": "123456789012.dkr.ecr.eu-central-1.amazonaws.com"}
	case "mitstack.app-hostname":
		return map[string]string{"appHostname": "myapp.apps.mitstack.dev", "address": "203.0.113.7", "appUrl": "https://myapp.apps.mitstack.dev"}
	case "aws.ec2-instance":
		out := map[string]string{"hostId": id, "execKind": "ssm", "execTarget": id, "publicIp": "203.0.113.7"}
		delete(out, o.drop)
		return out
	}
	return nil
}

// byTypeObserver needs no identity, so a block reporting none still observes present; it answers unknown while inFlight counts observes left, and with report set reports the identity it found.
type byTypeObserver struct {
	p        *fakeProvider
	report   bool
	inFlight *int
}

func (o byTypeObserver) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	if o.inFlight != nil && *o.inFlight > 0 {
		*o.inFlight--
		return contract.Observation{State: contract.Unknown, StatusDetail: "the block's work is in flight"}, nil
	}
	tagged, _ := o.p.ListTagged(ctx, r.Desired.SubscriptionID)
	for _, item := range tagged {
		if item.Type == r.Type {
			obs := contract.Observation{State: contract.Present, Outputs: providerObserver{}.outputs(item.Type, item.ID)}
			if o.report {
				obs.Identity = map[string]string{"id": item.ID}
			}
			return obs, nil
		}
	}
	return contract.Observation{State: contract.Absent}, nil
}

// waitingOnceObserver answers its first observe unknown, waiting on a person, and every later one as its inner observer does.
type waitingOnceObserver struct {
	inner    handler.Observer
	answered atomic.Bool
}

func (o *waitingOnceObserver) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	if o.answered.CompareAndSwap(false, true) {
		return contract.Observation{State: contract.Unknown, WaitingOnPerson: &contract.WaitingOnPerson{Action: "approve the pull request"}}, nil
	}
	return o.inner.ObserveResource(ctx, r)
}

// amendedObserver amends each answer in which its inner observer finds a resource present, and with anyState set every answer.
type amendedObserver struct {
	inner    handler.Observer
	amend    func(*contract.Observation)
	anyState bool
}

func (o amendedObserver) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	obs, err := o.inner.ObserveResource(ctx, r)
	if err == nil && (o.anyState || obs.State == contract.Present) {
		o.amend(&obs)
	}
	return obs, err
}

// unnamedBlock reports its resources with no identity, as a block whose ensure answers before its work finishes does.
type unnamedBlock struct{ *fakeBlock }

func (b unnamedBlock) EnsureResources(ctx context.Context, d contract.Desired) ([]contract.CreatedResource, error) {
	created, err := b.fakeBlock.EnsureResources(ctx, d)
	for i := range created {
		created[i].Identity = nil
	}
	return created, err
}

// silentBlock hides EnsureResources, so it reports no identity.
type silentBlock struct{ b *fakeBlock }

func (s silentBlock) Ensure(ctx context.Context, d contract.Desired) error { return s.b.Ensure(ctx, d) }
func (s silentBlock) Delete(ctx context.Context, d contract.Desired) (map[string]string, error) {
	return s.b.Delete(ctx, d)
}

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

// backgroundTeardown answers its unsubscribe before its teardown finishes: once deleted, each resource in inFlight reads unknown for that many observes, and each in hostGone fails any observe after it first reads absent.
type backgroundTeardown struct {
	*fakeBlock
	deleted  bool
	inFlight map[string]int
	hostGone map[string]bool
	gone     map[string]bool
}

func (b *backgroundTeardown) Delete(ctx context.Context, d contract.Desired) (map[string]string, error) {
	b.deleted = true
	return b.fakeBlock.Delete(ctx, d)
}

type teardownObserver struct {
	b    *backgroundTeardown
	name string
}

func (o teardownObserver) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	b := o.b
	if b.deleted && b.inFlight[o.name] > 0 {
		b.inFlight[o.name]--
		return contract.Observation{State: contract.Unknown, StatusDetail: "converging: the teardown is running"}, nil
	}
	if b.gone[o.name] {
		return contract.Observation{}, fmt.Errorf("the host of %s is gone, so it cannot be observed", o.name)
	}
	obs, err := providerObserver{p: b.p}.ObserveResource(ctx, r)
	if b.deleted && b.hostGone[o.name] && obs.State == contract.Absent {
		b.gone[o.name] = true
	}
	return obs, err
}

func blockDesired(volumeSize string) contract.Desired {
	return contract.Desired{
		Identity: contract.Identity{SubscriptionID: "sub-block", BlockID: "host-block", OrgID: "org-1"},
		Params:   map[string]string{"volumeSize": volumeSize},
	}
}

func hostBlock(t *testing.T) (*fakeBlock, kittest.Block) {
	t.Helper()
	p := newFakeProvider()
	b := &fakeBlock{p: p}
	upgrade := blockDesired("20")
	return b, kittest.Block{
		Config: handler.Config{
			Resource: b,
			Manifest: []byte(hostBlockManifest),
			Types:    typeSnapshot(t),
			Observers: map[string]handler.Observer{
				"aws.ec2-instance": providerObserver{p: p},
				"aws.ebs-volume":   providerObserver{p: p},
			},
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

func containerBlock(t *testing.T) (*fakeBlock, kittest.Block) {
	t.Helper()
	p := newFakeProvider()
	b := &fakeBlock{p: p, container: true}
	return b, kittest.Block{
		Config: handler.Config{
			Resource:  b,
			Manifest:  []byte(containerBlockManifest),
			Types:     typeSnapshot(t),
			Observers: map[string]handler.Observer{"app-container": providerObserver{p: p}},
		},
		Provider: p,
		Desired:  blockDesired(""),
		Requirements: map[string]map[string]map[string]string{
			"host": {
				"aws.ec2-instance": {"hostId": "i-0host", "execKind": "ssm", "execTarget": "i-0host"},
				"hetzner.server":   {"hostId": "4711", "execKind": "ssh", "execTarget": "198.51.100.9"},
			},
		},
	}
}

func TestCheckBlock_FailsTheKitObserverGuardOnAFindingSpecWithNoUsableRead(t *testing.T) {
	_, in := hostBlock(t)
	snap := typeSnapshot(t)
	snap.Types = slices.Clone(snap.Types)
	i := slices.IndexFunc(snap.Types, func(st contract.SnapshotType) bool { return st.ID == "aws.ebs-volume" })
	snap.Types[i].ObserveSpec = &contract.SnapshotObserve{Find: &contract.SnapshotFind{Tag: observe.AWSSubscriptionTag, Identity: "volumeId"},
		Probe: &contract.SnapshotProbe{Reads: []string{"existence", "tags"}, MaxReads: 2}, FullRead: &contract.SnapshotFullRead{CloudControl: "AWS::EC2::Volume"}}
	in.Config.Types = snap
	delete(in.Config.Observers, "aws.ebs-volume")

	wantProblemNaming(t, kittest.CheckBlock(in), `resource "aws.ebs-volume"`, "kit observer", "no usable read")
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

func optionalHostBlock(t *testing.T) (*fakeBlock, kittest.Block) {
	t.Helper()
	b, in := containerBlock(t)
	in.Config.Manifest = []byte(optionalHostBlockManifest)
	in.Requirements = map[string]map[string]map[string]string{
		"host": {"hetzner.server": {"hostId": "4711", "execKind": "ssh", "execTarget": "198.51.100.9"}},
	}
	return b, in
}

func TestCheckBlock_RunsAnOptionalRequirementUnboundAndNamesThatRunWhenItFails(t *testing.T) {
	_, in := optionalHostBlock(t)

	problems := kittest.CheckBlock(in)

	wantProblemNaming(t, problems, "no hostId bound", "requires.host unbound")
	for _, p := range problems {
		if !strings.Contains(p, "requires.host unbound") {
			t.Errorf("problem %q does not name the unbound run, yet the bound run passes", p)
		}
	}
}

func TestCheckBlock_HandsTheUnboundRunOfAnOptionalRequirementNoBindingAndNoTypeForIt(t *testing.T) {
	b, in := optionalHostBlock(t)
	b.hostOptional = true
	in.Desired.Bindings = map[string]map[string]string{"host": {"hostId": "set-by-the-fixture"}}
	in.Desired.RequirementTypes = map[string]string{"host": "hetzner.server"}

	problems := kittest.CheckBlock(in)

	wantNoProblems(t, problems)
	bound, unbound := 0, 0
	for _, d := range b.ensured {
		_, hasBinding := d.Bindings["host"]
		_, hasType := d.RequirementTypes["host"]
		switch {
		case hasBinding && hasType:
			bound++
		case !hasBinding && !hasType:
			unbound++
		default:
			t.Errorf("an ensure was handed Bindings %v and RequirementTypes %v, want both or neither to hold host", d.Bindings, d.RequirementTypes)
		}
	}
	if bound == 0 || unbound == 0 {
		t.Errorf("CheckBlock ran %d ensures with host bound and %d with it unbound, want at least one of each", bound, unbound)
	}
	if in.Desired.Bindings["host"]["hostId"] != "set-by-the-fixture" {
		t.Errorf("CheckBlock changed the fixture's Desired.Bindings to %v, want it left as handed", in.Desired.Bindings)
	}
}

func TestCheckBlock_RunsARequirementWithoutOptionalOnlyBound(t *testing.T) {
	b, in := containerBlock(t)

	problems := kittest.CheckBlock(in)

	wantNoProblems(t, problems)
	for _, d := range b.ensured {
		if _, ok := d.Bindings["host"]; !ok {
			t.Errorf("an ensure was handed Bindings %v with no host, want every run of a requirement without optional bound", d.Bindings)
		}
	}
}

// AssertBlock may also log which guards ran, so only the problems are matched.
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

// The volume's delete leaves it standing, so every unsubscribe observes it present.
func TestAssertBlock_ObservesAResourceOncePerVerb(t *testing.T) {
	b, in := hostBlock(t)
	counts := &observeCounts{most: map[string]int{}}
	b.counts, b.leaveOnDelete = counts, "aws.ebs-volume"
	in.Config.Observers = map[string]handler.Observer{
		"aws.ec2-instance": providerObserver{p: b.p, counts: counts},
		"aws.ebs-volume":   providerObserver{p: b.p, counts: counts},
	}

	runAndCollect(func(tb testing.TB) { kittest.AssertBlock(tb, in) })

	for _, name := range []string{"aws.ec2-instance", "aws.ebs-volume"} {
		if most := counts.most[name]; most != 1 {
			t.Errorf("one verb observed %s at most %d times; want once", name, most)
		}
	}
}

const poolBlockManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: pool-block
  name: Pool Block
spec:
  version: 1.0.0
  resources:
    - name: cloud-worker
      type: hetzner.server
      many: true
  lifecycle:
    endpoint: https://pool-block.example/hook
    operations: [subscribe, unsubscribe, upgrade, validate]
    contract: kit/v1
`

const poolMax = 3

// poolBlock keeps the first size of poolMax servers tagged with the subscription's id, reporting server i as cloud-worker/i, and a leaky one never removes a server the size drops.
type poolBlock struct {
	p     *fakeProvider
	leaky bool
}

func (b *poolBlock) EnsureResources(_ context.Context, d contract.Desired) ([]contract.CreatedResource, error) {
	size, err := strconv.Atoi(d.Params["size"])
	if err != nil {
		return nil, err
	}
	var out []contract.CreatedResource
	for i := 1; i <= poolMax; i++ {
		id := fmt.Sprintf("server-%d", i)
		switch {
		case i <= size:
			if !b.p.holds(id) {
				b.p.put(id, "hetzner.server", d.SubscriptionID)
			}
			out = append(out, contract.CreatedResource{Name: fmt.Sprintf("cloud-worker/%d", i), Type: "hetzner.server", Identity: map[string]string{"id": id}})
		case !b.leaky:
			b.p.remove(id)
		}
	}
	return out, nil
}

func (b *poolBlock) Ensure(ctx context.Context, d contract.Desired) error {
	_, err := b.EnsureResources(ctx, d)
	return err
}

func (b *poolBlock) Delete(context.Context, contract.Desired) (map[string]string, error) {
	for i := 1; i <= poolMax; i++ {
		b.p.remove(fmt.Sprintf("server-%d", i))
	}
	return nil, nil
}

// seenObserver records each name and identity the handler hands it.
type seenObserver struct {
	providerObserver
	mu   *sync.Mutex
	seen map[string]bool
}

func (o seenObserver) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	if r.Identity["id"] != "" || !r.Unsubscribing {
		o.mu.Lock()
		o.seen[r.Name+"="+r.Identity["id"]] = true
		o.mu.Unlock()
	}
	return o.providerObserver.ObserveResource(ctx, r)
}

// poolCheck subscribes poolBlock at size and upgrades it to upgradeTo.
func poolCheck(t *testing.T, size, upgradeTo string, leaky bool) (kittest.Block, *fakeProvider, seenObserver) {
	t.Helper()
	p := newFakeProvider()
	obs := seenObserver{providerObserver: providerObserver{p: p}, mu: &sync.Mutex{}, seen: map[string]bool{}}
	desired := func(size string) contract.Desired {
		return contract.Desired{Identity: contract.Identity{SubscriptionID: "sub-pool", BlockID: "pool-block", OrgID: "org-1"}, Params: map[string]string{"size": size}}
	}
	upgrade := desired(upgradeTo)
	return kittest.Block{
		Config: handler.Config{Resource: &poolBlock{p: p, leaky: leaky}, Manifest: []byte(poolBlockManifest), Types: typeSnapshot(t),
			Observers: map[string]handler.Observer{"cloud-worker": obs}},
		Provider: p,
		Desired:  desired(size),
		Upgrade:  &upgrade,
	}, p, obs
}

func TestCheckBlock_PassesAnEntryMarkedManyWhoseTwoTaggedMembersItHandsTheObserverByName(t *testing.T) {
	in, _, obs := poolCheck(t, "2", "2", false)

	problems := kittest.CheckBlock(in)

	wantNoProblems(t, problems)
	if got, want := slices.Sorted(maps.Keys(obs.seen)), []string{"cloud-worker/1=server-1", "cloud-worker/2=server-2"}; !slices.Equal(got, want) {
		t.Errorf("the handler handed its observer %q, want %q", got, want)
	}
}

func TestCheckBlock_PassesAnEntryMarkedManyWithNoMemberAndFailsAStrayObjectBesideIt(t *testing.T) {
	for _, tc := range []struct {
		label string
		stray bool
	}{{"no object carries the subscription's id", false}, {"a stray server carries it", true}} {
		t.Run(tc.label, func(t *testing.T) {
			in, p, _ := poolCheck(t, "0", "0", false)
			if tc.stray {
				p.put("server-9", "hetzner.server", "sub-pool")
			}

			problems := kittest.CheckBlock(in)

			if !tc.stray {
				wantNoProblems(t, problems)
				return
			}
			wantProblemNaming(t, problems, `resource "server-9"`, "hetzner.server")
		})
	}
}

func TestCheckBlock_AnUpgradeDroppingAMemberPassesOnlyOnceItsTaggedServerIsGone(t *testing.T) {
	for _, tc := range []struct {
		label string
		leaky bool
	}{{"a block removing the member the upgrade drops", false}, {"a block leaving the dropped member's server", true}} {
		t.Run(tc.label, func(t *testing.T) {
			in, p, _ := poolCheck(t, "3", "2", tc.leaky)

			problems := kittest.CheckBlock(in)

			if tc.leaky {
				wantProblemNaming(t, problems, `resource "cloud-worker/3"`, "stopped reporting")
				wantProblemNaming(t, problems, `resource "server-3"`, "hetzner.server")
				return
			}
			wantNoProblems(t, problems)
			if left, _ := p.ListTagged(context.Background(), "sub-pool"); len(left) != 0 {
				t.Errorf("after unsubscribe the provider still lists %+v carrying the subscription's id, want nothing", left)
			}
		})
	}
}

const pullRequestBlockManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: pull-request-block
  name: Pull Request Block
spec:
  version: 1.0.0
  resources:
    - aws.ec2-instance
    - github.pull-request
  provides: [aws.ec2-instance]
  lifecycle:
    endpoint: https://pull-request-block.example/hook
    operations: [subscribe, unsubscribe, upgrade, validate]
    contract: kit/v1
`

// markedSnapshot sets the snapshot key mark true on each type ids names, through the JSON the platform serves.
func markedSnapshot(t *testing.T, mark string, ids ...string) contract.TypeSnapshot {
	t.Helper()
	raw, err := os.ReadFile("../../contract/testdata/type-snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version int              `json:"version"`
		Types   []map[string]any `json:"types"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, typ := range doc.Types {
		if slices.Contains(ids, typ["id"].(string)) {
			typ[mark] = true
		}
	}
	if raw, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	snap, err := contract.ParseTypeSnapshot(raw)
	if err != nil {
		t.Fatalf("ParseTypeSnapshot: %v", err)
	}
	return snap
}

// pullRequestBlock leaves its pull request in the provider on unsubscribe, as a merged pull request stays.
func pullRequestBlock(t *testing.T) (*fakeBlock, kittest.Block) {
	t.Helper()
	p := newFakeProvider()
	b := &fakeBlock{p: p, resources: map[string]string{
		"aws.ec2-instance":    "aws.ec2-instance",
		"github.pull-request": "github.pull-request",
	}, leaveOnDelete: "github.pull-request"}
	return b, kittest.Block{
		Config: handler.Config{
			Resource: b,
			Manifest: []byte(pullRequestBlockManifest),
			Types:    markedSnapshot(t, "endsWithSubscription", "github.pull-request"),
			Observers: map[string]handler.Observer{
				"aws.ec2-instance":    providerObserver{p: p},
				"github.pull-request": providerObserver{p: p},
			},
		},
		Provider: p,
		Desired:  blockDesired(""),
	}
}

func TestAssertBlock_PassesAMarkedResourceLeftPresentAndFailsAnUnmarkedOne(t *testing.T) {
	_, marked := pullRequestBlock(t)
	b, unmarked := pullRequestBlock(t)
	b.leaveOnDelete = "aws.ec2-instance"

	passing := runAndCollect(func(tb testing.TB) { kittest.AssertBlock(tb, marked) })
	failing := runAndCollect(func(tb testing.TB) { kittest.AssertBlock(tb, unmarked) })

	if passing.failed {
		t.Errorf("AssertBlock failed a block whose only resource left present is of a marked type: %q", passing.logs)
	}
	if !failing.failed {
		t.Errorf("AssertBlock passed a block that leaves unmarked aws.ec2-instance present: %q", failing.logs)
	}
}

// unlistedVolumeBlock holds its volume under no subscription's tag, as a record no provider object stands behind.
func unlistedVolumeBlock(t *testing.T) kittest.Block {
	t.Helper()
	b, in := hostBlock(t)
	b.untagged = "aws.ebs-volume"
	return in
}

func TestCheckBlock_PassesAnUnlistedResourceOnlyWhenTheSnapshotMarksItsTypeProviderHoldsNothing(t *testing.T) {
	unmarked := unlistedVolumeBlock(t)
	marked := unlistedVolumeBlock(t)
	marked.Config.Types = markedSnapshot(t, "providerHoldsNothing", "aws.ebs-volume")

	wantProblemNaming(t, kittest.CheckBlock(unmarked), `resource "aws.ebs-volume"`, "untagged")
	wantNoProblems(t, kittest.CheckBlock(marked))
}

func TestCheckBlock_FailsAnObjectListedAsATypeMarkedProviderHoldsNothing(t *testing.T) {
	_, in := hostBlock(t)
	in.Config.Types = markedSnapshot(t, "providerHoldsNothing", "aws.ebs-volume")

	wantProblemNaming(t, kittest.CheckBlock(in), `resource "aws.ebs-volume-`, "carries the subscription's id", "providerHoldsNothing")
}

func partlessSnapshot(t *testing.T) contract.TypeSnapshot {
	t.Helper()
	snap := typeSnapshot(t)
	snap.Types = slices.Clone(snap.Types)
	for i := range snap.Types {
		snap.Types[i].Parts = nil
	}
	return snap
}

// hostParts are the objects the host block's instance and volume hold, each tagged with the subscription's id.
func hostParts() []observe.Tagged {
	return []observe.Tagged{
		{Type: "aws.iam-role", ID: "mitstack-app-myapp-instance"},
		{Type: "aws.iam-role", ID: "mitstack-app-myapp-deploy"},
		{Type: "aws.iam-instance-profile", ID: "mitstack-app-myapp-profile"},
		{Type: "aws.elastic-ip", ID: "eipalloc-0host"},
		{Type: "aws.iam-role", ID: "mitstack-app-myapp-dlm"},
		{Type: "aws.dlm-lifecycle-policy", ID: "policy-0data"},
	}
}

// cfnBlockManifest declares aws-app-infra-cfn's resources with its deploy and push roles left to the instance's and the repository's parts.
const cfnBlockManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: aws-app-infra-cfn
  name: AWS App Infrastructure (CloudFormation)
spec:
  version: 3.0.0
  requires: [github.repository]
  resources:
    - aws.security-group
    - { name: instance-role, type: aws.iam-role }
    - aws.ec2-instance
    - aws.ebs-volume
    - aws.ecr-repository
    - { name: dlm-role, type: aws.iam-role }
    - { name: edge-config, type: aws.ssm-association }
    - { name: app-env, type: aws.ssm-association }
    - { name: data-volume, type: aws.ssm-association }
    - mitstack.app-hostname
  provides: [aws.ec2-instance, aws.ebs-volume, aws.ecr-repository, mitstack.app-hostname]
  lifecycle:
    endpoint: https://api.mitstack.dev/operators/aws-app-infra-cfn/hook
    operations: [subscribe, unsubscribe, upgrade, validate]
    contract: kit/v1
`

// appHostnameType is the platform's mitstack.app-hostname, which the snapshot fixture predates.
var appHostnameType = contract.SnapshotType{
	ID: "mitstack.app-hostname", Capability: "mitstack", Kind: "app-address", Scope: "per-app", Category: "network", Version: 1,
	Outputs: []contract.SnapshotOutput{
		{Name: "appHostname", Required: true},
		{Name: "address", Required: true},
		{Name: "appUrl", Required: true},
	},
}

// cfnBlock tags its declared instance and DLM roles itself and holds its deploy and push roles, instance profile, Elastic IP, DLM policy and SSM documents as parts.
func cfnBlock(t *testing.T) kittest.Block {
	t.Helper()
	p := newFakeProvider()
	resources := map[string]string{
		"aws.security-group":    "aws.security-group",
		"instance-role":         "aws.iam-role",
		"aws.ec2-instance":      "aws.ec2-instance",
		"aws.ebs-volume":        "aws.ebs-volume",
		"aws.ecr-repository":    "aws.ecr-repository",
		"dlm-role":              "aws.iam-role",
		"edge-config":           "aws.ssm-association",
		"app-env":               "aws.ssm-association",
		"data-volume":           "aws.ssm-association",
		"mitstack.app-hostname": "mitstack.app-hostname",
	}
	b := &fakeBlock{p: p, resources: resources, parts: []observe.Tagged{
		{Type: "aws.iam-role", ID: "mitstack-app-myapp-deploy"},
		{Type: "aws.iam-role", ID: "mitstack-app-myapp-ecr-push"},
		{Type: "aws.iam-instance-profile", ID: "mitstack-app-myapp-profile"},
		{Type: "aws.elastic-ip", ID: "eipalloc-0host"},
		{Type: "aws.dlm-lifecycle-policy", ID: "policy-0data"},
		{Type: "aws.ssm-document", ID: "mitstack-app-myapp-edge-config"},
		{Type: "aws.ssm-document", ID: "mitstack-app-myapp-app-env"},
		{Type: "aws.ssm-document", ID: "mitstack-app-myapp-data-volume"},
	}}
	observers := map[string]handler.Observer{}
	for name := range resources {
		observers[name] = providerObserver{p: p}
	}
	snap := typeSnapshot(t)
	snap.Types = append(snap.Types, appHostnameType)
	upgrade := blockDesired("20")
	return kittest.Block{
		Config:   handler.Config{Resource: b, Manifest: []byte(cfnBlockManifest), Types: snap, Observers: observers},
		Provider: p,
		Desired:  blockDesired("10"),
		Upgrade:  &upgrade,
		Requirements: map[string]map[string]map[string]string{
			"github.repository": {"github.repository": {"repo": "acme/myapp"}},
		},
	}
}

// requiringSecretToken returns snap with each type named in ids also requiring the secret output token.
func requiringSecretToken(snap contract.TypeSnapshot, ids ...string) contract.TypeSnapshot {
	snap.Types = slices.Clone(snap.Types)
	for i, typ := range snap.Types {
		if slices.Contains(ids, typ.ID) {
			snap.Types[i].Outputs = append(slices.Clone(typ.Outputs), contract.SnapshotOutput{Name: "token", Secret: true, Required: true})
		}
	}
	return snap
}

const repoHostBlockManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: host-block
  name: Host Block
spec:
  version: 1.0.0
  requires:
    - { name: app-repo, type: github.repository }
  resources:
    - aws.ec2-instance
    - aws.ebs-volume
  provides: [aws.ec2-instance]
  lifecycle:
    endpoint: https://host-block.example/hook
    operations: [subscribe, unsubscribe, upgrade, validate]
    contract: kit/v1
`

type recordingLister struct {
	p    *fakeProvider
	seen *[]contract.Desired
}

func (l recordingLister) ListTagged(ctx context.Context, d contract.Desired, subscriptionID string) ([]observe.Tagged, error) {
	*l.seen = append(*l.seen, d)
	return l.p.ListTagged(ctx, subscriptionID)
}

func listedHostBlock(t *testing.T) (*fakeBlock, kittest.Block, *[]contract.Desired) {
	t.Helper()
	b, in := hostBlock(t)
	seen := &[]contract.Desired{}
	in.Config.Manifest = []byte(repoHostBlockManifest)
	in.Requirements = map[string]map[string]map[string]string{
		"app-repo": {"github.repository": {"repo": "acme/app"}},
	}
	in.Provider = kittest.ListerProvider{Lister: recordingLister{p: b.p, seen: seen}, Loser: b.p}
	return b, in, seen
}

func TestCheckBlock_HandsTheAdaptedListerTheGuardsDesiredWithItsRepositoryBinding(t *testing.T) {
	_, in, seen := listedHostBlock(t)

	problems := kittest.CheckBlock(in)

	wantNoProblems(t, problems)
	if len(*seen) == 0 {
		t.Fatal("CheckBlock never called the lister served through kittest.ListerProvider")
	}
	for _, d := range *seen {
		if d.SubscriptionID != "sub-block" || d.OrgID != "org-1" {
			t.Errorf("the lister was handed subscription %q of org %q; want the guard's sub-block of org-1", d.SubscriptionID, d.OrgID)
		}
		if got := d.RequirementTypes["app-repo"]; got != "github.repository" {
			t.Errorf("the lister was handed requirement app-repo of type %q; want github.repository", got)
		}
		if got := d.Bindings["app-repo"]["repo"]; got != "acme/app" {
			t.Errorf("the lister was handed app-repo bound to %q; want acme/app, the github.repository binding the guard holds", got)
		}
		if len(d.RecordedResources) != 2 {
			t.Errorf("the lister was handed %d recorded resources; want the guard's Desired after subscribe, recording both", len(d.RecordedResources))
		}
	}
}

func TestListerProvider_RefusesAListingWithoutTheGuardsDesired(t *testing.T) {
	p := newFakeProvider()
	var seen []contract.Desired
	provider := kittest.ListerProvider{Lister: recordingLister{p: p, seen: &seen}, Loser: p}

	_, err := provider.ListTagged(context.Background(), "sub-block")

	if err == nil {
		t.Error("ListerProvider.ListTagged with a subscription id alone returned no error; its lister needs the Desired only the tagging guard hands it")
	}
	if len(seen) != 0 {
		t.Errorf("ListerProvider.ListTagged called its lister with %+v; want no call without the guard's Desired", seen)
	}
}

const githubBlockManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: github-block
  name: GitHub Block
spec:
  version: 1.0.0
  resources:
    - github.repository
    - { name: docs, type: github.repository }
  lifecycle:
    endpoint: https://github-block.example/hook
    operations: [subscribe, unsubscribe, upgrade, validate]
    contract: kit/v1
`

// githubBlock creates an app repository and a docs repository, and leaves the one named untagged without the subscription's tag.
func githubBlock(t *testing.T, untagged string) kittest.Block {
	t.Helper()
	p := newFakeProvider()
	b := &fakeBlock{p: p, untagged: untagged, resources: map[string]string{"github.repository": "github.repository", "docs": "github.repository"}}
	upgrade := blockDesired("")
	return kittest.Block{
		Config: handler.Config{
			Resource:  b,
			Manifest:  []byte(githubBlockManifest),
			Types:     typeSnapshot(t),
			Observers: map[string]handler.Observer{"github.repository": providerObserver{p: p}, "docs": providerObserver{p: p}},
		},
		Provider: p,
		Desired:  blockDesired(""),
		Upgrade:  &upgrade,
	}
}

type sentRequest struct {
	operation         string
	readOnly, noWrite bool
}

// requestLog keeps each request the check sent once, by its idempotency key, in the order an observer first saw it.
type requestLog struct {
	mu   sync.Mutex
	seen map[string]bool
	sent []sentRequest
}

type loggingObserver struct {
	inner handler.Observer
	log   *requestLog
}

func (o loggingObserver) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	env, _ := events.EnvelopeFrom(ctx)
	o.log.mu.Lock()
	if !o.log.seen[env.IdempotencyKey] {
		o.log.seen[env.IdempotencyKey] = true
		o.log.sent = append(o.log.sent, sentRequest{env.Operation, r.Desired.ReadOnly, contract.RefuseWrite(ctx, http.MethodPost) != nil})
	}
	o.log.mu.Unlock()
	return o.inner.ObserveResource(ctx, r)
}

func TestCheckBlock_SendsOnlyTheValidatesThatSettleAVerbAsOnesThatMayWrite(t *testing.T) {
	for _, tc := range []struct {
		name        string
		settlePolls int
	}{
		{"a block whose verbs settle at once is sent read-only validates alone", 0},
		{"a block whose subscribe is in flight for one more observe is settled by one validate that may write", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, in := hostBlock(t)
			log, inFlight := &requestLog{seen: map[string]bool{}}, tc.settlePolls
			in.Config.Resource = unnamedBlock{b}
			for name := range in.Config.Observers {
				in.Config.Observers[name] = loggingObserver{inner: byTypeObserver{p: b.p, report: true, inFlight: &inFlight}, log: log}
			}

			wantNoProblems(t, kittest.CheckBlock(in))

			var validates []bool
			for _, sent := range log.sent {
				switch {
				case sent.operation == "validate":
					validates = append(validates, sent.readOnly)
					if sent.noWrite != sent.readOnly {
						t.Errorf("a validate reached the block with ReadOnly %v and a context that refuses a write %v, want the two to agree", sent.readOnly, sent.noWrite)
					}
				case sent.readOnly:
					t.Errorf("a %s reached the block read-only, want only a validate to", sent.operation)
				}
			}
			if len(validates) <= tc.settlePolls {
				t.Fatalf("the check sent %d validates, want the %d settle polls and the validates that settle no verb after them", len(validates), tc.settlePolls)
			}
			for i, readOnly := range validates {
				if want := i >= tc.settlePolls; readOnly != want {
					t.Errorf("validate %d of %d reached the block with ReadOnly %v, want %v: only the first %d settle the subscribe and may write", i+1, len(validates), readOnly, want, tc.settlePolls)
				}
			}
		})
	}
}

func TestCheckBlock(t *testing.T) {
	type mutation func(b *fakeBlock, in *kittest.Block)
	host := func(m mutation) func(t *testing.T) kittest.Block {
		return func(t *testing.T) kittest.Block {
			b, in := hostBlock(t)
			if m != nil {
				m(b, &in)
			}
			return in
		}
	}
	pullRequest := func(leave string, marked ...string) func(t *testing.T) kittest.Block {
		return func(t *testing.T) kittest.Block {
			b, in := pullRequestBlock(t)
			if leave != "" {
				b.leaveOnDelete, in.Config.Types = leave, markedSnapshot(t, "endsWithSubscription", marked...)
			}
			return in
		}
	}
	listed := func(m mutation) func(t *testing.T) kittest.Block {
		return func(t *testing.T) kittest.Block {
			b, in, _ := listedHostBlock(t)
			m(b, &in)
			return in
		}
	}
	github := func(untagged string) func(t *testing.T) kittest.Block {
		return func(t *testing.T) kittest.Block { return githubBlock(t, untagged) }
	}
	// sharingToken is the cfn block with its provided instance and volume both requiring the secret output token, which only the resources named in reporting report.
	sharingToken := func(reporting ...string) func(t *testing.T) kittest.Block {
		return func(t *testing.T) kittest.Block {
			in := cfnBlock(t)
			in.Config.Types = requiringSecretToken(in.Config.Types, "aws.ec2-instance", "aws.ebs-volume")
			for _, name := range reporting {
				amend := func(o *contract.Observation) { o.SecretOutputs = map[string]string{"token": "token-of-" + name} }
				in.Config.Observers[name] = amendedObserver{inner: in.Config.Observers[name], amend: amend}
			}
			return in
		}
	}
	remembering := func(b *fakeBlock, in *kittest.Block) {
		in.Config.Observers["aws.ec2-instance"] = rememberingObserver{b: b, name: "aws.ec2-instance"}
	}
	settling := func(report bool) mutation {
		return func(b *fakeBlock, in *kittest.Block) {
			inFlight := 1
			observer := byTypeObserver{p: b.p, report: report, inFlight: &inFlight}
			in.Config.Resource = unnamedBlock{b}
			in.Config.Observers = map[string]handler.Observer{"aws.ec2-instance": observer, "aws.ebs-volume": observer}
		}
	}
	tearingDown := func(inFlight map[string]int, hostGone ...string) mutation {
		return func(b *fakeBlock, in *kittest.Block) {
			bt := &backgroundTeardown{fakeBlock: b, inFlight: inFlight, hostGone: map[string]bool{}, gone: map[string]bool{}}
			for _, name := range hostGone {
				bt.hostGone[name] = true
			}
			in.Config.Resource = bt
			in.Config.Observers = map[string]handler.Observer{
				"aws.ec2-instance": teardownObserver{b: bt, name: "aws.ec2-instance"},
				"aws.ebs-volume":   teardownObserver{b: bt, name: "aws.ebs-volume"},
			}
		}
	}
	amended := func(amend func(*contract.Observation), anyState bool) mutation {
		return func(b *fakeBlock, in *kittest.Block) {
			in.Config.Observers["aws.ebs-volume"] = amendedObserver{inner: providerObserver{p: b.p}, amend: amend, anyState: anyState}
		}
	}
	unhealthy := func(cause string, anyState bool) mutation {
		return amended(func(o *contract.Observation) {
			o.Unhealthy = &contract.Unhealthy{Cause: cause, Detail: "the volume has been attaching for two hours"}
		}, anyState)
	}
	removing := func(waitsOn string, anyState bool) mutation {
		return amended(func(o *contract.Observation) { o.Removing = &contract.Removing{WaitsOn: waitsOn} }, anyState)
	}
	waitingOnPerson := func(action string) mutation {
		return amended(func(o *contract.Observation) { o.WaitingOnPerson = &contract.WaitingOnPerson{Action: action} }, false)
	}
	waitingOnce := func(b *fakeBlock, in *kittest.Block) {
		in.Config.Observers["aws.ebs-volume"] = &waitingOnceObserver{inner: providerObserver{p: b.p}}
	}
	withParts := func(parts ...observe.Tagged) mutation {
		return func(b *fakeBlock, _ *kittest.Block) { b.parts = parts }
	}
	var noPartNamings [][]string
	for _, part := range hostParts() {
		noPartNamings = append(noPartNamings, []string{`resource "` + part.ID + `"`, part.Type})
	}
	cases := []struct {
		name  string
		block func(t *testing.T) kittest.Block
		// want lists, per expected problem, what it must name; none means the block conforms.
		want  [][]string
		exact bool
	}{
		{"a block that holds every guard passes", host(nil), nil, true},
		{"an observe still reporting a resource the provider lost fails", host(remembering), [][]string{{`resource "aws.ec2-instance"`, "provider"}}, false},
		{"a tagged resource the manifest does not declare fails", host(func(b *fakeBlock, _ *kittest.Block) { b.extraTagged = true }), [][]string{{`resource "state-bucket"`, "aws.s3-bucket"}}, false},
		{"a declared resource left present after unsubscribe fails", host(func(b *fakeBlock, _ *kittest.Block) { b.leaveOnDelete = "aws.ebs-volume" }), [][]string{{`resource "aws.ebs-volume"`, "unsubscribe"}}, false},
		{"an unsubscribe answered converging, then absent on a later observe, passes", host(tearingDown(map[string]int{"aws.ebs-volume": 2})), nil, true},
		{"a resource proven absent is not observed again while the teardown settles", host(tearingDown(map[string]int{"aws.ec2-instance": 2}, "aws.ebs-volume")), nil, true},
		{"a validate hiding the replacement of a stateful resource fails", host(func(b *fakeBlock, _ *kittest.Block) { b.hideReplacement = true }), [][]string{{`resource "aws.ebs-volume"`, "stateful"}}, false},
		{"a block run once per listed requirement type passes", func(t *testing.T) kittest.Block { _, in := containerBlock(t); return in }, nil, true},
		{"declared resources with no identity after subscribe fail", host(func(b *fakeBlock, in *kittest.Block) {
			in.Config.Resource = silentBlock{b: b}
			in.Config.Observers = map[string]handler.Observer{"aws.ec2-instance": byTypeObserver{p: b.p}, "aws.ebs-volume": byTypeObserver{p: b.p}}
			in.Upgrade = nil
		}), [][]string{{`resource "aws.ec2-instance"`, "identity"}, {`resource "aws.ebs-volume"`, "identity"}}, false},
		{"a pending subscribe settled through validate, its observer reporting each identity, passes", host(settling(true)), nil, true},
		{"a pending subscribe settled through validate, its observer reporting no identity, fails", host(settling(false)),
			[][]string{{`resource "aws.ec2-instance"`, "identity"}, {`resource "aws.ebs-volume"`, "identity"}}, false},
		{"a stateful resource with no Upgrade to prove its replacement fails", host(func(_ *fakeBlock, in *kittest.Block) { in.Upgrade = nil }), [][]string{{`resource "aws.ebs-volume"`, "Upgrade"}}, false},
		{"a provided resource missing a required output fails", host(func(b *fakeBlock, in *kittest.Block) {
			in.Config.Observers["aws.ec2-instance"] = providerObserver{p: b.p, drop: "hostId"}
		}), [][]string{{`resource "aws.ec2-instance"`, "hostId"}}, false},
		{"a provided resource missing only an optional output passes", host(func(b *fakeBlock, in *kittest.Block) {
			in.Config.Observers["aws.ec2-instance"] = providerObserver{p: b.p, drop: "publicIp"}
		}), nil, true},
		{"two provided resources whose types both require the secret token, only one reporting it, fail naming the other and token", sharingToken("aws.ec2-instance"),
			[][]string{{`resource "aws.ebs-volume"`, `"token"`, "after subscribe"}, {`resource "aws.ebs-volume"`, `"token"`, "after upgrade"}}, true},
		{"two provided resources whose types both require the secret token, both reporting it, pass", sharingToken("aws.ec2-instance", "aws.ebs-volume"), nil, true},
		{"a resource of a marked type left present after unsubscribe passes", pullRequest(""), nil, true},
		{"an unmarked resource left present beside a marked one fails", pullRequest("aws.ec2-instance", "github.pull-request"), [][]string{{`resource "aws.ec2-instance"`, "unsubscribe"}}, false},
		{"a pull request whose snapshot leaves its type unmarked fails", pullRequest("github.pull-request"), [][]string{{`resource "github.pull-request"`, "unsubscribe"}}, false},
		{"tagged parts of types the recorded resources' types list pass", host(withParts(hostParts()...)), nil, true},
		{"a tagged instance profile the recorded instance's type lists as a part passes", host(withParts(observe.Tagged{Type: "aws.iam-instance-profile", ID: "mitstack-app-myapp-profile"})), nil, true},
		{"a second tagged instance profile beyond the instance's part room fails alone", host(withParts(
			observe.Tagged{Type: "aws.iam-instance-profile", ID: "mitstack-app-myapp-profile"},
			observe.Tagged{Type: "aws.iam-instance-profile", ID: "mitstack-app-myapp-profile-stray"},
		)), [][]string{{`resource "mitstack-app-myapp-profile-stray"`, "aws.iam-instance-profile"}}, true},
		{"every tagged part fails when the snapshot lists no parts", host(func(b *fakeBlock, in *kittest.Block) {
			b.parts, in.Config.Types = hostParts(), partlessSnapshot(t)
		}), noPartNamings, false},
		{"tagged objects beyond the room the declared resources and their parts give fail alone", host(withParts(append(hostParts(),
			observe.Tagged{Type: "aws.vpc", ID: "vpc-0stray"}, observe.Tagged{Type: "aws.iam-role", ID: "mitstack-app-myapp-stray"})...)),
			[][]string{{`resource "vpc-0stray"`, "aws.vpc"}, {`resource "mitstack-app-myapp-stray"`, "aws.iam-role"}}, true},
		{"the cfn block, whose deploy and push roles are parts, passes", cfnBlock, nil, true},
		{"through the adapted lister, a tagged resource the manifest does not declare fails", listed(func(b *fakeBlock, _ *kittest.Block) { b.extraTagged = true }),
			[][]string{{`resource "state-bucket"`, "aws.s3-bucket"}}, false},
		{"through the adapted lister, each resource is lost through the loser", listed(remembering), [][]string{{`resource "aws.ec2-instance"`, "provider"}}, false},
		{"a github block that tags each repository it creates passes", github(""), nil, true},
		{"a github block whose Ensure never tags its app repository fails, naming it untagged", github("github.repository"), [][]string{{`resource "github.repository"`, "untagged"}}, true},
		{"a docs repository left untagged beside a tagged app repository fails, naming docs", github("docs"), [][]string{{`resource "docs"`, "untagged"}}, true},
		{"a present resource answered unhealthy with a cause its type lists passes", host(unhealthy("the volume is still creating, attaching or detaching", false)), nil, true},
		{"a present resource answered unhealthy with a cause its type does not list fails, naming the cause and the type", host(unhealthy("the resource stopped answering", false)),
			[][]string{{`resource "aws.ebs-volume"`, `"the resource stopped answering"`, "aws.ebs-volume"}}, true},
		{"a resource answered unhealthy while absent fails, since only a present resource is unhealthy", host(unhealthy("the volume is still creating, attaching or detaching", true)),
			[][]string{{`resource "aws.ebs-volume"`, "observed absent", "only a present resource"}}, true},
		{"an unknown resource answered waiting on a person, naming the action, passes", host(waitingOnce), nil, true},
		{"a present resource answered waiting on a person fails, since only an unknown resource waits on one", host(waitingOnPerson("approve the pull request")),
			[][]string{{`resource "aws.ebs-volume"`, "observed present", "only an unknown resource"}}, true},
		{"a resource answered waiting on a person with no action fails, naming the resource", host(waitingOnPerson(" ")),
			[][]string{{`resource "aws.ebs-volume"`, "action"}}, true},
		{"a present resource answered removing, naming what the removal waits on, passes", host(removing("the volume to detach from its instance", false)), nil, true},
		{"a resource answered removing while absent fails, since only a present resource is being removed", host(removing("the volume to detach from its instance", true)),
			[][]string{{`resource "aws.ebs-volume"`, "observed absent", "only a present resource"}}, true},
		{"a present resource answered removing with no waitsOn fails, naming the resource", host(removing("", false)),
			[][]string{{`resource "aws.ebs-volume"`, "waitsOn"}}, true},
		{"tagged resources the provider lists by no value of their identity pass, each matched by its type", host(func(b *fakeBlock, _ *kittest.Block) { b.p.listPrefix = "arn:" }), nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.block(t)

			problems := kittest.CheckBlock(in)

			for _, wants := range tc.want {
				wantProblemNaming(t, problems, wants...)
			}
			if tc.exact && len(problems) != len(tc.want) {
				t.Errorf("CheckBlock problems = %q; want exactly %d", problems, len(tc.want))
			}
		})
	}
}

const landingType = "github.scaffold-contribution"

const landingBlockManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: landing-block
  name: Landing Block
spec:
  version: 1.0.0
  resources:
    - ` + landingType + `
  lifecycle:
    endpoint: https://landing-block.example/hook
    operations: [subscribe, unsubscribe, upgrade, validate]
    contract: kit/v1
`

var starterFiles = map[string]string{"CLAUDE.md": "# weather\n", "Dockerfile": "FROM nginx:1.27\n"}

// landingBlock lands two files and a branch through the landing package, or with offer records them through landing.Offer for another block to commit, and removes them through landing.Remove unless teardown names a fault.
type landingBlock struct {
	repo          *landingtest.Fake
	typ, teardown string
	offer, lost   bool
}

func (b *landingBlock) client() *ghapi.Client { return ghapi.NewClient(b.repo.URL, "ghs_kittest", nil) }

func (b *landingBlock) reachableClient(context.Context, contract.Desired) (*ghapi.Client, error) {
	if b.lost {
		return nil, errors.New("the installation no longer reaches the repository")
	}
	return b.client(), nil
}

func (b *landingBlock) EnsureResources(ctx context.Context, d contract.Desired) ([]contract.CreatedResource, error) {
	prior, _ := landing.RecordFromIdentity(d.RecordedResources[b.typ].Identity)
	rec, err := b.record(ctx, d, prior)
	if err != nil {
		return nil, err
	}
	identity, err := rec.Identity()
	return []contract.CreatedResource{{Name: b.typ, Type: b.typ, Identity: identity}}, err
}

func (b *landingBlock) record(ctx context.Context, d contract.Desired, prior landing.Record) (landing.Record, error) {
	if !b.offer {
		rec, err := landing.Land(ctx, b.client(), landing.Landing{Repository: b.repo.Repo, Message: "land", Files: starterFiles, Prior: prior})
		if err == nil {
			b.repo.Branch(landing.BranchPrefix(d.SubscriptionID) + "land")
		}
		return rec, err
	}
	b.lost = false
	files := b.offered()
	if b.teardown == "records one file" {
		delete(files, "Dockerfile")
	}
	rec, err := landing.Offer(ctx, b.client(), landing.Landing{Repository: b.repo.Repo, Files: files, Prior: prior})
	if err == nil && b.teardown == "commits its offer itself" {
		b.repo.Seed(files)
	}
	return rec, err
}

// offered is what the contribution offers app-scaffold, the starter files unless teardown is "offers no file".
func (b *landingBlock) offered() map[string]string {
	if b.teardown == "offers no file" {
		return map[string]string{}
	}
	return maps.Clone(starterFiles)
}

func (b *landingBlock) Ensure(ctx context.Context, d contract.Desired) error {
	_, err := b.EnsureResources(ctx, d)
	return err
}

func (b *landingBlock) Delete(ctx context.Context, d contract.Desired) (map[string]string, error) {
	_, _, err := b.DeleteKeeping(ctx, d)
	return nil, err
}

func (b *landingBlock) DeleteKeeping(ctx context.Context, d contract.Desired) (map[string]string, map[string][]contract.Kept, error) {
	rec, err := landing.RecordFromIdentity(d.RecordedResources[b.typ].Identity)
	if err != nil || b.teardown == "leaves its files" {
		return nil, nil, nil
	}
	subscription := d.SubscriptionID
	switch b.teardown {
	case "removes under another prefix":
		subscription += "-other"
	case "deletes every file":
		if err := deleteFiles(ctx, b.client(), rec); err != nil {
			return nil, nil, err
		}
	}
	res, err := landing.Remove(ctx, b.client(), landing.Removal{Subscription: subscription, Records: []landing.Record{rec}})
	if b.teardown == "reports nothing kept" {
		return nil, nil, err
	}
	return nil, map[string][]contract.Kept{b.typ: res.Kept}, err
}

func deleteFiles(ctx context.Context, c *ghapi.Client, rec landing.Record) error {
	head, err := c.HeadCommit(ctx, rec.Repository)
	if err != nil {
		return err
	}
	changes := map[string]*string{}
	for _, f := range rec.Files {
		changes[f.Path] = nil
	}
	_, err = c.CommitChanges(ctx, rec.Repository, head, "delete", changes)
	return err
}

type repositoryProvider struct{ b *landingBlock }

func (p repositoryProvider) Repository() *landingtest.Fake { return p.b.repo }

func (p repositoryProvider) ListTagged(context.Context, string) ([]observe.Tagged, error) {
	return []observe.Tagged{{Type: p.b.typ, ID: p.b.repo.Repo}}, nil
}

func (p repositoryProvider) Lose(ctx context.Context, r contract.CreatedResource) error {
	if p.b.offer {
		p.b.lost = true
		return nil
	}
	rec, _ := landing.RecordFromIdentity(r.Identity)
	return deleteFiles(ctx, p.b.client(), rec)
}

// offerObserver adds the offered files as the scaffoldContribution output that app-scaffold reads.
type offerObserver struct{ b *landingBlock }

func (o offerObserver) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	obs, err := landing.OfferObserver{Client: o.b.reachableClient}.ObserveResource(ctx, r)
	if err != nil || obs.State != contract.Present {
		return obs, err
	}
	fragment, err := json.Marshal(map[string]any{"files": o.b.offered()})
	obs.Outputs = map[string]string{"scaffoldContribution": string(fragment)}
	return obs, err
}

func landingBlockThat(t *testing.T, typ, teardown string, offer bool) kittest.Block {
	t.Helper()
	b := &landingBlock{repo: landingtest.New(t, "acme/weather"), typ: typ, teardown: teardown, offer: offer}
	types := typeSnapshot(t)
	if teardown == "leaves its files" {
		types = markedSnapshot(t, "endsWithSubscription", typ)
	}
	var observer handler.Observer = landing.Observer{Client: b.reachableClient}
	if offer {
		observer = offerObserver{b: b}
	}
	return kittest.Block{
		Config: handler.Config{
			Resource:  b,
			Manifest:  []byte(strings.Replace(landingBlockManifest, landingType, typ, 1)),
			Types:     types,
			Observers: map[string]handler.Observer{typ: observer},
		},
		Provider: repositoryProvider{b: b},
		Desired:  blockDesired(""),
	}
}

// The check changes CLAUDE.md, the first file the landing created, before it unsubscribes, which leaves Dockerfile unchanged.
func TestAssertBlock_ReadsMainAfterUnsubscribingABlockThatLandsFiles(t *testing.T) {
	for _, tc := range []struct {
		typ, teardown string
		want          []string
	}{
		{landingType, "", nil},
		{landingType, "leaves its files", []string{"Dockerfile", "unchanged"}},
		{landingType, "removes under another prefix", []string{"branch mitstack/sub-block/land"}},
		{landingType, "deletes every file", []string{"CLAUDE.md", "changed"}},
		{landingType, "reports nothing kept", []string{"CLAUDE.md", "delete outcome"}},
		{"github.commit", "leaves its files", []string{"Dockerfile", "unchanged"}},
	} {
		t.Run("a "+tc.typ+" block that "+cmp.Or(tc.teardown, "removes through the landing package"), func(t *testing.T) {
			in := landingBlockThat(t, tc.typ, tc.teardown, false)

			got := runAndCollect(func(tb testing.TB) { kittest.AssertBlock(tb, in) })

			if got.failed != (tc.want != nil) {
				t.Fatalf("AssertBlock failed = %v; want %v, naming %q: %q", got.failed, tc.want != nil, tc.want, got.logs)
			}
			wantProblemNaming(t, got.logs, tc.want...)
		})
	}
}

const twoRepositoryManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: landing-block
  name: Landing Block
spec:
  version: 1.0.0
  resources:
    - { name: app-files, type: github.commit }
    - { name: ci-workflows, type: github.workflow-file }
  lifecycle:
    endpoint: https://landing-block.example/hook
    operations: [subscribe, unsubscribe, upgrade, validate]
    contract: kit/v1
`

var ciFiles = map[string]string{"build.yml": "name: build\n", "dispatch.yml": "name: dispatch\n"}

// twoRepositoryBlock lands the starter files in the app's repository and two workflows in a CI repository of its own, which its unsubscribe deletes whole unless teardown names another way.
type twoRepositoryBlock struct {
	app, ci  *landingtest.Fake
	teardown string
}

func (b *twoRepositoryBlock) fakes() map[string]*landingtest.Fake {
	return map[string]*landingtest.Fake{b.app.Repo: b.app, b.ci.Repo: b.ci}
}

// nilEntryProvider also hands a nil fake for a repository the block never lands in.
type nilEntryProvider struct{ repositoriesProvider }

func (p nilEntryProvider) Repositories() map[string]*landingtest.Fake {
	fakes := p.b.fakes()
	fakes["acme/unused"] = nil
	return fakes
}

func (b *twoRepositoryBlock) client(repo string) *ghapi.Client {
	return ghapi.NewClient(b.fakes()[repo].URL, "ghs_kittest", nil)
}

func (b *twoRepositoryBlock) observer(repo string) landing.Observer {
	return landing.Observer{Client: func(context.Context, contract.Desired) (*ghapi.Client, error) { return b.client(repo), nil }}
}

func (b *twoRepositoryBlock) EnsureResources(ctx context.Context, d contract.Desired) ([]contract.CreatedResource, error) {
	var out []contract.CreatedResource
	for _, l := range []struct {
		name, typ string
		fake      *landingtest.Fake
		files     map[string]string
	}{{"app-files", "github.commit", b.app, starterFiles}, {"ci-workflows", "github.workflow-file", b.ci, ciFiles}} {
		prior, _ := landing.RecordFromIdentity(d.RecordedResources[l.name].Identity)
		rec, err := landing.Land(ctx, b.client(l.fake.Repo), landing.Landing{Repository: l.fake.Repo, Message: "land", Files: l.files, Prior: prior})
		if err != nil {
			return nil, err
		}
		identity, err := rec.Identity()
		if err != nil {
			return nil, err
		}
		out = append(out, contract.CreatedResource{Name: l.name, Type: l.typ, Identity: identity})
	}
	return out, nil
}

func (b *twoRepositoryBlock) Ensure(ctx context.Context, d contract.Desired) error {
	_, err := b.EnsureResources(ctx, d)
	return err
}

func (b *twoRepositoryBlock) Delete(ctx context.Context, d contract.Desired) (map[string]string, error) {
	_, _, err := b.DeleteKeeping(ctx, d)
	return nil, err
}

func (b *twoRepositoryBlock) DeleteKeeping(ctx context.Context, d contract.Desired) (map[string]string, map[string][]contract.Kept, error) {
	kept := map[string][]contract.Kept{}
	names := []string{"app-files"}
	if b.teardown == "removes the CI repository's files" {
		names = append(names, "ci-workflows")
	}
	for _, name := range names {
		rec, err := landing.RecordFromIdentity(d.RecordedResources[name].Identity)
		if err != nil {
			continue
		}
		res, err := landing.Remove(ctx, b.client(rec.Repository), landing.Removal{Subscription: d.SubscriptionID, Records: []landing.Record{rec}})
		if err != nil {
			return nil, nil, err
		}
		kept[name] = res.Kept
	}
	if b.teardown == "" {
		b.ci.DeleteRepository()
	}
	return nil, kept, nil
}

// repositoriesProvider hands the check one fake per repository and no single Repository.
type repositoriesProvider struct{ b *twoRepositoryBlock }

func (p repositoriesProvider) Repositories() map[string]*landingtest.Fake { return p.b.fakes() }

func (p repositoriesProvider) ListTagged(context.Context, string) ([]observe.Tagged, error) {
	return []observe.Tagged{{Type: "github.commit", ID: p.b.app.Repo}, {Type: "github.workflow-file", ID: p.b.ci.Repo}}, nil
}

func (p repositoriesProvider) Lose(ctx context.Context, r contract.CreatedResource) error {
	rec, _ := landing.RecordFromIdentity(r.Identity)
	return deleteFiles(ctx, p.b.client(rec.Repository), rec)
}

// The check changes CLAUDE.md on the app's repository and build.yml on the CI repository before it unsubscribes, which leaves Dockerfile and dispatch.yml unchanged.
func TestAssertBlock_ReadsEachLandingRecordAgainstItsOwnRepository(t *testing.T) {
	for _, tc := range []struct {
		teardown string
		nilEntry bool
		want     []string
	}{
		{"", false, nil},
		{"", true, nil},
		{"removes the CI repository's files", false, nil},
		{"leaves the CI repository", false, []string{"acme/weather-ci", "dispatch.yml", "unchanged"}},
	} {
		name := "a block that " + cmp.Or(tc.teardown, "deletes the CI repository whole")
		if tc.nilEntry {
			name += ", with a nil fake beside its two"
		}
		t.Run(name, func(t *testing.T) {
			b := &twoRepositoryBlock{app: landingtest.New(t, "acme/weather"), ci: landingtest.New(t, "acme/weather-ci"), teardown: tc.teardown}
			var provider kittest.Provider = repositoriesProvider{b: b}
			if tc.nilEntry {
				provider = nilEntryProvider{repositoriesProvider{b: b}}
			}
			in := kittest.Block{
				Config: handler.Config{
					Resource:  b,
					Manifest:  []byte(twoRepositoryManifest),
					Types:     typeSnapshot(t),
					Observers: map[string]handler.Observer{"app-files": b.observer(b.app.Repo), "ci-workflows": b.observer(b.ci.Repo)},
				},
				Provider: provider,
				Desired:  blockDesired(""),
			}

			got := runAndCollect(func(tb testing.TB) { kittest.AssertBlock(tb, in) })

			if got.failed != (tc.want != nil) {
				t.Fatalf("AssertBlock failed = %v; want %v, naming %q: %q", got.failed, tc.want != nil, tc.want, got.logs)
			}
			wantProblemNaming(t, got.logs, tc.want...)
		})
	}
}

// The check commits the offered files onto main as app-scaffold would, then changes CLAUDE.md, which leaves Dockerfile unchanged.
func TestAssertBlock_CommitsAContributionsOfferedFilesBeforeReadingMainAfterUnsubscribe(t *testing.T) {
	for _, tc := range []struct {
		teardown string
		want     []string
	}{
		{"", nil},
		{"leaves its files", []string{"Dockerfile", "unchanged"}},
		{"records one file", []string{"Dockerfile", "landing record"}},
	} {
		t.Run("a contribution that "+cmp.Or(tc.teardown, "removes through the landing package"), func(t *testing.T) {
			in := landingBlockThat(t, landingType, tc.teardown, true)

			got := runAndCollect(func(tb testing.TB) { kittest.AssertBlock(tb, in) })

			if got.failed != (tc.want != nil) {
				t.Fatalf("AssertBlock failed = %v; want %v, naming %q: %q", got.failed, tc.want != nil, tc.want, got.logs)
			}
			wantProblemNaming(t, got.logs, tc.want...)
		})
	}
}

// mainAPI is GitHub as observe.GitHubTagged reads it, holding the fake repository on no topic and nothing on it but what main holds.
type mainAPI struct{ repo *landingtest.Fake }

var _ observe.GitHubTagAPI = mainAPI{}

func (mainAPI) RepositoriesWithTopic(context.Context, contract.Desired, string) ([]observe.GitHubRepositoryTopics, error) {
	return nil, nil
}

func (mainAPI) Webhooks(context.Context, contract.Desired, string) ([]observe.GitHubWebhook, error) {
	return nil, nil
}

func (mainAPI) ActionsSecretNames(context.Context, contract.Desired, string) ([]string, error) {
	return nil, nil
}

func (mainAPI) ActionsVariableNames(context.Context, contract.Desired, string) ([]string, error) {
	return nil, nil
}

func (mainAPI) Commits(context.Context, contract.Desired, string) ([]observe.GitHubCommitPaths, error) {
	return nil, nil
}

func (a mainAPI) MainFile(_ context.Context, _ contract.Desired, repo, path string) ([]byte, bool, error) {
	if repo != a.repo.Repo {
		return nil, false, fmt.Errorf("the fake holds %s, not %s", a.repo.Repo, repo)
	}
	content, held := a.repo.Main()[path]
	return []byte(content), held, nil
}

type listsNothing struct{}

func (listsNothing) ListTagged(context.Context, contract.Desired, string) ([]observe.Tagged, error) {
	return nil, nil
}

// listerRepositoryProvider lists through a kit lister and hands the check the block's fake repository.
type listerRepositoryProvider struct {
	kittest.ListerProvider
	b *landingBlock
}

func (p listerRepositoryProvider) Repository() *landingtest.Fake { return p.b.repo }

func TestAssertBlock_ListsAContributionFromItsLandingRecord(t *testing.T) {
	for _, tc := range []struct {
		name, teardown string
		nothingListed  bool
		want           []string
	}{
		{name: "a contribution whose offered files the check commits is listed from its record"},
		{name: "a contribution offering no file holds nothing on GitHub for a lister to list", teardown: "offers no file"},
		{name: "a contribution whose files the check commits fails when its lister lists nothing", nothingListed: true, want: []string{`resource "` + landingType + `"`, "untagged"}},
		{name: "a contribution whose created files main holds before the check commits fails when its lister lists nothing", teardown: "commits its offer itself", nothingListed: true, want: []string{`resource "` + landingType + `"`, "untagged"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := landingBlockThat(t, landingType, tc.teardown, true)
			b := in.Provider.(repositoryProvider).b
			var lister observe.TaggedLister = observe.GitHubTagged{API: mainAPI{repo: b.repo}}
			if tc.nothingListed {
				lister = listsNothing{}
			}
			in.Provider = listerRepositoryProvider{ListerProvider: kittest.ListerProvider{Lister: lister, Loser: repositoryProvider{b: b}}, b: b}

			got := runAndCollect(func(tb testing.TB) { kittest.AssertBlock(tb, in) })

			if got.failed != (tc.want != nil) {
				t.Fatalf("AssertBlock failed = %v; want %v, naming %q: %q", got.failed, tc.want != nil, tc.want, got.logs)
			}
			wantProblemNaming(t, got.logs, tc.want...)
			if !slices.ContainsFunc(got.logs, func(l string) bool { return strings.Contains(l, "guards ran:") && strings.Contains(l, "tagging") }) {
				t.Errorf("the tagging guard did not run: %q", got.logs)
			}
		})
	}
}

// writingOnReadOnlyObserver writes on each request that reaches it read-only, as a block does that never reads Desired.ReadOnly.
type writingOnReadOnlyObserver struct {
	handler.Observer
	write func(context.Context) error
}

func (o writingOnReadOnlyObserver) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	if r.Desired.ReadOnly {
		if err := o.write(ctx); err != nil {
			return contract.Observation{}, err
		}
	}
	return o.Observer.ObserveResource(ctx, r)
}

func TestCheckBlock_FailsABlockThatWritesOnAValidateThatSettlesNoVerb(t *testing.T) {
	for name, tc := range map[string]struct {
		write func(ctx context.Context, b *landingBlock) error
		want  string
	}{
		"commits through a kit client": {func(ctx context.Context, b *landingBlock) error {
			return b.client().CommitFiles(ctx, b.repo.Repo, "dry run", map[string]string{"dry-run.txt": "x\n"})
		}, ""},
		"dispatches a workflow through a client of its own": {func(_ context.Context, b *landingBlock) error {
			_, err := http.Post(b.repo.URL+"/repos/"+b.repo.Repo+"/actions/workflows/ci.yml/dispatches", "application/json", http.NoBody)
			return err
		}, "POST /repos/acme/weather/actions/workflows/ci.yml/dispatches"},
	} {
		t.Run("a block that "+name, func(t *testing.T) {
			in := landingBlockThat(t, landingType, "", false)
			b := in.Provider.(repositoryProvider).b
			in.Config.Observers[landingType] = writingOnReadOnlyObserver{in.Config.Observers[landingType], func(ctx context.Context) error { return tc.write(ctx, b) }}

			wantProblemNaming(t, kittest.CheckBlock(in), in.Desired.BlockID, "wrote on a validate that settles no verb", tc.want)
		})
	}
}

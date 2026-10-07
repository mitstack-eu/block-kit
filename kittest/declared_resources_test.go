package kittest_test

// declared_resources_test.go pins CheckDeclaredResources, the per-resource
// conformance check a block that declares its resources runs in its own
// tests, and AssertDeclaredResources, its testing.TB wrapper. The check
// drives each declared resource, in manifest order, through ensure, observe,
// ensure again and observe again; tears the resources down in reverse
// manifest order, proving each absent; and checks that every supplied
// resource stands before the first ensure and after the last delete. Every
// problem names the resource it concerns.

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/kittest"
)

// checkedManifest declares network then instance, and requires a
// git-repository the app supplies.
const checkedManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: checked-block
  name: Checked Block
spec:
  version: 1.0.0
  requires: [github.repository]
  resources:
    - name: network
      type: aws.vpc
    - name: instance
      type: aws.ec2-instance
  lifecycle:
    endpoint: https://checked-block.example/hook
    operations: [subscribe, unsubscribe, upgrade, validate]
    contract: kit/v1
`

const checkedV1AlphaManifest = `apiVersion: mitstack/v1alpha
kind: Block
metadata:
  id: checked-block
  name: Checked Block
spec:
  layer: infra
`

const checkedNoResourcesManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: checked-block
  name: Checked Block
spec:
  version: 1.0.0
  resources: []
  lifecycle:
    endpoint: https://checked-block.example/hook
    operations: [subscribe, unsubscribe, upgrade, validate]
    contract: kit/v1
`

// checkedDesired is the subscription the check drives the resources for.
var checkedDesired = contract.Desired{
	Identity: contract.Identity{SubscriptionID: "sub-checked", BlockID: "checked-block", OrgID: "org-checked"},
	Params:   map[string]string{"instanceType": "t3.small"},
}

// scriptedResource is a kittest.FakeResource that writes each call, named
// by its resource and the subscription it was addressed to, to a shared
// log. It adds what FakeResource cannot do: report SecretOutputs that may
// depend on how many times Ensure ran, and act on something outside itself
// when deleted, as a block that removes a supplied resource does.
type scriptedResource struct {
	*kittest.FakeResource
	name          string
	log           *[]string
	secretOutputs func(ensures int) map[string]string
	onDelete      func()
}

func (r *scriptedResource) record(call, subscriptionID string) {
	if r.log != nil {
		*r.log = append(*r.log, r.name+" "+call+" "+subscriptionID)
	}
}

func (r *scriptedResource) Ensure(ctx context.Context, d contract.Desired) error {
	r.record("ensure", d.SubscriptionID)
	return r.FakeResource.Ensure(ctx, d)
}

func (r *scriptedResource) Observe(ctx context.Context, id contract.Identity) (contract.Observation, error) {
	r.record("observe", id.SubscriptionID)
	obs, err := r.FakeResource.Observe(ctx, id)
	if err == nil && obs.State == contract.Present && r.secretOutputs != nil {
		obs.SecretOutputs = r.secretOutputs(r.EnsureCalls)
	}
	return obs, err
}

func (r *scriptedResource) Delete(ctx context.Context, id contract.Identity) error {
	r.record("delete", id.SubscriptionID)
	if r.onDelete != nil {
		r.onDelete()
	}
	return r.FakeResource.Delete(ctx, id)
}

// suppliedFake is a resource the app supplies to the block: it stands until
// something removes it.
type suppliedFake struct {
	name    string
	present bool
	log     *[]string
}

func (s *suppliedFake) Observe(ctx context.Context, id contract.Identity) (contract.Observation, error) {
	if s.log != nil {
		*s.log = append(*s.log, "supplied "+s.name+" observe "+id.SubscriptionID)
	}
	if s.present {
		return contract.Observation{State: contract.Present, Outputs: map[string]string{"repo": "acme/app"}}, nil
	}
	return contract.Observation{State: contract.Absent}, nil
}

// checkedBlock is a converging block over checkedManifest: two declared
// resources and the supplied git-repository, all sharing one log.
type checkedBlock struct {
	network, instance *scriptedResource
	repo              *suppliedFake
	log               *[]string
}

func newCheckedBlock() *checkedBlock {
	log := &[]string{}
	return &checkedBlock{
		network: &scriptedResource{
			name: "network", log: log,
			FakeResource: &kittest.FakeResource{Outputs: map[string]string{"vpcId": "vpc-0abc"}},
		},
		instance: &scriptedResource{
			name: "instance", log: log,
			FakeResource: &kittest.FakeResource{Outputs: map[string]string{"publicIp": "203.0.113.7"}},
		},
		repo: &suppliedFake{name: "git-repository", present: true, log: log},
		log:  log,
	}
}

func (b *checkedBlock) input() kittest.DeclaredResources {
	return kittest.DeclaredResources{
		Manifest:  []byte(checkedManifest),
		Resources: map[string]handler.DeclaredResource{"network": b.network, "instance": b.instance},
		Supplied:  map[string]kittest.SuppliedResource{"git-repository": b.repo},
		Desired:   checkedDesired,
	}
}

// wantProblemsAllNaming fails unless problems is non-empty and every
// problem begins with prefix, so a fault in one resource is never blamed on
// another.
func wantProblemsAllNaming(t *testing.T, problems []string, prefix string) {
	t.Helper()
	if len(problems) == 0 {
		t.Fatalf("CheckDeclaredResources found no problem, want at least one beginning %q", prefix)
	}
	for _, p := range problems {
		if !strings.HasPrefix(p, prefix) {
			t.Errorf("problem %q does not begin %q", p, prefix)
		}
	}
}

// --- a block that converges ---------------------------------------------------

func TestCheckDeclaredResources_NoProblemsForResourcesThatConverge(t *testing.T) {
	b := newCheckedBlock()
	if problems := kittest.CheckDeclaredResources(b.input()); len(problems) != 0 {
		t.Errorf("a converging block must have no problems, got %q", problems)
	}
}

func TestCheckDeclaredResources_DrivesEachResourceInManifestOrderAndTearsDownInReverse(t *testing.T) {
	b := newCheckedBlock()
	kittest.CheckDeclaredResources(b.input())

	want := []string{
		"supplied git-repository observe sub-checked",
		"network ensure sub-checked",
		"network observe sub-checked",
		"network ensure sub-checked",
		"network observe sub-checked",
		"instance ensure sub-checked",
		"instance observe sub-checked",
		"instance ensure sub-checked",
		"instance observe sub-checked",
		"instance delete sub-checked",
		"instance observe sub-checked",
		"network delete sub-checked",
		"network observe sub-checked",
		"supplied git-repository observe sub-checked",
	}
	if !reflect.DeepEqual(*b.log, want) {
		t.Errorf("calls =\n  %s\nwant\n  %s", strings.Join(*b.log, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestCheckDeclaredResources_EnsuresWithTheDesiredSubscription(t *testing.T) {
	b := newCheckedBlock()
	var got contract.Desired
	capture := &desiredCapture{DeclaredResource: b.network, got: &got}
	in := b.input()
	in.Resources["network"] = capture

	kittest.CheckDeclaredResources(in)
	if !reflect.DeepEqual(got, checkedDesired) {
		t.Errorf("Ensure received %+v, want the Desired the check was given %+v", got, checkedDesired)
	}
}

// desiredCapture records the Desired its resource's Ensure is handed.
type desiredCapture struct {
	handler.DeclaredResource
	got *contract.Desired
}

func (c *desiredCapture) Ensure(ctx context.Context, d contract.Desired) error {
	*c.got = d
	return c.DeclaredResource.Ensure(ctx, d)
}

// --- a declared resource that misbehaves ---------------------------------------

func TestCheckDeclaredResources_ProblemNamingAResourceWhoseEnsureFails(t *testing.T) {
	b := newCheckedBlock()
	b.instance.EnsureErr = errors.New("create instance: capacity")
	wantProblemsAllNaming(t, kittest.CheckDeclaredResources(b.input()), `resource "instance": `)
}

func TestCheckDeclaredResources_ProblemNamingAResourceNotPresentAfterEnsure(t *testing.T) {
	b := newCheckedBlock()
	unknown := contract.Unknown
	b.instance.ObserveState = &unknown
	wantProblemsAllNaming(t, kittest.CheckDeclaredResources(b.input()), `resource "instance": `)
}

func TestCheckDeclaredResources_ProblemNamingAResourceWhoseObserveFails(t *testing.T) {
	b := newCheckedBlock()
	b.network.ObserveErr = errors.New("describe VPC: throttled")
	wantProblemsAllNaming(t, kittest.CheckDeclaredResources(b.input()), `resource "network": `)
}

func TestCheckDeclaredResources_ProblemNamingAResourceTheSecondEnsureChanges(t *testing.T) {
	b := newCheckedBlock()
	b.instance.ClobberOnUpgrade = true
	wantProblemsAllNaming(t, kittest.CheckDeclaredResources(b.input()), `resource "instance": `)
}

func TestCheckDeclaredResources_ProblemNamingAResourceWhoseSecondEnsureChangesItsSecretOutputs(t *testing.T) {
	b := newCheckedBlock()
	b.network.secretOutputs = func(ensures int) map[string]string {
		if ensures > 1 {
			return map[string]string{"flowLogKey": "rotated"}
		}
		return map[string]string{"flowLogKey": "original"}
	}
	wantProblemsAllNaming(t, kittest.CheckDeclaredResources(b.input()), `resource "network": `)
}

func TestCheckDeclaredResources_ProblemNamingAResourceItsDeleteLeavesStanding(t *testing.T) {
	b := newCheckedBlock()
	b.instance.LeakOnDelete = true
	wantProblemsAllNaming(t, kittest.CheckDeclaredResources(b.input()), `resource "instance": `)
}

func TestCheckDeclaredResources_ProblemNamingAResourceWhoseDeleteFails(t *testing.T) {
	b := newCheckedBlock()
	b.network.DeleteErr = errors.New("delete VPC: dependency violation")
	wantProblemsAllNaming(t, kittest.CheckDeclaredResources(b.input()), `resource "network": `)
}

// --- a supplied resource -------------------------------------------------------

func TestCheckDeclaredResources_ProblemNamingASuppliedResourceTheBlockDeletes(t *testing.T) {
	b := newCheckedBlock()
	b.instance.onDelete = func() { b.repo.present = false }
	wantProblemsAllNaming(t, kittest.CheckDeclaredResources(b.input()), `supplied resource "git-repository": `)
}

func TestCheckDeclaredResources_ProblemNamingASuppliedResourceAbsentBeforeTheFirstEnsure(t *testing.T) {
	b := newCheckedBlock()
	b.repo.present = false
	wantProblemsAllNaming(t, kittest.CheckDeclaredResources(b.input()), `supplied resource "git-repository": `)
}

// --- what the manifest declares --------------------------------------------------

func TestCheckDeclaredResources_ProblemNamingADeclaredResourceWithNoHandler(t *testing.T) {
	b := newCheckedBlock()
	in := b.input()
	delete(in.Resources, "instance")

	problems := kittest.CheckDeclaredResources(in)
	found := false
	for _, p := range problems {
		if strings.HasPrefix(p, `resource "instance": `) {
			found = true
		}
	}
	if !found {
		t.Errorf("problems %q name no problem for the declared resource %q that has no handler", problems, "instance")
	}
}

func TestCheckDeclaredResources_ProblemNamingAHandlerForAnUndeclaredResource(t *testing.T) {
	b := newCheckedBlock()
	in := b.input()
	in.Resources["cache"] = &kittest.FakeResource{}

	problems := kittest.CheckDeclaredResources(in)
	found := false
	for _, p := range problems {
		if strings.Contains(p, `"cache"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("problems %q do not name the handler for the undeclared resource %q", problems, "cache")
	}
}

func TestCheckDeclaredResources_ProblemForAManifestThatIsNotV1Beta(t *testing.T) {
	b := newCheckedBlock()
	in := b.input()
	in.Manifest = []byte(checkedV1AlphaManifest)

	problems := kittest.CheckDeclaredResources(in)
	if len(problems) == 0 {
		t.Fatal("a mitstack/v1alpha manifest declares no resources, so the check must report a problem")
	}
	if !strings.Contains(strings.Join(problems, "\n"), contract.ManifestAPIVersion) {
		t.Errorf("problems %q do not name %s", problems, contract.ManifestAPIVersion)
	}
	if b.network.EnsureCalls+b.instance.EnsureCalls != 0 {
		t.Error("the check drove resources a manifest it cannot read does not declare")
	}
}

func TestCheckDeclaredResources_ProblemForAManifestDeclaringNoResources(t *testing.T) {
	b := newCheckedBlock()
	in := b.input()
	in.Manifest = []byte(checkedNoResourcesManifest)
	in.Resources = map[string]handler.DeclaredResource{}

	if problems := kittest.CheckDeclaredResources(in); len(problems) == 0 {
		t.Fatal("a manifest declaring no resources gives the check nothing to prove, so it must report a problem")
	}
}

func TestCheckDeclaredResources_ProblemForAManifestTheParserRefuses(t *testing.T) {
	b := newCheckedBlock()
	in := b.input()
	in.Manifest = []byte("apiVersion: [unclosed\n")

	if problems := kittest.CheckDeclaredResources(in); len(problems) == 0 {
		t.Fatal("a manifest the parser refuses must be a problem, never a silent pass")
	}
}

// --- AssertDeclaredResources ------------------------------------------------------

func TestAssertDeclaredResources_PassesForResourcesThatConverge(t *testing.T) {
	b := newCheckedBlock()
	kittest.AssertDeclaredResources(t, b.input())
}

func TestAssertDeclaredResources_ReportsOneErrorPerProblem(t *testing.T) {
	leaky := func() kittest.DeclaredResources {
		b := newCheckedBlock()
		b.instance.LeakOnDelete = true
		b.network.ClobberOnUpgrade = true
		return b.input()
	}
	want := kittest.CheckDeclaredResources(leaky())
	if len(want) == 0 {
		t.Fatal("the leaky block must have problems for this test to prove anything")
	}

	got := runAndCollect(func(tb testing.TB) { kittest.AssertDeclaredResources(tb, leaky()) })
	if !got.failed {
		t.Fatal("AssertDeclaredResources did not fail for a block with problems")
	}
	if !reflect.DeepEqual(got.logs, want) {
		t.Errorf("AssertDeclaredResources reported %q, want one error per problem %q", got.logs, want)
	}
}

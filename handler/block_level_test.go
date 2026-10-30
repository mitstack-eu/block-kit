// A mitstack/v1beta block is served at block level: one Ensure or Delete for the whole block, then each declared resource settled through its own Observer.
package handler_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/verify/kittest"
)

func v1betaManifest(id string, resources []string, provides ...string) string {
	m := "apiVersion: mitstack/v1beta\ntype: block\nmetadata:\n  id: " + id + "\n  name: " + id + "\nspec:\n  version: 1.0.0\n  resources:\n    - " + strings.Join(resources, "\n    - ") + "\n"
	if len(provides) > 0 {
		m += "  provides: [" + strings.Join(provides, ", ") + "]\n"
	}
	return m + "  lifecycle:\n    endpoint: https://" + id + ".example/hook\n    operations: [subscribe, unsubscribe, upgrade, validate]\n    contract: kit/v1\n"
}

var (
	blockLevelManifest = v1betaManifest("block-level", []string{"aws.vpc", "aws.ec2-instance", "{ name: a, type: aws.iam-role }", "{ name: b, type: aws.iam-role }"}, "aws.ec2-instance", "a")
	capabilityManifest = strings.Replace(blockLevelManifest, "type: block\n", "type: capability\n", 1)
	onlyAManifest      = v1betaManifest("only-a", []string{"{ name: a, type: aws.iam-role }"}, "a")
	roleBlockManifest  = v1betaManifest("role-block",
		[]string{"{ name: deploy-role, type: aws.iam-role }", "{ name: ecr-push-role, type: aws.iam-role }", "{ name: audit-role, type: aws.iam-role }"},
		"deploy-role", "ecr-push-role")
)

var declaredTypes = map[string]string{"aws.vpc": "aws.vpc", "aws.ec2-instance": "aws.ec2-instance", "a": "aws.iam-role", "b": "aws.iam-role"}

var blockOutputs = map[string]map[string]string{
	"aws.vpc":          {"vpcId": "vpc-0abc"},
	"aws.ec2-instance": {"publicIp": "203.0.113.7"},
	"a":                {"aOut": "from-a"},
	"b":                {"bOut": "from-b"},
}

type fakeCloud struct {
	mu                              sync.Mutex
	present, neverPresent, standing map[string]bool
}

func newFakeCloud() *fakeCloud {
	return &fakeCloud{present: map[string]bool{}, neverPresent: map[string]bool{}, standing: map[string]bool{}}
}

func standingCloud() *fakeCloud {
	c := newFakeCloud()
	c.createAll()
	return c
}

func (c *fakeCloud) createAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for name := range declaredTypes {
		if !c.neverPresent[name] {
			c.present[name] = true
		}
	}
}

func (c *fakeCloud) deleteAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for name := range declaredTypes {
		if !c.standing[name] {
			delete(c.present, name)
		}
	}
}

func (c *fakeCloud) stands(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.present[name]
}

type blockResource struct {
	cloud                    *fakeCloud
	ensureCalls, deleteCalls int
	deleteOutputs            map[string]string
	deleteErr                error
	deleted                  []contract.Desired
	clock                    *tickingClock
	ensuredAt, deletedAt     time.Time
}

func (b *blockResource) Ensure(ctx context.Context, d contract.Desired) error {
	b.ensureCalls++
	b.cloud.createAll()
	if b.clock != nil {
		b.ensuredAt = b.clock.advance()
	}
	return nil
}

func (b *blockResource) Delete(ctx context.Context, d contract.Desired) (map[string]string, error) {
	b.deleteCalls++
	if b.deleteErr != nil {
		return nil, b.deleteErr
	}
	b.deleted = append(b.deleted, d)
	b.cloud.deleteAll()
	if b.clock != nil {
		b.deletedAt = b.clock.advance()
	}
	return b.deleteOutputs, nil
}

func (b *blockResource) calls() [2]int { return [2]int{b.ensureCalls, b.deleteCalls} }

type reportingBlock struct {
	*blockResource
	report []contract.CreatedResource
}

func (r reportingBlock) EnsureResources(ctx context.Context, d contract.Desired) ([]contract.CreatedResource, error) {
	if err := r.Ensure(ctx, d); err != nil {
		return nil, err
	}
	return r.report, nil
}

type keepingBlock struct {
	*blockResource
	kept map[string][]contract.Kept
}

func (k keepingBlock) DeleteKeeping(ctx context.Context, d contract.Desired) (map[string]string, map[string][]contract.Kept, error) {
	outputs, err := k.Delete(ctx, d)
	return outputs, k.kept, err
}

type planningBlock struct {
	*blockResource
	replaces []string
}

func (p planningBlock) PlanReplacements(ctx context.Context, d contract.Desired) ([]string, error) {
	return p.replaces, nil
}

var (
	_ handler.ResourceReporter   = reportingBlock{}
	_ handler.ReplacementPlanner = planningBlock{}
	_ handler.Observer           = (*observer)(nil)
)

type observer struct {
	name  string
	cloud *fakeCloud
	obs   contract.Observation
	err   error
	mu    sync.Mutex
	refs  []contract.ResourceRef
}

func (o *observer) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	o.mu.Lock()
	o.refs = append(o.refs, r)
	o.mu.Unlock()
	switch {
	case o.err != nil:
		return contract.Observation{}, o.err
	case o.cloud == nil:
		return o.obs, nil
	case o.cloud.stands(o.name):
		return contract.Observation{State: contract.Present, Outputs: blockOutputs[o.name]}, nil
	default:
		return contract.Observation{State: contract.Absent}, nil
	}
}

func (o *observer) calls() []contract.ResourceRef {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]contract.ResourceRef(nil), o.refs...)
}

func fixed(obs contract.Observation) *observer { return &observer{obs: obs} }

func blockObservers(cloud *fakeCloud) map[string]*observer {
	out := map[string]*observer{}
	for name := range declaredTypes {
		out[name] = &observer{name: name, cloud: cloud}
	}
	return out
}

func asObservers(obs map[string]*observer) map[string]handler.Observer {
	out := make(map[string]handler.Observer, len(obs))
	for name, o := range obs {
		out[name] = o
	}
	return out
}

type blockCall struct {
	ctx    context.Context
	r      handler.Resource
	obs    map[string]handler.Observer
	op     string
	fields map[string]any
	config []func(*handler.Config)
}

func serveBlock(t *testing.T, c blockCall) (int, contract.Response) {
	t.Helper()
	config := append([]func(*handler.Config){withManifest(blockLevelManifest), func(cfg *handler.Config) { cfg.Observers = c.obs }}, c.config...)
	x := newHarness(t, c.r, config...)
	if c.ctx == nil {
		c.ctx = context.Background()
	}
	status, raw := x.postContext(c.ctx, map[string]any{"operation": c.op}, c.fields)
	assertConforms(t, raw)
	return status, decodeResponse(t, raw)
}

func recordedAll() map[string]any {
	return map[string]any{"recordedResources": map[string]any{
		"aws.vpc":          map[string]any{"type": "aws.vpc", "identity": map[string]any{"vpcId": "vpc-0abc"}},
		"aws.ec2-instance": map[string]any{"type": "aws.ec2-instance", "identity": map[string]any{"instanceId": "i-0abc"}},
		"a":                map[string]any{"type": "aws.iam-role", "identity": map[string]any{"roleName": "a-role"}},
		"b":                map[string]any{"type": "aws.iam-role", "identity": map[string]any{"roleName": "b-role"}},
	}}
}

func wantEveryResourceObserved(t *testing.T, resp contract.Response, state contract.State) {
	t.Helper()
	got := make([]string, 0, len(resp.Resources))
	for name := range resp.Resources {
		got = append(got, name)
	}
	slices.Sort(got)
	if want := []string{"a", "aws.ec2-instance", "aws.vpc", "b"}; !slices.Equal(got, want) {
		t.Errorf("resources = %q, want one entry per declared resource %q", got, want)
	}
	for name, typ := range declaredTypes {
		o := resp.Resources[name].Observe
		if o == nil || o.State != state || o.Type != typ || o.Error != "" {
			t.Errorf("resource %q observe = %+v, want state %s, type %s and no error", name, o, state, typ)
		}
	}
}

func wantOK(t *testing.T, status int, resp contract.Response) {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; response %+v", status, resp)
	}
}

func wantAbsent(t *testing.T, resp contract.Response, want bool) {
	t.Helper()
	if resp.Absent == nil || *resp.Absent != want {
		t.Errorf("absent = %v, want %v; statusDetail %q", resp.Absent, want, resp.StatusDetail)
	}
}

func wantRefusal(t *testing.T, status int, resp contract.Response, class string, names ...string) {
	t.Helper()
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; response %+v", status, resp)
	}
	if class != "" {
		if got := handler.RefusedClass(resp.StatusDetail); got != class {
			t.Errorf("refusal class = %q, want %s", got, class)
		}
	}
	for _, name := range names {
		if !strings.Contains(resp.Error, name) {
			t.Errorf("error = %q, want it to name %s", resp.Error, name)
		}
	}
}

func TestBlockLevel_SubscribeAndUpgradeRunOneEnsureThenObserveEachResourceByName(t *testing.T) {
	for _, tc := range []struct{ name, manifest, op string }{
		{"block subscribe", blockLevelManifest, "subscribe"}, {"block upgrade", blockLevelManifest, "upgrade"},
		{"capability subscribe", capabilityManifest, "subscribe"}, {"capability upgrade", capabilityManifest, "upgrade"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cloud := newFakeCloud()
			block, obs := &blockResource{cloud: cloud}, blockObservers(cloud)

			status, resp := serveBlock(t, blockCall{r: block, obs: asObservers(obs), op: tc.op, config: []func(*handler.Config){withManifest(tc.manifest)}})

			if status != http.StatusOK || resp.Pending {
				t.Fatalf("status, pending = %d, %v; want 200 and settled, every resource stands; response %+v", status, resp.Pending, resp)
			}
			if got := block.calls(); got != [2]int{1, 0} {
				t.Errorf("block Ensure, Delete calls = %v, want one Ensure for the whole block", got)
			}
			wantEveryResourceObserved(t, resp, contract.Present)
			for name, o := range obs {
				refs := o.calls()
				if len(refs) == 0 || refs[0].Name != name || refs[0].Type != declaredTypes[name] || refs[0].Desired.SubscriptionID != "sub-1" {
					t.Errorf("resource %q observer handed %+v, want name %q, type %q, subscription sub-1", name, refs, name, declaredTypes[name])
				}
			}
			if want := map[string]string{"publicIp": "203.0.113.7", "aOut": "from-a"}; !reflect.DeepEqual(resp.Outputs, want) {
				t.Errorf("outputs = %v, want %v: the union of the provided resources' outputs alone", resp.Outputs, want)
			}
		})
	}
}

// Each observe is handed the reported identity and type, else the recorded ones, else no identity and the entry's one type.
func TestBlockLevel_EachObserveIsHandedTheIdentityAndTypeOfItsResource(t *testing.T) {
	reported := []contract.CreatedResource{
		{Name: "aws.vpc", Type: "aws.vpc", Identity: map[string]string{"vpcId": "vpc-new"}},
		{Name: "aws.ec2-instance", Type: "aws.ec2-instance", Identity: map[string]string{"instanceId": "i-new"}},
		{Name: "a", Type: "aws.iam-role", Identity: map[string]string{"roleName": "a-new"}},
		{Name: "b", Type: "aws.iam-role", Identity: map[string]string{"roleName": "b-new"}},
	}
	cases := []struct {
		label     string
		block     func(cloud *fakeCloud) handler.Resource
		fields    map[string]any
		wantB     map[string]string
		wantEntry bool
	}{
		{"reported by the block", func(c *fakeCloud) handler.Resource {
			return reportingBlock{blockResource: &blockResource{cloud: c}, report: reported}
		}, recordedAll(), map[string]string{"roleName": "b-new"}, true},
		{"recorded by the engine", func(c *fakeCloud) handler.Resource { return &blockResource{cloud: c} }, recordedAll(), map[string]string{"roleName": "b-role"}, true},
		{"neither", func(c *fakeCloud) handler.Resource { return &blockResource{cloud: c} }, nil, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			cloud := newFakeCloud()
			obs := blockObservers(cloud)

			status, resp := serveBlock(t, blockCall{r: tc.block(cloud), obs: asObservers(obs), op: "upgrade", fields: tc.fields})

			wantOK(t, status, resp)
			refs := obs["b"].calls()
			if len(refs) == 0 {
				t.Fatal("b was never observed")
			}
			if r := refs[0]; r.Type != "aws.iam-role" || !(reflect.DeepEqual(r.Identity, tc.wantB) || len(r.Identity)+len(tc.wantB) == 0) {
				t.Errorf("b observer handed type %q, identity %v; want aws.iam-role, %v", r.Type, r.Identity, tc.wantB)
			}
			if o := resp.Resources["b"].Observe; tc.wantEntry && (o == nil || !reflect.DeepEqual(o.Identity, tc.wantB)) {
				t.Errorf("b observe entry = %+v, want identity %v", o, tc.wantB)
			}
		})
	}
}

func TestBlockLevel_APendingSubscribeOrUpgradeNamesTheResourceNotPresentAndCarriesEveryReportedIdentity(t *testing.T) {
	reported := []contract.CreatedResource{
		{Name: "aws.vpc", Type: "aws.vpc", Identity: map[string]string{"vpcId": "vpc-new"}},
		{Name: "b", Type: "aws.iam-role", Identity: map[string]string{"roleName": "b-new"}},
	}
	for _, op := range []string{"subscribe", "upgrade"} {
		t.Run(op, func(t *testing.T) {
			cloud := newFakeCloud()
			cloud.neverPresent["aws.ec2-instance"] = true
			block, obs := reportingBlock{blockResource: &blockResource{cloud: cloud}, report: reported}, blockObservers(cloud)

			status, resp := serveBlock(t, blockCall{r: block, obs: asObservers(obs), op: op})

			if want := `resource "aws.ec2-instance" observed absent, not yet present`; status != http.StatusOK || !resp.Pending || resp.StatusDetail != want || len(obs["aws.ec2-instance"].calls()) != 1 {
				t.Fatalf("status, pending, statusDetail = %d, %v, %q after %d observes of aws.ec2-instance; want 200, pending, %q after one observe",
					status, resp.Pending, resp.StatusDetail, len(obs["aws.ec2-instance"].calls()), want)
			}
			for _, c := range reported {
				if got, want := resp.Resources[c.Name].Ensure, (&contract.CallOutcome{Type: c.Type, Identity: c.Identity}); !reflect.DeepEqual(got, want) {
					t.Errorf("resource %q ensure = %+v, want %+v", c.Name, got, want)
				}
			}
			vpc, ec2, b := resp.Resources["aws.vpc"].Observe, resp.Resources["aws.ec2-instance"].Observe, resp.Resources["b"].Observe
			if vpc == nil || vpc.State != contract.Present || ec2 == nil || ec2.State != contract.Absent || b != nil {
				t.Errorf("observe of aws.vpc, aws.ec2-instance, b = %+v, %+v, %+v; want aws.vpc present, aws.ec2-instance absent and b unreached", vpc, ec2, b)
			}
		})
	}
}

func TestBlockLevel_RefusesWhatTheManifestDoesNotDeclare(t *testing.T) {
	report := func(c contract.CreatedResource) func(*fakeCloud) handler.Resource {
		return func(cloud *fakeCloud) handler.Resource {
			return reportingBlock{blockResource: &blockResource{cloud: cloud}, report: []contract.CreatedResource{c}}
		}
	}
	plan := func(name string) func(*fakeCloud) handler.Resource {
		return func(cloud *fakeCloud) handler.Resource {
			return planningBlock{blockResource: &blockResource{cloud: cloud}, replaces: []string{name}}
		}
	}
	plain := func(cloud *fakeCloud) handler.Resource { return &blockResource{cloud: cloud} }
	cases := []struct {
		label  string
		block  func(*fakeCloud) handler.Resource
		op     string
		fields map[string]any
		class  string
		names  []string
	}{
		{"a reported name the manifest does not declare", report(contract.CreatedResource{Name: "c", Type: "aws.iam-role"}), "subscribe", nil, "", []string{`"c"`}},
		{"a reported type its entry does not list", report(contract.CreatedResource{Name: "b", Type: "aws.vpc"}), "subscribe", nil, "", []string{`"b"`, "aws.vpc"}},
		{"a planned replacement the manifest does not declare", plan("c"), "validate", nil, "", []string{`"c"`}},
		{"a validate naming an undeclared resource", plain, "validate", map[string]any{"resource": "c"}, "undeclared-resource", nil},
		{"a subscribe naming one resource", plain, "subscribe", map[string]any{"resource": "b"}, "", []string{`"b"`, "block-level handler"}},
		{"an upgrade naming one resource", plain, "upgrade", map[string]any{"resource": "b"}, "", []string{`"b"`, "block-level handler"}},
		{"an unsubscribe naming one resource", plain, "unsubscribe", map[string]any{"resource": "b"}, "", []string{`"b"`, "block-level handler"}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			cloud := standingCloud()
			block := tc.block(cloud)

			status, resp := serveBlock(t, blockCall{r: block, obs: asObservers(blockObservers(cloud)), op: tc.op, fields: tc.fields})

			wantRefusal(t, status, resp, tc.class, tc.names...)
			if b, ok := block.(*blockResource); ok && b.ensureCalls+b.deleteCalls != 0 {
				t.Errorf("a refused %s ran Ensure %d and Delete %d times", tc.op, b.ensureCalls, b.deleteCalls)
			}
		})
	}
}

func TestBlockLevel_UnsubscribeRunsOneDeleteHandedTheRequestThenProvesEachResourceAbsentAndMergesTheDeletesOutputs(t *testing.T) {
	cloud := standingCloud()
	block, obs := &blockResource{cloud: cloud, deleteOutputs: map[string]string{"dataSnapshotId": "snap-0abc"}}, blockObservers(cloud)
	fields := recordedAll()
	fields["params"] = map[string]string{"dataHandover": "snapshot"}
	fields["secrets"], fields["priorOutputs"] = map[string]any{"hetzner": map[string]any{"token": "hc-token"}}, map[string]string{"volumeId": "vol-1"}

	status, resp := serveBlock(t, blockCall{r: block, obs: asObservers(obs), op: "unsubscribe", fields: fields})

	wantOK(t, status, resp)
	if got := block.calls(); got != [2]int{0, 1} || block.deleted[0].Params["dataHandover"] != "snapshot" || block.deleted[0].Secrets["hetzner"]["token"] != "hc-token" || block.deleted[0].PriorOutputs["volumeId"] != "vol-1" {
		t.Errorf("block Ensure, Delete calls = %v handed %+v, want one Delete for the whole block carrying the request's params, secrets and prior outputs", got, block.deleted)
	}
	wantAbsent(t, resp, true)
	wantEveryResourceObserved(t, resp, contract.Absent)
	if refs := obs["b"].calls(); len(refs) == 0 || !reflect.DeepEqual(refs[0].Identity, map[string]string{"roleName": "b-role"}) || refs[0].Type != "aws.iam-role" || refs[0].Desired.Params["dataHandover"] != "snapshot" {
		t.Errorf("b observer handed %+v, want its recorded identity and type and the request's params", refs)
	}
	if got := resp.Outputs["dataSnapshotId"]; got != "snap-0abc" {
		t.Errorf(`outputs["dataSnapshotId"] = %q, want the snap-0abc the block's Delete returned; outputs %v`, got, resp.Outputs)
	}
}

func TestBlockLevel_UnsubscribeAnswersEachFileTheBlocksDeleteKeptOnTheDeleteOfTheResourceThatLandedIt(t *testing.T) {
	cloud := standingCloud()
	kept := []contract.Kept{{Repository: "acme/weather", Path: "CLAUDE.md", Reason: "changed"}}
	block := keepingBlock{&blockResource{cloud: cloud}, map[string][]contract.Kept{"b": kept}}

	status, resp := serveBlock(t, blockCall{r: block, obs: asObservers(blockObservers(cloud)), op: "unsubscribe", fields: recordedAll()})

	wantOK(t, status, resp)
	wantAbsent(t, resp, true)
	if d := resp.Resources["b"].Delete; d == nil || !reflect.DeepEqual(d.Kept, kept) {
		t.Errorf("resource \"b\" delete = %+v, want kept %+v", d, kept)
	}
	if d := resp.Resources["a"].Delete; d != nil {
		t.Errorf("resource \"a\" delete = %+v, want none, since the block kept nothing of it", d)
	}
}

type teardownReportingBlock struct {
	*blockResource
	asked int
}

func (b *teardownReportingBlock) TeardownDetail(context.Context, contract.Desired) (string, error) {
	b.asked++
	return "left alone, since their tags name another subscription: parameter/decoy", nil
}

func TestBlockLevel_AnUnsubscribeOrValidateAnswersTheBlocksTeardownDetailOnlyOnceItProvesEveryResourceAbsent(t *testing.T) {
	for _, op := range []string{"unsubscribe", "validate"} {
		for _, standing := range []bool{false, true} {
			cloud := standingCloud()
			cloud.standing["b"] = standing
			if op == "validate" {
				cloud.deleteAll()
			}
			block := &teardownReportingBlock{blockResource: &blockResource{cloud: cloud}}

			status, resp := serveBlock(t, blockCall{r: block, obs: asObservers(blockObservers(cloud)), op: op, fields: recordedAll()})

			wantOK(t, status, resp)
			asks := 1
			if standing {
				asks = 0
			}
			if absent := resp.Absent != nil && *resp.Absent; absent == standing || block.asked != asks || strings.Contains(resp.StatusDetail, "parameter/decoy") == standing {
				t.Errorf("%s with b standing %t: absent %v, statusDetail = %q after %d ask(s), want the block's detail, asked once, only when every resource is absent", op, standing, absent, resp.StatusDetail, block.asked)
			}
		}
	}
}

func TestBlockLevel_RefusesAKeptFileOfAResourceTheManifestDoesNotDeclare(t *testing.T) {
	cloud := standingCloud()
	block := keepingBlock{&blockResource{cloud: cloud}, map[string][]contract.Kept{"ghost": {{Repository: "acme/weather", Path: "CLAUDE.md", Reason: "changed"}}}}

	status, resp := serveBlock(t, blockCall{r: block, obs: asObservers(blockObservers(cloud)), op: "unsubscribe", fields: recordedAll()})

	wantRefusal(t, status, resp, "undeclared-resource", "ghost")
}

func TestBlockLevel_UnsubscribeLeavesAResourceRecordedAbsentUnobservedAndHandsTheBlockItsState(t *testing.T) {
	cloud := standingCloud()
	obs := blockObservers(cloud)
	obs["aws.ec2-instance"] = &observer{err: errors.New("the instance does not answer")}
	block := &blockResource{cloud: cloud}
	fields := recordedAll()
	fields["recordedResources"].(map[string]any)["aws.ec2-instance"] = map[string]any{
		"type": "aws.ec2-instance", "identity": map[string]any{"instanceId": "i-0abc"}, "state": "absent"}

	status, resp := serveBlock(t, blockCall{r: block, obs: asObservers(obs), op: "unsubscribe", fields: fields})

	wantOK(t, status, resp)
	wantAbsent(t, resp, true)
	if calls := obs["aws.ec2-instance"].calls(); len(calls) != 0 {
		t.Errorf("the observer of the resource recorded absent was called %d times, want none", len(calls))
	}
	if o, ok := resp.Resources["aws.ec2-instance"]; ok {
		t.Errorf("resource \"aws.ec2-instance\" outcome = %+v, want none for a resource recorded absent", o)
	}
	for _, name := range []string{"aws.vpc", "a", "b"} {
		if o := resp.Resources[name].Observe; o == nil || o.State != contract.Absent {
			t.Errorf("resource %q observe = %+v, want it still observed absent", name, o)
		}
	}
	if len(block.deleted) != 1 || block.deleted[0].RecordedResources["aws.ec2-instance"].State != contract.Absent {
		t.Errorf("the block's Delete was handed %+v, want the record of \"aws.ec2-instance\" reading state absent", block.deleted)
	}
}

func TestBlockLevel_AnUnsubscribeLeavingAResourceStandingAnswersAbsentFalseNamingItWithNoneOfTheDeletesOutputs(t *testing.T) {
	cloud := standingCloud()
	cloud.standing["b"] = true
	block := &blockResource{cloud: cloud, deleteOutputs: map[string]string{"dataSnapshotId": "snap-0abc"}}

	obs := blockObservers(cloud)

	status, resp := serveBlock(t, blockCall{r: block, obs: asObservers(obs), op: "unsubscribe", fields: recordedAll()})

	wantOK(t, status, resp)
	wantAbsent(t, resp, false)
	if want := `resource "b" observed present, not yet absent`; resp.StatusDetail != want || len(resp.Outputs) != 0 || len(obs["b"].calls()) != 1 {
		t.Errorf("statusDetail = %q with outputs %v after %d observes of b, want %q after one observe and no output until the block settles absent", resp.StatusDetail, resp.Outputs, len(obs["b"].calls()), want)
	}
}

func TestBlockLevel_ValidateObservesEachResourceOnceAndNamesTheReplacementsTheBlockPlans(t *testing.T) {
	cloud := standingCloud()
	block, obs := planningBlock{blockResource: &blockResource{cloud: cloud}, replaces: []string{"b"}}, blockObservers(cloud)

	status, resp := serveBlock(t, blockCall{r: block, obs: asObservers(obs), op: "validate", fields: recordedAll()})

	wantOK(t, status, resp)
	for name, o := range obs {
		if got := len(o.calls()); got != 1 {
			t.Errorf("resource %q observed %d times, want once", name, got)
		}
	}
	if got := block.calls(); got != [2]int{} {
		t.Errorf("validate ran block Ensure, Delete %v times, want none", got)
	}
	wantEveryResourceObserved(t, resp, contract.Present)
	if !slices.Equal(resp.Replaces, []string{"b"}) {
		t.Errorf("replaces = %q, want [b]", resp.Replaces)
	}
}

// The engine observes by the type it recorded, which an older manifest may list and the current one not, so that type reaches the declared observer rather than a refusal or a type observer.
func TestBlockLevel_ObservesOneResourceThroughItsDeclaredObserverByTheTypeAndIdentityTheRequestOrRecordNames(t *testing.T) {
	recordedBAsVPC := recordedAll()
	recordedBAsVPC["recordedResources"].(map[string]any)["b"] = map[string]any{"type": "aws.vpc", "identity": map[string]any{"vpcId": "vpc-old"}}
	cases := []struct {
		label    string
		op       string
		fields   map[string]any
		typ      string
		identity map[string]string
		alone    bool
	}{
		{"a validate naming b observes it alone with the request's identity and type", "validate",
			map[string]any{"resource": "b", "resourceIdentity": map[string]any{"roleName": "b-role"}, "resourceType": "aws.iam-role"}, "aws.iam-role", map[string]string{"roleName": "b-role"}, true},
		{"a validate naming b hands the observer a requested type its entry does not list", "validate",
			map[string]any{"resource": "b", "resourceType": "aws.vpc"}, "aws.vpc", nil, true},
		{"an unsubscribe hands the observer a recorded type its entry does not list", "unsubscribe", recordedBAsVPC, "aws.vpc", map[string]string{"vpcId": "vpc-old"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			cloud := standingCloud()
			block, obs, typed := &blockResource{cloud: cloud}, blockObservers(cloud), fixed(contract.Observation{State: contract.Present})
			byType := map[string]handler.Observer{"aws.iam-role": typed, "aws.vpc": typed}

			status, resp := serveBlock(t, blockCall{r: block, obs: asObservers(obs), op: tc.op, fields: tc.fields, config: []func(*handler.Config){func(c *handler.Config) { c.TypeObservers = byType }}})

			wantOK(t, status, resp)
			refs := obs["b"].calls()
			if len(refs) == 0 || refs[0].Name != "b" || refs[0].Type != tc.typ || !reflect.DeepEqual(refs[0].Identity, tc.identity) || len(typed.calls()) != 0 {
				t.Errorf("b observer handed %+v and the type observer %d refs, want b as %s with identity %v through its declared observer alone", refs, len(typed.calls()), tc.typ, tc.identity)
			}
			if !tc.alone {
				return
			}
			if len(refs) != 1 {
				t.Errorf("b observed %d times, want once", len(refs))
			}
			for name, o := range obs {
				if name != "b" && len(o.calls()) != 0 {
					t.Errorf("resource %q was observed, but the request names b alone", name)
				}
			}
			if got := block.calls(); got != [2]int{} {
				t.Errorf("the block-level handler ran Ensure, Delete %v times; want none for an observe of one resource", got)
			}
			o := resp.Resources["b"].Observe
			if len(resp.Resources) != 1 || o == nil || o.State != contract.Present || o.Type != tc.typ || !reflect.DeepEqual(o.Identity, tc.identity) {
				t.Errorf("resources = %+v, want b's entry alone, present as %s with identity %v", resp.Resources, tc.typ, tc.identity)
			}
		})
	}
}

type tickingClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *tickingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *tickingClock) advance() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Second)
	return c.now
}

// Each observe is handed the clock's reading once the block-level call returned, since it settles that verb, and a validate the settlesAt it is sent.
func TestBlockLevel_ObservesAreHandedTheTimeOfTheVerbTheySettleAndWhetherItIsAnUnsubscribe(t *testing.T) {
	settles := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	for _, op := range []string{"subscribe", "upgrade", "unsubscribe", "validate"} {
		t.Run(op, func(t *testing.T) {
			cloud := newFakeCloud()
			if op == "unsubscribe" || op == "validate" {
				cloud.createAll()
			}
			clock := &tickingClock{now: time.Now()}
			block, obs := &blockResource{cloud: cloud, clock: clock}, blockObservers(cloud)
			fields := recordedAll()
			fields["settlesAt"] = settles.Format(time.RFC3339)

			status, resp := serveBlock(t, blockCall{r: block, obs: asObservers(obs), op: op, fields: fields,
				config: []func(*handler.Config){func(c *handler.Config) { c.Now = clock.Now }}})

			wantOK(t, status, resp)
			want := map[string]time.Time{"subscribe": block.ensuredAt, "upgrade": block.ensuredAt, "unsubscribe": block.deletedAt, "validate": settles}[op]
			if want.IsZero() {
				t.Fatalf("the block-level call for %s never ran", op)
			}
			for name, o := range obs {
				if refs := o.calls(); len(refs) == 0 || !refs[0].Desired.VerbAt.Equal(want) {
					t.Errorf("resource %q observer handed %+v, want VerbAt %v", name, refs, want)
				}
				for _, r := range o.calls() {
					if r.Unsubscribing != (op == "unsubscribe") {
						t.Errorf("resource %q observed on %s with Unsubscribing %v, want it true on unsubscribe alone", name, op, r.Unsubscribing)
					}
				}
			}
		})
	}
}

func TestTypeObservers_ServeOnlyAValidateNamingAResourceTheManifestNoLongerDeclares(t *testing.T) {
	byRecordedType := map[string]any{"resource": "b", "resourceType": "aws.vpc", "resourceIdentity": map[string]any{"vpcId": "vpc-0abc"}}
	cases := []struct {
		label   string
		op      string
		fields  map[string]any
		state   contract.State
		refused bool
		names   []string
	}{
		{"a validate observed absent", "validate", byRecordedType, contract.Absent, false, nil},
		{"a validate observed present", "validate", byRecordedType, contract.Present, false, nil},
		{"a subscribe is refused", "subscribe", map[string]any{"resource": "b", "resourceType": "aws.vpc"}, contract.Present, true, nil},
		{"an upgrade is refused", "upgrade", map[string]any{"resource": "b", "resourceType": "aws.vpc"}, contract.Present, true, nil},
		{"an unsubscribe is refused", "unsubscribe", map[string]any{"resource": "b", "resourceType": "aws.vpc"}, contract.Present, true, nil},
		{"a validate with no type is refused naming the resource", "validate", map[string]any{"resource": "b"}, contract.Present, true, []string{`"b"`}},
		{"a validate of a type no observer holds is refused naming resource and type", "validate",
			map[string]any{"resource": "b", "resourceType": "aws.subnet"}, contract.Present, true, []string{`"b"`, "aws.subnet"}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			block, vpc := &blockResource{cloud: newFakeCloud()}, fixed(contract.Observation{State: tc.state})

			status, resp := serveBlock(t, blockCall{r: block, obs: map[string]handler.Observer{"a": &observer{name: "a", cloud: newFakeCloud()}}, op: tc.op, fields: tc.fields, config: []func(*handler.Config){
				withManifest(onlyAManifest), func(c *handler.Config) { c.TypeObservers = map[string]handler.Observer{"aws.vpc": vpc} }}})

			if got := block.calls(); got != [2]int{} {
				t.Errorf("block-level handler ran Ensure, Delete %v times; want none", got)
			}
			if tc.refused {
				wantRefusal(t, status, resp, "undeclared-resource", tc.names...)
				if n := len(vpc.calls()); n != 0 {
					t.Errorf("aws.vpc observer called %d times, want none", n)
				}
				return
			}
			wantOK(t, status, resp)
			refs := vpc.calls()
			if len(refs) != 1 || refs[0].Name != "b" || refs[0].Type != "aws.vpc" || !reflect.DeepEqual(refs[0].Identity, map[string]string{"vpcId": "vpc-0abc"}) {
				t.Errorf("aws.vpc observer handed %+v, want one ref to b as aws.vpc with identity vpcId vpc-0abc", refs)
			}
			if o := resp.Resources["b"].Observe; len(resp.Resources) != 1 || o == nil || o.State != tc.state {
				t.Errorf("resources = %+v, want b's entry alone with state %s", resp.Resources, tc.state)
			}
		})
	}
}

func TestBlockLevel_AnObserveErrorIsReportedUnderItsResourceWithNoAbsenceClaimed(t *testing.T) {
	for _, op := range []string{"subscribe", "unsubscribe", "validate"} {
		t.Run(op, func(t *testing.T) {
			cloud := newFakeCloud()
			obs := asObservers(blockObservers(cloud))
			obs["b"] = &observer{err: errors.New("describe role: throttled")}

			status, resp := serveBlock(t, blockCall{r: &blockResource{cloud: cloud}, obs: obs, op: op})

			if status != http.StatusInternalServerError || resp.Absent != nil {
				t.Fatalf("status = %d, absent = %v; want 500 for an unclassified observe error and no absence claimed", status, resp.Absent)
			}
			if o := resp.Resources["b"].Observe; o == nil || o.Error != "describe role: throttled" {
				t.Errorf("b observe entry = %+v, want the observe error", o)
			}
		})
	}
}

func roleObservers(arns map[string]string) map[string]handler.Observer {
	obs := map[string]handler.Observer{}
	for name, arn := range arns {
		var secrets map[string]string
		if arn != "" {
			secrets = map[string]string{"roleArn": arn}
		}
		obs[name] = fixed(contract.Observation{State: contract.Present, SecretOutputs: secrets})
	}
	return obs
}

func roleArns() map[string]string {
	return map[string]string{"deploy-role": "arn:aws:iam::1:role/deploy", "ecr-push-role": "arn:aws:iam::1:role/ecr-push", "audit-role": "arn:aws:iam::1:role/audit"}
}

func serveRoleBlock(t *testing.T, ctx context.Context, op string, obs map[string]handler.Observer) (int, contract.Response) {
	t.Helper()
	return serveBlock(t, blockCall{ctx: ctx, r: &blockResource{cloud: newFakeCloud()}, obs: obs, op: op, config: []func(*handler.Config){withManifest(roleBlockManifest)}})
}

func TestBlockLevel_AnswerKeysEachProvidedResourcesSecretOutputsByResourceNameAndRecordsNoneOfThem(t *testing.T) {
	cases := []struct {
		label string
		op    string
		arns  map[string]string
		want  map[string]map[string]string
	}{
		{"subscribe", "subscribe", roleArns(), map[string]map[string]string{"deploy-role": {"roleArn": "arn:aws:iam::1:role/deploy"}, "ecr-push-role": {"roleArn": "arn:aws:iam::1:role/ecr-push"}}},
		{"upgrade", "upgrade", roleArns(), map[string]map[string]string{"deploy-role": {"roleArn": "arn:aws:iam::1:role/deploy"}, "ecr-push-role": {"roleArn": "arn:aws:iam::1:role/ecr-push"}}},
		{"validate", "validate", roleArns(), map[string]map[string]string{"deploy-role": {"roleArn": "arn:aws:iam::1:role/deploy"}, "ecr-push-role": {"roleArn": "arn:aws:iam::1:role/ecr-push"}}},
		{"a provided resource reporting no secret output has no entry", "subscribe",
			map[string]string{"deploy-role": "arn:aws:iam::1:role/deploy", "ecr-push-role": "", "audit-role": "arn:aws:iam::1:role/audit"},
			map[string]map[string]string{"deploy-role": {"roleArn": "arn:aws:iam::1:role/deploy"}}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			status, resp := serveRoleBlock(t, context.Background(), tc.op, roleObservers(tc.arns))

			wantOK(t, status, resp)
			if !reflect.DeepEqual(resp.ResourceSecretOutputs, tc.want) {
				t.Errorf("ResourceSecretOutputs = %v, want %v: provided resources only, none for audit-role", resp.ResourceSecretOutputs, tc.want)
			}
			if wantMerged := tc.want["ecr-push-role"]["roleArn"]; wantMerged != "" && resp.SecretOutputs["roleArn"] != wantMerged {
				t.Errorf(`merged SecretOutputs["roleArn"] = %q, want the later provided resource's %q`, resp.SecretOutputs["roleArn"], wantMerged)
			}
			if len(resp.Resources) != 3 {
				t.Errorf("Resources has %d entries, want one per declared resource: %+v", len(resp.Resources), resp.Resources)
			}
			for name, o := range resp.Resources {
				if want := (contract.ResourceOutcome{Observe: &contract.CallOutcome{State: contract.Present, Type: "aws.iam-role"}}); !reflect.DeepEqual(o, want) {
					t.Errorf("resource %q outcome = %+v, want %+v, carrying no secret output value", name, o, want)
				}
			}
		})
	}
}

// cancellingObserver cancels the request context once it reports settledOn, so the verb sees a done context only after this resource settled.
type cancellingObserver struct {
	handler.Observer
	settledOn contract.State
	cancel    context.CancelFunc
}

func (o cancellingObserver) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	obs, err := o.Observer.ObserveResource(ctx, r)
	if err == nil && obs.State == o.settledOn {
		o.cancel()
	}
	return obs, err
}

func TestBlockLevel_APendingAnswerCarriesTheSecretOutputsAlreadyObservedPresentAndAValidateTheFinishedWorkOfAnUnknownResource(t *testing.T) {
	const deployArn = "arn:aws:iam::1:role/deploy"
	for _, tc := range []struct {
		name, op string
		ended    bool
	}{
		{"subscribe with a later resource unknown", "subscribe", false},
		{"validate with a later resource unknown", "validate", false},
		{"subscribe ended before the later resource", "subscribe", true},
		{"validate ended before the later resource", "validate", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var deploy handler.Observer = fixed(contract.Observation{State: contract.Present, SecretOutputs: map[string]string{"roleArn": deployArn}})
			if tc.ended {
				deploy = cancellingObserver{Observer: deploy, settledOn: contract.Present, cancel: cancel}
			}
			obs := roleObservers(map[string]string{"audit-role": "arn:aws:iam::1:role/audit"})
			obs["deploy-role"], obs["ecr-push-role"] = deploy, fixed(contract.Observation{State: contract.Unknown, Redrive: true})

			status, resp := serveRoleBlock(t, ctx, tc.op, obs)

			if status != http.StatusOK || !resp.Pending {
				t.Fatalf("status, pending = %d, %v; want 200 and pending; response %+v", status, resp.Pending, resp)
			}
			if want := map[string]map[string]string{"deploy-role": {"roleArn": deployArn}}; !reflect.DeepEqual(resp.ResourceSecretOutputs, want) {
				t.Errorf("ResourceSecretOutputs = %v, want %v: deploy-role was observed present before the answer went pending", resp.ResourceSecretOutputs, want)
			}
			if want := map[string]string{"roleArn": deployArn}; !reflect.DeepEqual(resp.SecretOutputs, want) {
				t.Errorf("SecretOutputs = %v, want %v: the merged map the platform judges and stores by name", resp.SecretOutputs, want)
			}
			if want := tc.op == "validate" && !tc.ended; resp.Redrive != want {
				t.Errorf("redrive = %v, want %v: only a validate that observed the unknown resource reports its finished work", resp.Redrive, want)
			}
		})
	}
}

const contributionType = "github.scaffold-contribution"

func contributionSnapshot(t *testing.T) contract.TypeSnapshot {
	t.Helper()
	snap, err := contract.ParseTypeSnapshot([]byte(`{"version": 2, "types": [
  {"id": "github.scaffold-contribution", "capability": "github", "kind": "scaffold-contribution", "scope": "per-app", "endsWithSubscription": true, "version": 1},
  {"id": "aws.ec2-instance", "capability": "aws", "kind": "host", "scope": "per-app", "version": 1}]}`))
	if err != nil {
		t.Fatalf("ParseTypeSnapshot: %v", err)
	}
	return snap
}

func recordedContribution() map[string]any {
	return map[string]any{"recordedResources": map[string]any{
		contributionType:   map[string]any{"type": contributionType, "identity": map[string]any{"repository": "acme/weather"}},
		"aws.ec2-instance": map[string]any{"type": "aws.ec2-instance", "identity": map[string]any{"instanceId": "i-0abc"}},
	}}
}

func unsubscribeContribution(t *testing.T, block handler.Resource, types contract.TypeSnapshot, obs map[string]*observer, resources ...string) (int, contract.Response) {
	t.Helper()
	return serveBlock(t, blockCall{r: block, obs: asObservers(obs), op: "unsubscribe", fields: recordedContribution(),
		config: []func(*handler.Config){withManifest(v1betaManifest("contribution-block", resources)), func(c *handler.Config) { c.Types = types }}})
}

func standing() *observer { return fixed(contract.Observation{State: contract.Present}) }

func TestBlockLevel_UnsubscribeCountsAResourceOfAMarkedTypeAbsentOnceTheBlocksDeleteSucceeds(t *testing.T) {
	block, contribution := &blockResource{cloud: newFakeCloud()}, standing()

	status, resp := unsubscribeContribution(t, block, contributionSnapshot(t), map[string]*observer{contributionType: contribution}, contributionType)

	if status != http.StatusOK || block.deleteCalls != 1 {
		t.Fatalf("status = %d with %d block Delete calls, want 200 and 1; response %+v", status, block.deleteCalls, resp)
	}
	wantAbsent(t, resp, true)
	o := resp.Resources[contributionType]
	if o.Observe != nil || len(contribution.calls()) != 0 {
		t.Errorf("resource %q observe = %+v with %d observer calls, want none: its absence is not observed", contributionType, o.Observe, len(contribution.calls()))
	}
	if d := o.Delete; d == nil || d.State != contract.Absent || d.Type != contributionType || d.Error != "" {
		t.Errorf("resource %q delete = %+v, want state absent, type %s and no error", contributionType, d, contributionType)
	}
}

func TestBlockLevel_UnsubscribeStillAnswersAbsentFalseNamingAnUnmarkedResourceObservedPresent(t *testing.T) {
	obs := map[string]*observer{contributionType: standing(), "aws.ec2-instance": standing()}

	status, resp := unsubscribeContribution(t, &blockResource{cloud: newFakeCloud()}, contributionSnapshot(t), obs, "aws.ec2-instance", contributionType)

	wantOK(t, status, resp)
	wantAbsent(t, resp, false)
	if want := `resource "aws.ec2-instance" observed present, not yet absent`; resp.StatusDetail != want {
		t.Errorf("statusDetail = %q, want %q", resp.StatusDetail, want)
	}
	if o := resp.Resources["aws.ec2-instance"].Observe; o == nil || o.State != contract.Present {
		t.Errorf("resource \"aws.ec2-instance\" observe = %+v, want state present", o)
	}
	if d := resp.Resources[contributionType].Delete; d == nil || d.State != contract.Absent {
		t.Errorf("resource %q delete = %+v, want state absent", contributionType, d)
	}
}

func TestBlockLevel_UnsubscribeObservesAContributionUnlessTheBlocksDeleteSucceedsAndTheSnapshotMarksItsType(t *testing.T) {
	cases := []struct {
		label string
		block *blockResource
		types func(t *testing.T) contract.TypeSnapshot
	}{
		{"no snapshot", &blockResource{cloud: newFakeCloud()}, func(*testing.T) contract.TypeSnapshot { return contract.TypeSnapshot{} }},
		{"a snapshot leaving the type unmarked", &blockResource{cloud: newFakeCloud()}, func(*testing.T) contract.TypeSnapshot {
			return contract.TypeSnapshot{Version: 1, Types: []contract.SnapshotType{{ID: contributionType}}}
		}},
		{"the block's delete failing", &blockResource{cloud: newFakeCloud(), deleteErr: errors.New("the GitHub API answered 502")}, contributionSnapshot},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			contribution := standing()

			status, resp := unsubscribeContribution(t, tc.block, tc.types(t), map[string]*observer{contributionType: contribution}, contributionType)

			if resp.Absent != nil && *resp.Absent {
				t.Errorf("absent = true, want no absence claimed: nothing settles %s absent; status %d", contributionType, status)
			}
			if d := resp.Resources[contributionType].Delete; d != nil && d.State == contract.Absent {
				t.Errorf("resource %q delete = %+v, want it not counted absent", contributionType, *d)
			}
			if tc.block.deleteErr != nil {
				if status != http.StatusInternalServerError || len(contribution.calls()) != 0 {
					t.Errorf("status = %d after %d observes, want 500 before anything is observed, since the delete failed; response %+v", status, len(contribution.calls()), resp)
				}
				return
			}
			wantAbsent(t, resp, false)
			if len(contribution.calls()) == 0 {
				t.Errorf("contribution observer never called, want its absence observed")
			}
		})
	}
}

func TestBlockLevel_ScrubsAndRefusesARequestSecretInWhatTheBlockAnswers(t *testing.T) {
	const token = "ghp_block-level-leak-3e9d7c21"
	cases := []struct {
		label  string
		op     string
		obs    contract.Observation
		delete map[string]string
		status int
	}{
		{"a resource's statusDetail is scrubbed", "validate", contract.Observation{State: contract.Present, StatusDetail: "role assumed with " + token}, nil, http.StatusOK},
		{"an observed output carrying it is refused", "validate", contract.Observation{State: contract.Present, Outputs: map[string]string{"leaked": token}}, nil, http.StatusBadRequest},
		{"a subscribe's observed output carrying it is refused", "subscribe", contract.Observation{State: contract.Present, Outputs: map[string]string{"leaked": token}}, nil, http.StatusBadRequest},
		{"an observed identity carrying it is refused, as an output is", "validate", contract.Observation{State: contract.Present, Identity: map[string]string{"roleName": token}}, nil, http.StatusBadRequest},
		{"a delete output carrying it is refused", "unsubscribe", contract.Observation{}, map[string]string{"leaked": token}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			cloud := standingCloud()
			obs := asObservers(blockObservers(cloud))
			if tc.obs.State != "" {
				obs["a"] = fixed(tc.obs)
			}
			fields := recordedAll()
			fields["secrets"] = map[string]any{"github": map[string]any{"token": token}}

			status, resp := serveBlock(t, blockCall{r: &blockResource{cloud: cloud, deleteOutputs: tc.delete}, obs: obs, op: tc.op, fields: fields})

			kittest.AssertNoSecret(t, "the answer", resp.Error+resp.StatusDetail, token)
			if tc.status == http.StatusOK {
				if o := resp.Resources["a"].Observe; status != http.StatusOK || o == nil || o.StatusDetail != "role assumed with [REDACTED]" {
					t.Errorf("status %d, a observe = %+v; want 200 with its statusDetail scrubbed of the request secret", status, o)
				}
				return
			}
			wantRefusal(t, status, resp, "secret-in-output")
			if resp.Absent != nil && *resp.Absent {
				t.Error("absent = true, want no absence claimed beside a refused output")
			}
			if o := resp.Resources["a"].Observe; tc.op != "unsubscribe" && (o == nil || o.Error == "") {
				t.Errorf("a observe = %+v, want the refusal recorded under it", o)
			}
		})
	}
}

func TestBlockLevel_ALaterProvidedResourceWinsAnOutputBothProduce(t *testing.T) {
	cloud := standingCloud()
	obs := asObservers(blockObservers(cloud))
	obs["aws.ec2-instance"] = fixed(contract.Observation{State: contract.Present, Outputs: map[string]string{"shared": "from-instance"}})
	obs["a"] = fixed(contract.Observation{State: contract.Present, Outputs: map[string]string{"shared": "from-a"}})

	status, resp := serveBlock(t, blockCall{r: &blockResource{cloud: cloud}, obs: obs, op: "subscribe"})

	if status != http.StatusOK || resp.Outputs["shared"] != "from-a" {
		t.Errorf(`status %d, outputs["shared"] = %q; want 200 and from-a, since a is declared after aws.ec2-instance`, status, resp.Outputs["shared"])
	}
}

func TestBlockLevel_StopsBeforeTheNextResourceOnceTheContextIsDone(t *testing.T) {
	for _, tc := range []struct {
		op, cancelling, notReached string
		settledOn                  contract.State
	}{
		{"subscribe", "aws.vpc", "aws.ec2-instance", contract.Present},
		{"validate", "aws.vpc", "aws.ec2-instance", contract.Present},
		{"unsubscribe", "b", "a", contract.Absent},
	} {
		t.Run(tc.op, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cloud := newFakeCloud()
			if tc.op != "subscribe" {
				cloud.createAll()
			}
			fakes := blockObservers(cloud)
			obs := asObservers(fakes)
			obs[tc.cancelling] = cancellingObserver{Observer: fakes[tc.cancelling], settledOn: tc.settledOn, cancel: cancel}

			status, resp := serveBlock(t, blockCall{ctx: ctx, r: &blockResource{cloud: cloud}, obs: obs, op: tc.op, fields: recordedAll()})

			wantOK(t, status, resp)
			if want := `resource "` + tc.notReached + `" not reached: the request ended before the verb got to it`; resp.StatusDetail != want {
				t.Errorf("statusDetail = %q, want %q", resp.StatusDetail, want)
			}
			if got := len(resp.Resources); got != 1 || resp.Resources[tc.cancelling].Observe == nil {
				t.Errorf("resources = %+v, want only %q, the one reached", resp.Resources, tc.cancelling)
			}
			if calls := fakes[tc.notReached].calls(); len(calls) != 0 {
				t.Errorf("%q observed %d time(s), want none after the context was done", tc.notReached, len(calls))
			}
			if tc.op == "unsubscribe" {
				wantAbsent(t, resp, false)
			} else if !resp.Pending {
				t.Errorf("pending = false, want true: %q was never reached", tc.notReached)
			}
		})
	}
}

var issuedExpiry = time.Date(2026, 10, 14, 13, 0, 0, 0, time.UTC)

type issuingBlock struct {
	*blockResource
	credential contract.Credential
	apps       []string
}

func (b *issuingBlock) IssueCredential(ctx context.Context, d contract.Desired, appID string) (contract.Credential, error) {
	b.apps = append(b.apps, appID)
	return b.credential, nil
}

func newIssuingBlock(values map[string]string, expiresAt time.Time) *issuingBlock {
	return &issuingBlock{blockResource: &blockResource{cloud: newFakeCloud()}, credential: contract.Credential{Values: values, ExpiresAt: expiresAt}}
}

// Each issue reaches the capability, since a credential replayed from the idempotency cache could be one that has expired.
func TestBlockLevel_IssueAnswersEachCallWithTheCredentialTheCapabilityIssuesTheNamedApp(t *testing.T) {
	block := newIssuingBlock(map[string]string{"accessToken": "tok-app-shop"}, issuedExpiry)
	x := newHarness(t, block, withManifest(capabilityManifest), func(c *handler.Config) { c.Observers = asObservers(blockObservers(block.cloud)) })

	for i := 1; i <= 2; i++ {
		status, resp := x.answer(map[string]any{"operation": "issue", "issueAppId": "app-shop", "idempotencyKey": "issue-1"})

		if c := resp.Credential; status != http.StatusOK || c == nil || !reflect.DeepEqual(c.Values, block.credential.Values) || !c.ExpiresAt.Equal(issuedExpiry) {
			t.Errorf("issue %d: status %d, credential %+v; want 200 and %+v", i, status, c, block.credential)
		}
	}
	if !slices.Equal(block.apps, []string{"app-shop", "app-shop"}) || block.ensureCalls+block.deleteCalls != 0 {
		t.Errorf("issued to %q with %d Ensure and %d Delete calls, want app-shop once per call and neither verb", block.apps, block.ensureCalls, block.deleteCalls)
	}
}

func TestBlockLevel_IssueIsRefusedUnlessACapabilityIssuesACredentialWithItsExpiry(t *testing.T) {
	token, app := map[string]string{"accessToken": "tok-app-shop"}, map[string]any{"issueAppId": "app-shop"}
	withSecret := map[string]any{"issueAppId": "app-shop", "secretInputs": map[string]any{"connectionKey": "conn-key-9f2"}}
	cases := []struct {
		name, manifest string
		resource       handler.Resource
		fields         map[string]any
		status         int
		want           string
	}{
		{"a block", blockLevelManifest, newIssuingBlock(token, issuedExpiry), app, http.StatusBadRequest, "refused:not-a-capability"},
		{"a capability with no issue handler", capabilityManifest, &blockResource{cloud: newFakeCloud()}, app, http.StatusBadRequest, "refused:issues-no-credential"},
		{"an issue naming no app", capabilityManifest, newIssuingBlock(token, issuedExpiry), nil, http.StatusBadRequest, `"issueAppId"`},
		{"a credential with no expiry", capabilityManifest, newIssuingBlock(token, time.Time{}), app, http.StatusInternalServerError, "expiry"},
		{"a credential with no value", capabilityManifest, newIssuingBlock(nil, issuedExpiry), app, http.StatusInternalServerError, "no value"},
		{"a credential repeating a secret the request carried", capabilityManifest,
			newIssuingBlock(map[string]string{"accessToken": "conn-key-9f2"}, issuedExpiry), withSecret, http.StatusInternalServerError, "accessToken"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, resp := serveBlock(t, blockCall{r: tc.resource, obs: asObservers(blockObservers(newFakeCloud())), op: "issue", fields: tc.fields,
				config: []func(*handler.Config){withManifest(tc.manifest)}})

			if status != tc.status || resp.Credential != nil {
				t.Fatalf("status = %d, credential = %+v; want %d and none", status, resp.Credential, tc.status)
			}
			if got := resp.Error + " " + resp.StatusDetail; !strings.Contains(got, tc.want) || strings.Contains(got, "conn-key-9f2") {
				t.Errorf("error and statusDetail = %q, want them to name %s and carry no secret", got, tc.want)
			}
		})
	}
}

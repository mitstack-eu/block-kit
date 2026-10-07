package kittest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/observe"
	"github.com/mitstack-eu/operator-kit/webhook"
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
	// Desired is the subscription the check subscribes and unsubscribes; its
	// SubscriptionID, BlockID and OrgID are required.
	Desired contract.Desired
	// Upgrade is the Desired the check upgrades the subscription to. It is
	// required when a declared type is stateful.
	Upgrade *contract.Desired
	// Requirements holds, by requirement reference and then by type, the
	// outputs a resource of that type offers the requirement, bound on the
	// Desired of the run that meets the requirement with that type.
	Requirements map[string]map[string]map[string]string
}

// The guards CheckBlock proves, as AssertBlock logs those that ran.
const (
	guardTagging     = "tagging"
	guardProvider    = "observe-asks-the-provider"
	guardReplacement = "replacement"
	guardAbsence     = "absence-after-unsubscribe"
	guardOutputs     = "required-outputs"
)

// CheckBlock serves the block through handler.New, as the engine reaches
// it, and returns one problem per violation, empty when the block conforms.
// It subscribes Desired, and every declared resource must observe present
// with an identity, and every provided one with the outputs its type marks
// required. Then:
//
//   - everything the Provider lists under the subscription's id must match
//     a declared resource by type and count;
//   - with Upgrade set, a validate of Upgrade must name in replaces every
//     resource the upgrade then replaces, a stateful type's especially, and
//     Upgrade nil while a declared type is stateful is a problem;
//   - each resource the Provider loses must observe not present on a
//     validate, after which the check upgrades to recreate them;
//   - after unsubscribe every declared resource must observe absent.
//
// A requirement listing more than one type runs the whole check once per
// listed type, its RequirementTypes and Bindings set for that type, and
// each problem of such a run names the requirement and the type. Each
// problem about a resource begins `resource "<name>"`.
func CheckBlock(in Block) []string {
	problems, _ := checkBlock(in)
	return problems
}

// AssertBlock runs CheckBlock, logs which guards ran, and fails tb with one
// error per problem, in the order the check found them.
func AssertBlock(tb testing.TB, in Block) {
	tb.Helper()
	problems, ran := checkBlock(in)
	tb.Logf("kittest.AssertBlock guards ran: %s", strings.Join(ran, ", "))
	for _, p := range problems {
		tb.Error(p)
	}
}

func checkBlock(in Block) ([]string, []string) {
	spec, problems := blockSpec(in)
	if len(problems) > 0 {
		return problems, nil
	}
	server, err := newBlockServer(in)
	if err != nil {
		return []string{err.Error()}, nil
	}
	ran := map[string]bool{}
	for _, run := range blockRuns(spec, in) {
		c := &blockCheck{in: in, spec: spec, server: server, label: run.label, desired: run.desired, ran: ran}
		c.run()
		problems = append(problems, c.problems...)
	}
	var names []string
	for _, g := range []string{guardTagging, guardProvider, guardReplacement, guardAbsence, guardOutputs} {
		if ran[g] {
			names = append(names, g)
		}
	}
	return problems, names
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
	if err := contract.CheckManifestTypes(m.Block.Spec, in.Types); err != nil {
		return contract.ManifestSpec{}, []string{err.Error()}
	}
	var problems []string
	if in.Handler == nil || in.Provider == nil {
		problems = append(problems, "Handler and Provider are both required, so the guards cannot run")
	}
	if in.Desired.SubscriptionID == "" || in.Desired.BlockID == "" || in.Desired.OrgID == "" {
		problems = append(problems, "Desired needs a SubscriptionID, a BlockID and an OrgID, which every request the engine sends carries")
	}
	return m.Block.Spec, problems
}

// blockServer is the block served through handler.New, and the key its
// requests are signed with.
type blockServer struct {
	h      http.Handler
	signer *Signer
}

func newBlockServer(in Block) (*blockServer, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating the check's signing key: %w", err)
	}
	signer := &Signer{Kid: "kittest-block", Public: pub, Private: priv}
	h, err := handler.New(handler.Config{
		Resource:     in.Handler,
		Manifest:     in.Manifest,
		Observers:    in.Observers,
		JWKSURL:      "https://kittest.invalid/jwks",
		PublicKey:    func(string) (ed25519.PublicKey, error) { return pub, nil },
		VerbBudget:   3,
		PollInterval: time.Millisecond,
		Sleep:        func(context.Context, time.Duration) {},
	})
	if err != nil {
		return nil, fmt.Errorf("the block cannot be served: %v", err)
	}
	return &blockServer{h: h, signer: signer}, nil
}

// send posts one signed request for d and returns the answer.
func (s *blockServer) send(operation string, d contract.Desired) (int, contract.Response, error) {
	now := time.Now()
	obo, err := webhook.MintOBO(s.signer.Private, s.signer.Kid,
		webhook.OBOClaims{OrgID: d.OrgID, AppID: d.AppID, SubscriptionID: d.SubscriptionID}, 5*time.Minute, now)
	if err != nil {
		return 0, contract.Response{}, err
	}
	body, err := json.Marshal(contract.Request{
		Operation: operation, SubscriptionID: d.SubscriptionID, IdempotencyKey: freshToken(),
		BlockID: d.BlockID, BlockVersion: d.BlockVersion, OrgID: d.OrgID, AppID: d.AppID,
		Params: d.Params, Instances: d.Instances, Secrets: d.Secrets, OBO: obo,
		SecretInputs: d.SecretInputs, AppSecrets: d.AppSecrets, Bindings: d.Bindings, BindingsAll: d.BindingsAll,
		CloudIdentity: d.CloudIdentity, CreatedAt: now, Nonce: freshToken(), PriorOutputs: d.PriorOutputs,
		BuildRequest: d.BuildRequest, RecordedResources: d.RecordedResources, RequirementTypes: d.RequirementTypes,
	})
	if err != nil {
		return 0, contract.Response{}, err
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, s.signer.hookRequest("/hook", body))
	resp, err := contract.DecodeResponse(rec.Body.Bytes())
	return rec.Code, resp, err
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
				desired: requirementDesired(in.Desired, in.Requirements, types),
			})
		}
	}
	if len(runs) == 0 {
		runs = append(runs, blockRun{desired: requirementDesired(in.Desired, in.Requirements, base)})
	}
	return runs
}

// requirementDesired is d with each requirement met by the type types names
// for it, and bound to the outputs requirements gives that type.
func requirementDesired(d contract.Desired, requirements map[string]map[string]map[string]string, types map[string]string) contract.Desired {
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
		if outputs, ok := requirements[ref][typ]; ok {
			d.Bindings[ref] = outputs
		}
	}
	return d
}

// blockCheck is one run of CheckBlock, collecting problems. desired carries
// what the engine recorded of each resource, as the subscription's answers
// reported it.
type blockCheck struct {
	in       Block
	spec     contract.ManifestSpec
	server   *blockServer
	label    string
	desired  contract.Desired
	ran      map[string]bool
	problems []string
}

func (c *blockCheck) problem(format string, args ...any) {
	c.problems = append(c.problems, fmt.Sprintf(format, args...)+c.label)
}

func (c *blockCheck) resourceProblem(name, format string, args ...any) {
	c.problems = append(c.problems, fmt.Sprintf("resource %q%s: ", name, c.label)+fmt.Sprintf(format, args...))
}

func (c *blockCheck) run() {
	resp, ok := c.converge("subscribe")
	if !ok {
		return
	}
	for _, entry := range c.spec.Resources {
		if len(c.desired.RecordedResources[entry.Name].Identity) == 0 {
			c.resourceProblem(entry.Name, "observed present after subscribe with no identity, so the check cannot lose it from the provider; report each resource's identity through handler.ResourceReporter")
		}
	}
	c.requiredOutputs(resp, "subscribe")
	c.taggedMatchDeclared()
	if !c.upgrade() || !c.observesTheProvider() {
		return
	}
	c.unsubscribe()
}

// converge sends a subscribe or upgrade, which must answer every declared
// resource present, and records each resource's type and identity.
func (c *blockCheck) converge(operation string) (contract.Response, bool) {
	_, resp, ok := c.sendOK(operation, c.desired)
	if !ok {
		return resp, false
	}
	recorded := make(map[string]contract.RecordedResource, len(c.spec.Resources))
	named := false
	for _, entry := range c.spec.Resources {
		o := resp.Resources[entry.Name].Observe
		switch {
		case o == nil:
		case o.State != contract.Present:
			c.resourceProblem(entry.Name, "observed %s after %s, want present", o.State, operation)
			named = true
		default:
			recorded[entry.Name] = contract.RecordedResource{Type: o.Type, Identity: o.Identity}
		}
	}
	if len(recorded) < len(c.spec.Resources) || resp.Pending {
		if !named {
			c.problem("%s did not settle every declared resource present: %s", operation, resp.StatusDetail)
		}
		return resp, false
	}
	c.desired.RecordedResources = recorded
	return resp, true
}

// sendOK sends one request and records a problem unless it answers 200,
// naming each resource whose call failed.
func (c *blockCheck) sendOK(operation string, d contract.Desired) (int, contract.Response, bool) {
	status, resp, err := c.server.send(operation, d)
	if err != nil {
		c.problem("%s could not be sent or its answer read: %v", operation, err)
		return status, resp, false
	}
	if status == http.StatusOK {
		return status, resp, true
	}
	named := false
	for _, name := range sortedKeys(resp.Resources) {
		for _, call := range []*contract.CallOutcome{resp.Resources[name].Ensure, resp.Resources[name].Observe, resp.Resources[name].Delete} {
			if call != nil && call.Error != "" {
				c.resourceProblem(name, "%s failed: %s", operation, call.Error)
				named = true
			}
		}
	}
	if !named {
		c.problem("%s answered %d: %s", operation, status, resp.Error)
	}
	return status, resp, false
}

// requiredOutputs refuses a provided resource observed present without an
// output its snapshot type marks required. A secret output is read from
// the answer's secretOutputs, which carry no resource's name.
func (c *blockCheck) requiredOutputs(resp contract.Response, operation string) {
	c.ran[guardOutputs] = true
	for _, name := range c.spec.Provides {
		o := resp.Resources[name].Observe
		if o == nil || o.State != contract.Present {
			continue
		}
		t, ok := c.in.Types.Lookup(o.Type)
		if !ok {
			continue
		}
		for _, out := range t.Outputs {
			if !out.Required {
				continue
			}
			_, plain := o.Outputs[out.Name]
			_, secret := resp.SecretOutputs[out.Name]
			if !plain && !(out.Secret && secret) {
				c.resourceProblem(name, "observed present after %s without output %q, which its type %s marks required", operation, out.Name, o.Type)
			}
		}
	}
}

// taggedMatchDeclared lists what the provider holds tagged with the
// subscription's id, which must match the declared resources by type and
// count.
func (c *blockCheck) taggedMatchDeclared() {
	tagged, err := c.in.Provider.ListTagged(context.Background(), c.desired.SubscriptionID)
	if err != nil {
		c.problem("listing what carries the subscription's id failed: %v", err)
		return
	}
	c.ran[guardTagging] = true
	room := map[string]int{}
	for _, r := range c.desired.RecordedResources {
		room[r.Type]++
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

// upgrade validates Upgrade, upgrades, and refuses a resource whose
// identity changed that the validate's replaces did not name. With no
// Upgrade, a stateful declared type is a problem, since its replacement
// cannot be proved.
func (c *blockCheck) upgrade() bool {
	if c.in.Upgrade == nil {
		for _, entry := range c.spec.Resources {
			typ := c.desired.RecordedResources[entry.Name].Type
			if t, ok := c.in.Types.Lookup(typ); ok && t.Stateful {
				c.resourceProblem(entry.Name, "its type %s is stateful, but Upgrade is nil, so the check cannot prove an upgrade names its replacement", typ)
			}
		}
		return true
	}
	before := c.desired.RecordedResources
	upgrade := *c.in.Upgrade
	upgrade.RequirementTypes, upgrade.Bindings, upgrade.RecordedResources = c.desired.RequirementTypes, c.desired.Bindings, before
	_, validated, ok := c.sendOK("validate", upgrade)
	if !ok {
		return false
	}
	c.desired = upgrade
	resp, ok := c.converge("upgrade")
	if !ok {
		return false
	}
	c.ran[guardReplacement] = true
	for _, entry := range c.spec.Resources {
		was, is := before[entry.Name], c.desired.RecordedResources[entry.Name]
		if len(was.Identity) == 0 || maps.Equal(was.Identity, is.Identity) || slices.Contains(validated.Replaces, entry.Name) {
			continue
		}
		if t, ok := c.in.Types.Lookup(is.Type); ok && t.Stateful {
			c.resourceProblem(entry.Name, "the upgrade replaced it, but the validate before it did not name it in replaces; its type %s is stateful, so the replacement loses its data unannounced", is.Type)
		} else {
			c.resourceProblem(entry.Name, "the upgrade replaced it, but the validate before it did not name it in replaces")
		}
	}
	c.requiredOutputs(resp, "upgrade")
	return true
}

// observesTheProvider loses each resource with an identity from the
// provider, which a validate must then observe not present, and upgrades
// again so the teardown has the block whole.
func (c *blockCheck) observesTheProvider() bool {
	var lost []string
	for _, entry := range c.spec.Resources {
		r := c.desired.RecordedResources[entry.Name]
		if len(r.Identity) == 0 {
			continue
		}
		if err := c.in.Provider.Lose(context.Background(), contract.CreatedResource{Name: entry.Name, Type: r.Type, Identity: r.Identity}); err != nil {
			c.resourceProblem(entry.Name, "the provider could not lose it: %v", err)
			return false
		}
		lost = append(lost, entry.Name)
	}
	if len(lost) == 0 {
		return true
	}
	_, resp, ok := c.sendOK("validate", c.desired)
	if !ok {
		return false
	}
	c.ran[guardProvider] = true
	for _, name := range lost {
		if o := resp.Resources[name].Observe; o != nil && o.State == contract.Present {
			c.resourceProblem(name, "still observed present after the provider lost it; observe must ask the provider, not remember what the ensure created")
		}
	}
	_, ok = c.converge("upgrade")
	return ok
}

// unsubscribe sends an unsubscribe, after which every declared resource
// must observe absent.
func (c *blockCheck) unsubscribe() {
	_, resp, ok := c.sendOK("unsubscribe", c.desired)
	if !ok {
		return
	}
	c.ran[guardAbsence] = true
	if resp.Absent != nil && *resp.Absent {
		return
	}
	named := false
	for _, entry := range c.spec.Resources {
		if o := resp.Resources[entry.Name].Observe; o != nil && o.State != contract.Absent {
			c.resourceProblem(entry.Name, "observed %s after unsubscribe, want absent", o.State)
			named = true
		}
	}
	if !named {
		c.problem("unsubscribe did not prove every declared resource absent: %s", resp.StatusDetail)
	}
}

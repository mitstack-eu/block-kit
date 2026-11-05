package kittest

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/github/landing"
	"github.com/mitstack-eu/block-kit/capabilities/github/landing/landingtest"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/observe"
	"github.com/mitstack-eu/block-kit/webhook"
)

// Provider is the provider a block creates its resources in, which lists everything carrying a subscription's id and loses one resource out from under the block.
type Provider interface {
	ListTagged(ctx context.Context, subscriptionID string) ([]observe.Tagged, error)
	Lose(ctx context.Context, r contract.CreatedResource) error
}

type Loser interface {
	Lose(ctx context.Context, r contract.CreatedResource) error
}

// ListerProvider serves a kit lister to the tagging guard, which hands it the Desired it holds, and loses each resource through Loser.
type ListerProvider struct {
	Lister observe.TaggedLister
	Loser  Loser
}

// ListTagged refuses, since a lister such as observe.GitHubTagged reads the requirement bindings only the guard's Desired holds.
func (p ListerProvider) ListTagged(context.Context, string) ([]observe.Tagged, error) {
	return nil, errors.New("a ListerProvider lists only through the tagging guard, which hands its lister the Desired holding the requirement bindings")
}

func (p ListerProvider) Lose(ctx context.Context, r contract.CreatedResource) error {
	if p.Loser == nil {
		return errors.New("the ListerProvider has no Loser to lose a resource through")
	}
	return p.Loser.Lose(ctx, r)
}

func (p ListerProvider) listTaggedWith(ctx context.Context, d contract.Desired) ([]observe.Tagged, error) {
	if p.Lister == nil {
		return nil, errors.New("the ListerProvider has no Lister")
	}
	return p.Lister.ListTagged(ctx, d, d.SubscriptionID)
}

type desiredLister interface {
	listTaggedWith(ctx context.Context, d contract.Desired) ([]observe.Tagged, error)
}

// Block is what a block served through one block-level handler supplies to have its guards checked.
type Block struct {
	// Config is the handler.Config the block serves, of which the check replaces only the signing key and the JWKS address.
	Config handler.Config
	// Provider is a RepositoryProvider when a declared type lands files, or a RepositoriesProvider when they land in more than one repository, so the check can read main after unsubscribe.
	Provider Provider
	// Desired needs its SubscriptionID, BlockID and OrgID.
	Desired contract.Desired
	// Upgrade is required when a declared type is stateful.
	Upgrade *contract.Desired
	// Requirements is keyed by requirement reference then type, giving the outputs bound on the run that meets the requirement with that type.
	Requirements map[string]map[string]map[string]string
}

const (
	guardTagging     = "tagging"
	guardProvider    = "observe-asks-the-provider"
	guardReplacement = "replacement"
	guardAbsence     = "absence-after-unsubscribe"
	guardOutputs     = "required-outputs"
	guardKitObserver = "kit-observer"
	guardProbeReads  = "probe-reads"
)

// CheckBlock serves the block through handler.New from its Config, as the engine reaches it, and returns one problem per violation.
func CheckBlock(in Block) []string {
	problems, _ := checkBlock(in)
	return problems
}

// AssertBlock runs CheckBlock, logs which guards ran, and fails tb with one error per problem.
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
	if problems := unservedKitTypes(in, spec); len(problems) > 0 {
		return problems, []string{guardKitObserver}
	}
	server, err := newBlockServer(in.Config)
	if err != nil {
		return []string{err.Error()}, nil
	}
	ran := map[string]bool{}
	problems = append(problems, ownObservers(in, spec, ran)...)
	for _, run := range blockRuns(spec, in) {
		c := &blockCheck{in: in, spec: spec, server: server, label: run.label, desired: run.desired, ran: ran}
		c.run()
		problems = append(problems, c.problems...)
	}
	var names []string
	for _, g := range []string{guardTagging, guardProvider, guardReplacement, guardAbsence, guardOutputs, guardKitObserver, guardProbeReads} {
		if ran[g] {
			names = append(names, g)
		}
	}
	return problems, names
}

func blockSpec(in Block) (contract.ManifestSpec, []string) {
	m, err := contract.ParseManifest(in.Config.Manifest)
	if err != nil {
		return contract.ManifestSpec{}, []string{fmt.Sprintf("the manifest is not one the kit's parser accepts, so its resources are unknown: %v", err)}
	}
	if m.Block == nil {
		return contract.ManifestSpec{}, []string{fmt.Sprintf("the manifest is %s, but only a %s manifest declares resources", m.APIVersion, contract.ManifestAPIVersion)}
	}
	if err := contract.CheckManifestTypes(m.Block.Spec, in.Config.Types); err != nil {
		return contract.ManifestSpec{}, []string{err.Error()}
	}
	var problems []string
	if in.Provider == nil {
		problems = append(problems, "Provider is nil, so the guards cannot list or lose what the block creates")
	}
	if in.Desired.SubscriptionID == "" || in.Desired.BlockID == "" || in.Desired.OrgID == "" {
		problems = append(problems, "Desired needs a SubscriptionID, a BlockID and an OrgID, which every request the engine sends carries")
	}
	return m.Block.Spec, problems
}

type blockServer struct {
	h        http.Handler
	signer   *Signer
	audience string
}

// sendsNoLogs gives a check built from the manifest alone, which never sends a logs request, the logs function handler.New requires of a manifest listing logs.
func sendsNoLogs(cfg handler.Config) handler.Config {
	m, err := contract.ParseManifest(cfg.Manifest)
	if err == nil && cfg.SubscriptionLogs == nil && slices.Contains(m.Block.Spec.Lifecycle.Operations, contract.OperationLogs) {
		cfg.SubscriptionLogs = func(context.Context, contract.Desired) ([]string, error) {
			return nil, errors.New("kittest: this check sends no logs request")
		}
	}
	return cfg
}

func newBlockServer(cfg handler.Config) (*blockServer, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating the check's signing key: %w", err)
	}
	cfg.JWKSURL = "https://kittest.invalid/jwks"
	cfg.PublicKey = func(string) (ed25519.PublicKey, error) { return pub, nil }
	h, err := handler.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("the block cannot be served: %v", err)
	}
	m, _ := contract.ParseManifest(cfg.Manifest)
	return &blockServer{h: h, signer: &Signer{Kid: "kittest-block", Public: pub, Private: priv}, audience: m.ID()}, nil
}

func (s *blockServer) send(operation string, d contract.Desired) (int, contract.Response, error) {
	now := time.Now()
	obo, err := webhook.MintOBO(s.signer.Private, s.signer.Kid,
		webhook.OBOClaims{Audience: s.audience, OrgID: d.OrgID, AppID: d.AppID, SubscriptionID: d.SubscriptionID}, 5*time.Minute, now)
	if err != nil {
		return 0, contract.Response{}, err
	}
	body, err := json.Marshal(contract.Request{
		Operation: operation, SubscriptionID: d.SubscriptionID, IdempotencyKey: freshToken(),
		BlockID: d.BlockID, BlockVersion: d.BlockVersion, OrgID: d.OrgID, AppID: d.AppID,
		Params: d.Params, Instances: d.Instances, Secrets: d.Secrets, OBO: obo,
		SecretInputs: d.SecretInputs, AppSecrets: d.AppSecrets, Bindings: d.Bindings, BindingsAll: d.BindingsAll,
		CloudIdentity: d.CloudIdentity, CreatedAt: now, Nonce: freshToken(), PriorOutputs: d.PriorOutputs,
		RecordedResources: d.RecordedResources, RequirementTypes: d.RequirementTypes,
		Resource: d.Resource, ResourceType: d.ResourceType, ResourceIdentity: d.ResourceIdentity,
	})
	if err != nil {
		return 0, contract.Response{}, err
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, s.signer.hookRequest("/hook", body))
	resp, err := contract.DecodeResponse(rec.Body.Bytes())
	return rec.Code, resp, err
}

type blockRun struct {
	label   string
	desired contract.Desired
}

// blockRuns runs once per listed type of each requirement listing more than one, meeting every other requirement with its first type.
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

type blockCheck struct {
	in       Block
	spec     contract.ManifestSpec
	server   *blockServer
	label    string
	desired  contract.Desired
	ran      map[string]bool
	problems []string
	kept     map[string][]contract.Kept
	// failedCauses holds each resource and unhealthy cause already failed, so a cause every answer repeats fails once.
	failedCauses map[[2]string]bool
	// failedRemovals holds each resource whose removing answer already failed, so an answer every observe repeats fails once.
	failedRemovals map[string]bool
}

// unitNames lists the names a guard reads, in manifest order, putting in each entry's place the names contract.ManifestResource.Names gives it among held.
func unitNames[V any](spec contract.ManifestSpec, held map[string]V) []string {
	keys := sortedKeys(held)
	var names []string
	for _, entry := range spec.Resources {
		names = append(names, entry.Names(keys)...)
	}
	return names
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
	for _, name := range unitNames(c.spec, c.desired.RecordedResources) {
		if len(c.desired.RecordedResources[name].Identity) == 0 {
			c.resourceProblem(name, "observed present after subscribe with no identity, so the check cannot lose it from the provider; report each resource's identity through handler.ResourceReporter or the Observation its observer returns")
		}
	}
	c.requiredOutputs(resp, "subscribe")
	c.commitContributions(resp)
	c.taggedMatchDeclared()
	c.probeReads()
	if !c.upgrade() || !c.observesTheProvider() {
		return
	}
	landed := c.changeALandedFile()
	c.unsubscribe()
	c.readMain(landed)
}

// RepositoryProvider is the Provider of a block declaring a file-landing type, which hands the check the fake repository the block's Config lands on.
type RepositoryProvider interface {
	Provider
	Repository() *landingtest.Fake
}

// RepositoriesProvider is the Provider of a block landing files in more than one repository, which hands the check one fake per repository, keyed by owner/name.
type RepositoriesProvider interface {
	Provider
	Repositories() map[string]*landingtest.Fake
}

// repositories is every fake repository the Provider hands the check, keyed by owner/name and leaving out a nil fake, and false when it hands none.
func (c *blockCheck) repositories() (map[string]*landingtest.Fake, bool) {
	switch p := c.in.Provider.(type) {
	case RepositoriesProvider:
		repos := maps.Clone(p.Repositories())
		maps.DeleteFunc(repos, func(_ string, f *landingtest.Fake) bool { return f == nil })
		return repos, true
	case RepositoryProvider:
		repo := p.Repository()
		return map[string]*landingtest.Fake{repo.Repo: repo}, true
	}
	return nil, false
}

// commitContributions writes each file a scaffold contribution offers onto the fake main, as app-scaffold commits it, and fails an offered file the contribution's landing record does not name at its blob, since unsubscribe could not remove it.
func (c *blockCheck) commitContributions(resp contract.Response) {
	repos, ok := c.repositories()
	if !ok {
		return
	}
	for _, name := range unitNames(c.spec, resp.Resources) {
		o := resp.Resources[name].Observe
		if o == nil || o.Type != "github.scaffold-contribution" {
			continue
		}
		var offer struct {
			Files map[string]string `json:"files"`
		}
		if err := json.Unmarshal([]byte(o.Outputs["scaffoldContribution"]), &offer); err != nil || len(offer.Files) == 0 {
			continue
		}
		rec, err := landing.RecordFromIdentity(c.desired.RecordedResources[name].Identity)
		if err != nil || repos[rec.Repository] == nil {
			continue
		}
		blobs := map[string]string{}
		for _, f := range rec.Files {
			blobs[f.Path] = f.Blob
		}
		repo := repos[rec.Repository]
		main, commit := repo.Main(), map[string]string{}
		for _, path := range sortedKeys(offer.Files) {
			if blob := landing.BlobHash([]byte(offer.Files[path])); blobs[path] != blob {
				c.resourceProblem(name, "it offers %s with blob %s, but its landing record does not name it at that blob, so unsubscribe cannot remove it", path, blob)
			}
			if _, held := main[path]; !held {
				commit[path] = offer.Files[path]
			}
		}
		if len(commit) > 0 {
			repo.Seed(commit)
		}
	}
}

type landedFiles struct {
	repos   map[string]*landingtest.Fake
	records map[string]landing.Record
	// changed holds, per repository, the file the check changed as the app would.
	changed map[string]changedFile
}

type changedFile struct {
	path, content string
}

// changeALandedFile reads each file-landing resource's record and, in each repository, changes the first file a record created there, as an app edits a landed file; it returns nil for a block that lands no files.
func (c *blockCheck) changeALandedFile() *landedFiles {
	var names []string
	for _, name := range unitNames(c.spec, c.desired.RecordedResources) {
		if landing.LandsFiles(c.desired.RecordedResources[name].Type) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	repos, ok := c.repositories()
	if !ok {
		c.resourceProblem(names[0], "its type lands files, but Provider is neither a kittest.RepositoryProvider nor a kittest.RepositoriesProvider, so the check cannot read main after unsubscribe")
		return nil
	}
	l := &landedFiles{repos: repos, records: map[string]landing.Record{}, changed: map[string]changedFile{}}
	for _, name := range names {
		rec, err := landing.RecordFromIdentity(c.desired.RecordedResources[name].Identity)
		if err != nil {
			c.resourceProblem(name, "its type lands files, but its identity holds no landing record: %v", err)
			continue
		}
		repo := repos[rec.Repository]
		if repo == nil {
			c.resourceProblem(name, "it landed files in %s, but the Provider hands the check no fake repository for it, only %s", rec.Repository, strings.Join(sortedKeys(repos), ", "))
			continue
		}
		l.records[name] = rec
		for _, f := range rec.Files {
			if _, done := l.changed[rec.Repository]; !done && f.Created {
				content := repo.Main()[f.Path] + "\nchanged by the app\n"
				repo.Edit(f.Path, content)
				l.changed[rec.Repository] = changedFile{path: f.Path, content: content}
			}
		}
	}
	return l
}

// readMain fails a landed file main still carries unchanged, a changed file gone or rewritten, and a branch left under the subscription's prefix, in each repository but one the fake shows deleted, which holds nothing.
func (c *blockCheck) readMain(l *landedFiles) {
	if l == nil {
		return
	}
	for _, name := range sortedKeys(l.records) {
		rec := l.records[name]
		repo := l.repos[rec.Repository]
		if repo.Deleted() {
			continue
		}
		main, changed := repo.Main(), l.changed[rec.Repository]
		for _, f := range rec.Files {
			content, held := main[f.Path]
			switch {
			case f.Path == changed.path && content != changed.content:
				c.resourceProblem(name, "unsubscribe deleted or rewrote %s, which the app changed after the landing, want it kept", f.Path)
			case f.Path == changed.path && !slices.Contains(c.kept[name], contract.Kept{Repository: rec.Repository, Path: f.Path, Reason: "changed"}):
				c.resourceProblem(name, "unsubscribe kept %s, which the app changed, but no delete outcome answers it kept with reason changed", f.Path)
			case f.Path != changed.path && f.Created && held && landing.BlobHash([]byte(content)) == f.Blob:
				c.resourceProblem(name, "main of %s still carries %s unchanged after unsubscribe, want it removed", rec.Repository, f.Path)
			}
		}
	}
	prefix := landing.BranchPrefix(c.desired.SubscriptionID)
	for _, name := range sortedKeys(l.repos) {
		if l.repos[name].Deleted() {
			continue
		}
		for _, branch := range l.repos[name].Branches() {
			if strings.HasPrefix(branch, prefix) {
				c.problem("branch %s under the subscription's prefix remains on %s after unsubscribe, want it deleted", branch, name)
			}
		}
	}
}

// settleBudget bounds how long converge and unsubscribe settle a pending answer, as the engine's settle budget bounds its own, and settlePause spaces the calls so work the block left in flight can finish.
var settleBudget = 10 * time.Second

const settlePause = 20 * time.Millisecond

// converge settles operation as the engine does: while the answer leaves a declared resource not present, it validates with what every answer so far reported of each resource, and sends operation again when a validate asks for a re-drive.
func (c *blockCheck) converge(operation string) (contract.Response, bool) {
	recorded := maps.Clone(c.desired.RecordedResources)
	dropped := map[string]contract.RecordedResource{}
	call, deadline := operation, time.Now().Add(settleBudget)
	for {
		d := c.desired
		d.RecordedResources = recorded
		_, resp, ok := c.sendOK(call, d)
		if !ok {
			return resp, false
		}
		recorded = c.recordReported(recorded, resp)
		if call == operation {
			c.dropUnreported(recorded, resp, dropped)
		}
		if c.settled(resp) {
			c.desired.RecordedResources = recorded
			c.proveDroppedAbsent(operation, dropped)
			return resp, true
		}
		if time.Now().After(deadline) {
			c.unsettled(operation, resp)
			return resp, false
		}
		call = "validate"
		if resp.Redrive {
			call = operation
		}
		time.Sleep(settlePause)
	}
}

// recordReported keeps, as the engine's record does, the type and identity each ensure and observe reported, and what was recorded where a call reported none.
func (c *blockCheck) recordReported(recorded map[string]contract.RecordedResource, resp contract.Response) map[string]contract.RecordedResource {
	if recorded == nil {
		recorded = make(map[string]contract.RecordedResource, len(c.spec.Resources))
	}
	for _, name := range unitNames(c.spec, resp.Resources) {
		for _, call := range []*contract.CallOutcome{resp.Resources[name].Ensure, resp.Resources[name].Observe} {
			if call == nil {
				continue
			}
			rec := recorded[name]
			rec.Type = cmp.Or(call.Type, rec.Type)
			if len(call.Identity) > 0 {
				rec.Identity = call.Identity
			}
			recorded[name] = rec
		}
	}
	return recorded
}

// dropUnreported moves into dropped each recorded member the answer carries no ensure outcome for, unless the answer carries none at all and is pending, as a block answers while it waits before ensuring.
func (c *blockCheck) dropUnreported(recorded map[string]contract.RecordedResource, resp contract.Response, dropped map[string]contract.RecordedResource) {
	if resp.Pending && !ensuredAny(resp) {
		return
	}
	held := sortedKeys(recorded)
	for _, entry := range c.spec.Resources {
		if !entry.Many {
			continue
		}
		for _, name := range entry.Names(held) {
			if resp.Resources[name].Ensure == nil {
				dropped[name] = recorded[name]
				delete(recorded, name)
			}
		}
	}
}

func ensuredAny(resp contract.Response) bool {
	for _, o := range resp.Resources {
		if o.Ensure != nil {
			return true
		}
	}
	return false
}

// proveDroppedAbsent validates each dropped member by name, as the engine proves a member absent before the operation that dropped it settles, then checks the provider again for a stray object.
func (c *blockCheck) proveDroppedAbsent(operation string, dropped map[string]contract.RecordedResource) {
	if len(dropped) == 0 {
		return
	}
	for _, name := range sortedKeys(dropped) {
		d := c.desired
		d.Resource, d.ResourceType, d.ResourceIdentity = name, dropped[name].Type, dropped[name].Identity
		_, resp, ok := c.sendOK("validate", d)
		if !ok {
			continue
		}
		if o := resp.Resources[name].Observe; o == nil || o.State != contract.Absent {
			c.resourceProblem(name, "the %s stopped reporting it, but a validate naming it does not observe it absent; Ensure removes a member it stops reporting", operation)
		}
	}
	c.taggedMatchDeclared()
}

func (c *blockCheck) settled(resp contract.Response) bool {
	for _, name := range unitNames(c.spec, resp.Resources) {
		if o := resp.Resources[name].Observe; o == nil || o.State != contract.Present {
			return false
		}
	}
	return !resp.Pending
}

func (c *blockCheck) unsettled(operation string, resp contract.Response) {
	named := false
	for _, name := range unitNames(c.spec, resp.Resources) {
		if o := resp.Resources[name].Observe; o != nil && o.State != contract.Present {
			c.resourceProblem(name, "observed %s after %s and validates for %s, want present", o.State, operation, settleBudget)
			named = true
		}
	}
	if !named {
		c.problem("%s did not settle every declared resource present within %s: %s", operation, settleBudget, resp.StatusDetail)
	}
}

func (c *blockCheck) sendOK(operation string, d contract.Desired) (int, contract.Response, bool) {
	status, resp, err := c.server.send(operation, d)
	if err != nil {
		c.problem("%s could not be sent or its answer read: %v", operation, err)
		return status, resp, false
	}
	if status == http.StatusOK {
		c.unhealthyCauses(resp)
		c.removingWaits(resp)
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

// unhealthyCauses fails an observe answering a resource unhealthy beside a state other than present, which the engine never reads, or with a cause its type does not list under failures, since the platform shows a failure by its type's entry.
func (c *blockCheck) unhealthyCauses(resp contract.Response) {
	for _, name := range sortedKeys(resp.Resources) {
		o := resp.Resources[name].Observe
		if o == nil || o.Unhealthy == nil {
			continue
		}
		t, _ := c.in.Config.Types.Lookup(o.Type)
		cause := o.Unhealthy.Cause
		listed := slices.ContainsFunc(t.Failures, func(f contract.SnapshotFailure) bool { return f.Cause == cause })
		key := [2]string{name, cause}
		if c.failedCauses[key] || (listed && o.State == contract.Present) {
			continue
		}
		if c.failedCauses == nil {
			c.failedCauses = map[[2]string]bool{}
		}
		c.failedCauses[key] = true
		if o.State != contract.Present {
			c.resourceProblem(name, "observed %s with the unhealthy cause %q, but only a present resource is unhealthy", o.State, cause)
		} else {
			c.resourceProblem(name, "observed unhealthy with the cause %q, which its type %s does not list under failures", cause, o.Type)
		}
	}
}

// removingWaits fails an observe answering a removal under way beside a state other than present, which the engine never reads, or naming nothing it waits on, since the platform shows what the removal waits on.
func (c *blockCheck) removingWaits(resp contract.Response) {
	for _, name := range sortedKeys(resp.Resources) {
		o := resp.Resources[name].Observe
		if o == nil || o.Removing == nil || c.failedRemovals[name] {
			continue
		}
		switch {
		case o.State != contract.Present:
			c.resourceProblem(name, "observed %s with removing, but only a present resource is being removed", o.State)
		case strings.TrimSpace(o.Removing.WaitsOn) == "":
			c.resourceProblem(name, "observed removing with a blank waitsOn, which must say what the removal waits on")
		default:
			continue
		}
		if c.failedRemovals == nil {
			c.failedRemovals = map[string]bool{}
		}
		c.failedRemovals[name] = true
	}
}

// requiredOutputs reads a secret output from the answer's secretOutputs, which carry no resource's name.
func (c *blockCheck) requiredOutputs(resp contract.Response, operation string) {
	c.ran[guardOutputs] = true
	for _, name := range c.spec.Provides {
		o := resp.Resources[name].Observe
		if o == nil || o.State != contract.Present {
			continue
		}
		t, ok := c.in.Config.Types.Lookup(o.Type)
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

func (c *blockCheck) taggedMatchDeclared() {
	var tagged []observe.Tagged
	var err error
	if l, ok := c.in.Provider.(desiredLister); ok {
		tagged, err = l.listTaggedWith(context.Background(), c.desired)
	} else {
		tagged, err = c.in.Provider.ListTagged(context.Background(), c.desired.SubscriptionID)
	}
	if err != nil {
		c.problem("listing what carries the subscription's id failed: %v", err)
		return
	}
	c.ran[guardTagging] = true
	room := c.taggedRoom()
	slices.SortFunc(tagged, func(a, b observe.Tagged) int { return strings.Compare(a.ID, b.ID) })
	for _, t := range tagged {
		switch {
		case t.Type == "":
			c.resourceProblem(t.ID, "it carries the subscription's id, but is of no type the provider's lister knows, so no declared resource matches it")
		case room[t.Type] == 0:
			c.resourceProblem(t.ID, "it carries the subscription's id as type %s, but neither the manifest's resources nor the parts their types list account for another of that type", t.Type)
		default:
			room[t.Type]--
		}
	}
	for _, name := range untagged(c.desired.RecordedResources, tagged) {
		c.resourceProblem(name, "untagged: the provider lists no %s carrying the subscription's id for it, so the platform can neither find it nor tear it down; tag it where Ensure creates it", c.desired.RecordedResources[name].Type)
	}
}

// untagged names, in name order, each recorded resource that no tagged object of its type is left for, giving a resource the object its identity names before any other of its type.
func untagged(recorded map[string]contract.RecordedResource, tagged []observe.Tagged) []string {
	taken := make([]bool, len(tagged))
	take := func(r contract.RecordedResource, fits func(observe.Tagged) bool) bool {
		for i, t := range tagged {
			if !taken[i] && t.Type == r.Type && fits(t) {
				taken[i] = true
				return true
			}
		}
		return false
	}
	var unmatched, missing []string
	for _, name := range sortedKeys(recorded) {
		identity := slices.Collect(maps.Values(recorded[name].Identity))
		if !take(recorded[name], func(t observe.Tagged) bool { return slices.Contains(identity, t.ID) }) {
			unmatched = append(unmatched, name)
		}
	}
	for _, name := range unmatched {
		if !take(recorded[name], func(observe.Tagged) bool { return true }) {
			missing = append(missing, name)
		}
	}
	return missing
}

// taggedRoom counts, per type, how many tagged objects the recorded resources account for: each resource itself, and the parts its type lists.
func (c *blockCheck) taggedRoom() map[string]int {
	room := map[string]int{}
	for _, r := range c.desired.RecordedResources {
		room[r.Type]++
		t, ok := c.in.Config.Types.Lookup(r.Type)
		if !ok {
			continue
		}
		for _, part := range t.Parts {
			room[part.Type] += part.Count
		}
	}
	return room
}

// upgrade flags a stateful declared type when Upgrade is nil, since its replacement cannot be proved.
func (c *blockCheck) upgrade() bool {
	if c.in.Upgrade == nil {
		for _, name := range unitNames(c.spec, c.desired.RecordedResources) {
			typ := c.desired.RecordedResources[name].Type
			if t, ok := c.in.Config.Types.Lookup(typ); ok && t.Stateful {
				c.resourceProblem(name, "its type %s is stateful, but Upgrade is nil, so the check cannot prove an upgrade names its replacement", typ)
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
	for _, name := range unitNames(c.spec, c.desired.RecordedResources) {
		was, is := before[name], c.desired.RecordedResources[name]
		if len(was.Identity) == 0 || maps.Equal(was.Identity, is.Identity) || slices.Contains(validated.Replaces, name) {
			continue
		}
		if t, ok := c.in.Config.Types.Lookup(is.Type); ok && t.Stateful {
			c.resourceProblem(name, "the upgrade replaced it, but the validate before it did not name it in replaces; its type %s is stateful, so the replacement loses its data unannounced", is.Type)
		} else {
			c.resourceProblem(name, "the upgrade replaced it, but the validate before it did not name it in replaces")
		}
	}
	c.requiredOutputs(resp, "upgrade")
	return true
}

// observesTheProvider upgrades again afterwards so the teardown has the block whole.
func (c *blockCheck) observesTheProvider() bool {
	var lost []string
	for _, name := range unitNames(c.spec, c.desired.RecordedResources) {
		r := c.desired.RecordedResources[name]
		if len(r.Identity) == 0 {
			continue
		}
		if err := c.in.Provider.Lose(context.Background(), contract.CreatedResource{Name: name, Type: r.Type, Identity: r.Identity}); err != nil {
			c.resourceProblem(name, "the provider could not lose it: %v", err)
			return false
		}
		lost = append(lost, name)
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

// unsubscribe settles the teardown as the engine re-drives one: it sends unsubscribe again, with each resource an answer observed absent recorded absent, until the answer is absent:true or the settle budget runs out.
func (c *blockCheck) unsubscribe() {
	d := c.desired
	d.RecordedResources = make(map[string]contract.RecordedResource, len(c.desired.RecordedResources))
	maps.Copy(d.RecordedResources, c.desired.RecordedResources)
	deadline := time.Now().Add(settleBudget)
	for {
		_, resp, ok := c.sendOK("unsubscribe", d)
		if !ok {
			return
		}
		c.ran[guardAbsence] = true
		c.keepKept(resp)
		if resp.Absent != nil && *resp.Absent {
			return
		}
		if time.Now().After(deadline) {
			c.notAbsent(resp)
			return
		}
		for _, name := range unitNames(c.spec, resp.Resources) {
			if o := resp.Resources[name].Observe; o != nil && o.State == contract.Absent {
				rec := d.RecordedResources[name]
				rec.State = contract.Absent
				d.RecordedResources[name] = rec
			}
		}
		time.Sleep(settlePause)
	}
}

func (c *blockCheck) keepKept(resp contract.Response) {
	for name, o := range resp.Resources {
		if o.Delete != nil && len(o.Delete.Kept) > 0 {
			if c.kept == nil {
				c.kept = map[string][]contract.Kept{}
			}
			c.kept[name] = o.Delete.Kept
		}
	}
}

func (c *blockCheck) notAbsent(resp contract.Response) {
	named := false
	for _, name := range unitNames(c.spec, resp.Resources) {
		if o := resp.Resources[name].Observe; o != nil && o.State != contract.Absent {
			c.resourceProblem(name, "observed %s after unsubscribe re-sent for %s, want absent", o.State, settleBudget)
			named = true
		}
	}
	if !named {
		c.problem("unsubscribe did not prove every declared resource absent within %s: %s", settleBudget, resp.StatusDetail)
	}
}

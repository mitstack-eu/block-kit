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
	"github.com/mitstack-eu/block-kit/verify/internal/kitbinding"
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
	guardEmptyID     = "empty-identity-unsubscribe"
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
	for _, g := range []string{guardTagging, guardProvider, guardReplacement, guardAbsence, guardOutputs, guardKitObserver, guardProbeReads, guardEmptyID} {
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

// sendsNoLogsOrRun gives a check built from the manifest alone, which never sends a logs or a run request, the functions handler.New requires of a manifest listing either.
func sendsNoLogsOrRun(cfg handler.Config) handler.Config {
	m, err := contract.ParseManifest(cfg.Manifest)
	if err != nil {
		return cfg
	}
	operations := m.Block.Spec.Lifecycle.Operations
	if cfg.SubscriptionLogs == nil && slices.Contains(operations, contract.OperationLogs) {
		cfg.SubscriptionLogs = func(context.Context, contract.Desired) ([]string, error) {
			return nil, errors.New("kittest: this check sends no logs request")
		}
	}
	if cfg.Run == nil && slices.Contains(operations, contract.OperationRun) {
		cfg.Run = func(context.Context, contract.Desired, contract.RunRequest) (contract.RunResult, error) {
			return contract.RunResult{}, errors.New("kittest: this check sends no run request")
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
		return nil, fmt.Errorf("%s%v", kitbinding.UnservableBlock, err)
	}
	m, _ := contract.ParseManifest(cfg.Manifest)
	return &blockServer{h: h, signer: &Signer{Kid: "kittest-block", Public: pub, Private: priv}, audience: m.ID()}, nil
}

func (s *blockServer) send(operation string, d contract.Desired) (int, contract.Response, error) {
	return s.sendUnit(operation, "", d)
}

// settle sends the validate that settles a verb, the one request that carries settling, as the engine's settle probe does.
func (s *blockServer) settle(d contract.Desired) (int, contract.Response, error) {
	return s.sendRequest("validate", "", true, d)
}

// sendUnit also names the spec.continuity entry a snapshot or a restore is for, and carries d.Replacing as the marker of a request sent during a replacement.
func (s *blockServer) sendUnit(operation, unit string, d contract.Desired) (int, contract.Response, error) {
	return s.sendRequest(operation, unit, false, d)
}

func (s *blockServer) sendRequest(operation, unit string, settling bool, d contract.Desired) (int, contract.Response, error) {
	req := requestFor(operation, d)
	req.Unit, req.Settling = unit, settling
	return s.post(req, time.Now())
}

// post serves req to the block as sent at now, under a fresh idempotency key and nonce and an OBO token naming its subscription.
func (s *blockServer) post(req contract.Request, now time.Time) (int, contract.Response, error) {
	obo, err := webhook.MintOBO(s.signer.Private, s.signer.Kid,
		webhook.OBOClaims{Audience: s.audience, OrgID: req.OrgID, AppID: req.AppID, SubscriptionID: req.SubscriptionID}, 5*time.Minute, now)
	if err != nil {
		return 0, contract.Response{}, err
	}
	req.IdempotencyKey, req.OBO, req.CreatedAt, req.Nonce = freshToken(), obo, now, freshToken()
	body, err := json.Marshal(req)
	if err != nil {
		return 0, contract.Response{}, err
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, s.signer.hookRequest("/hook", body))
	resp, err := contract.DecodeResponse(rec.Body.Bytes())
	return rec.Code, resp, err
}

// RequestBody is the engine's request for operation on d as a LeakRequest's Body, which the harness wraps in its envelope.
func RequestBody(t testing.TB, operation string, d contract.Desired) map[string]any {
	t.Helper()
	raw, err := json.Marshal(requestFor(operation, d))
	var body map[string]any
	if err == nil {
		err = json.Unmarshal(raw, &body)
	}
	if err != nil {
		t.Fatalf("RequestBody(%s): the request does not read back as a body: %v", operation, err)
	}
	for _, envelope := range []string{"idempotencyKey", "obo", "createdAt", "nonce"} {
		delete(body, envelope)
	}
	return body
}

// requestFor is the engine's request for operation on d, before its envelope of key, token, time and nonce.
func requestFor(operation string, d contract.Desired) contract.Request {
	return contract.Request{
		Operation: operation, SubscriptionID: d.SubscriptionID,
		BlockID: d.BlockID, BlockVersion: d.BlockVersion, OrgID: d.OrgID, AppID: d.AppID,
		Params: d.Params, Instances: d.Instances, Secrets: d.Secrets,
		SecretInputs: d.SecretInputs, AppSecrets: d.AppSecrets, Bindings: d.Bindings, BindingsAll: d.BindingsAll, Absences: d.Absences,
		CloudIdentity: d.CloudIdentity, PriorOutputs: d.PriorOutputs,
		RecordedResources: d.RecordedResources, RequirementTypes: d.RequirementTypes,
		Resource: d.Resource, ResourceType: d.ResourceType, ResourceIdentity: d.ResourceIdentity, ResourceUnsubscribing: d.ResourceUnsubscribing,
		Replacing: d.Replacing,
	}
}

type blockRun struct {
	label   string
	desired contract.Desired
}

// blockRuns runs once per listed type of each requirement listing more than one, and once with each optional requirement unbound, meeting every other requirement with its first type.
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
				desired: requirementDesired(in.Desired, in.Requirements, types, ""),
			})
		}
	}
	if len(runs) == 0 {
		runs = append(runs, blockRun{desired: requirementDesired(in.Desired, in.Requirements, base, "")})
	}
	for _, q := range spec.Requires {
		if !q.Optional {
			continue
		}
		types := maps.Clone(base)
		delete(types, q.Reference())
		runs = append(runs, blockRun{
			label:   fmt.Sprintf(" (requires.%s unbound)", q.Reference()),
			desired: requirementDesired(in.Desired, in.Requirements, types, q.Reference()),
		})
	}
	return runs
}

// requirementDesired meets each requirement in types with its type, and hands the one named unbound no binding and no type, as the platform does an optional requirement the app holds no provider for.
func requirementDesired(d contract.Desired, requirements map[string]map[string]map[string]string, types map[string]string, unbound string) contract.Desired {
	if len(types) == 0 && unbound == "" {
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
	delete(d.RequirementTypes, unbound)
	delete(d.Bindings, unbound)
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
	// failedWaits holds each resource whose waitingOnPerson answer already failed, for the same reason.
	failedWaits map[string]bool
	// holdsNothing holds each scaffold contribution with a landing record for which the check wrote no file onto main and main holds no file the record created, so GitHub holds nothing of it for a lister to list.
	holdsNothing map[string]bool
	// afterRestore makes a resource settle only once it reads healthy, as the engine observes a replaced resource.
	afterRestore bool
	// snapshotting, while the replacement drill settles a snapshot, names the unit every send carries and keeps the first send's createdAt, which each resend carries as settlesAt.
	snapshotting *unitSend
	reported     map[string]bool
}

type unitSend struct {
	unit  string
	first time.Time
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

// commitContributions writes each file a scaffold contribution offers onto the fake main, as app-scaffold commits it, fails an offered file the contribution's landing record does not name at its blob, since unsubscribe could not remove it, and notes each contribution of which main holds nothing.
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
		if err := json.Unmarshal([]byte(o.Outputs["scaffoldContribution"]), &offer); err != nil {
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
			continue
		}
		if !slices.ContainsFunc(rec.Files, func(f landing.File) bool {
			content, held := main[f.Path]
			return f.Created && held && landing.BlobHash([]byte(content)) == f.Blob
		}) {
			if c.holdsNothing == nil {
				c.holdsNothing = map[string]bool{}
			}
			c.holdsNothing[name] = true
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

// converge settles operation as the engine does: while the answer leaves a declared resource not present, it validates with what every answer so far reported of each resource, and sends operation again when an answer asks for a re-drive; a snapshot reports no resource, so it drops no member and settles once it stops answering pending.
func (c *blockCheck) converge(operation string) (contract.Response, bool) {
	recorded := maps.Clone(c.desired.RecordedResources)
	dropped := map[string]contract.RecordedResource{}
	call, deadline := operation, time.Now().Add(settleBudget)
	for {
		d := c.desired
		d.RecordedResources = recorded
		_, resp, ok := c.settleOK(call, d)
		if !ok {
			return resp, false
		}
		recorded = c.recordReported(recorded, resp)
		if call == operation && c.snapshotting == nil {
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
	if c.snapshotting != nil {
		return !resp.Pending
	}
	for _, name := range unitNames(c.spec, resp.Resources) {
		o := resp.Resources[name].Observe
		if o == nil || o.State != contract.Present || (c.afterRestore && o.Unhealthy != nil) {
			return false
		}
	}
	return !resp.Pending
}

func (c *blockCheck) unsettled(operation string, resp contract.Response) {
	if c.snapshotting != nil {
		c.problem("the snapshot of %q still answered pending after %s: %s", c.snapshotting.unit, settleBudget, resp.StatusDetail)
		return
	}
	named := false
	for _, name := range unitNames(c.spec, resp.Resources) {
		o := resp.Resources[name].Observe
		switch {
		case o != nil && o.State != contract.Present:
			c.resourceProblem(name, "observed %s after %s and validates for %s, want present", o.State, operation, settleBudget)
			named = true
		case o != nil && c.afterRestore && o.Unhealthy != nil:
			c.resourceProblem(name, "reads unhealthy after the restore: %s (%s)", o.Unhealthy.Cause, o.Unhealthy.Detail)
			named = true
		}
	}
	if !named {
		c.problem("%s did not settle every declared resource present within %s: %s", operation, settleBudget, resp.StatusDetail)
	}
}

func (c *blockCheck) sendOK(operation string, d contract.Desired) (int, contract.Response, bool) {
	before := c.repositoryWrites()
	status, resp, err := c.send(operation, d)
	if operation == "validate" {
		c.noWriteOnReadOnly(before, resp)
	}
	return c.answeredOK(operation, status, resp, err)
}

// send sends a snapshot the drill settles for its unit, each resend carrying the first send's createdAt as settlesAt as the engine resends a pending snapshot, and any other request as it stands.
func (c *blockCheck) send(operation string, d contract.Desired) (int, contract.Response, error) {
	s := c.snapshotting
	if operation != contract.OperationSnapshot || s == nil {
		return c.server.send(operation, d)
	}
	req := requestFor(operation, d)
	req.Unit = s.unit
	now := time.Now()
	if s.first.IsZero() {
		s.first = now
	} else {
		req.SettlesAt = &s.first
	}
	return c.server.post(req, now)
}

func (c *blockCheck) repositoryWrites() map[string]int {
	repos, _ := c.repositories()
	counts := make(map[string]int, len(repos))
	for name, fake := range repos {
		counts[name] = len(fake.Writes())
	}
	return counts
}

// noWriteOnReadOnly fails a block that wrote on a validate that settles no verb, which a fake repository shows by taking a write and the kit shows by refusing one. A write to anything the check does not fake stays unseen.
func (c *blockCheck) noWriteOnReadOnly(before map[string]int, resp contract.Response) {
	repos, _ := c.repositories()
	for _, name := range sortedKeys(repos) {
		for _, w := range repos[name].Writes()[before[name]:] {
			c.problemOnce("block %q wrote on a validate that settles no verb: %s %s", c.desired.BlockID, w.Method, w.Path)
		}
	}
	refusals := []string{resp.Error}
	for _, name := range sortedKeys(resp.Resources) {
		for _, call := range []*contract.CallOutcome{resp.Resources[name].Ensure, resp.Resources[name].Observe, resp.Resources[name].Delete} {
			if call != nil {
				refusals = append(refusals, call.Error)
			}
		}
	}
	for _, text := range refusals {
		if strings.Contains(text, contract.ErrReadOnly.Error()) {
			c.problemOnce("block %q wrote on a validate that settles no verb, and the kit refused it: %s", c.desired.BlockID, text)
		}
	}
}

func (c *blockCheck) problemOnce(format string, args ...any) {
	if c.reported == nil {
		c.reported = map[string]bool{}
	}
	if text := fmt.Sprintf(format, args...); !c.reported[text] {
		c.reported[text] = true
		c.problem("%s", text)
	}
}

func (c *blockCheck) settleOK(call string, d contract.Desired) (int, contract.Response, bool) {
	if call != "validate" {
		return c.sendOK(call, d)
	}
	status, resp, err := c.server.settle(d)
	return c.answeredOK(call, status, resp, err)
}

func (c *blockCheck) answeredOK(operation string, status int, resp contract.Response, err error) (int, contract.Response, bool) {
	if err != nil {
		c.problem("%s could not be sent or its answer read: %v", operation, err)
		return status, resp, false
	}
	if status == http.StatusOK {
		c.unhealthyCauses(resp)
		c.removingWaits(resp)
		c.waitsOnPersonNameAction(resp)
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

// waitsOnPersonNameAction fails an observe answering a wait on a person beside a state other than unknown, which the engine never reads, or naming no action, since the platform shows it as the resource's status.
func (c *blockCheck) waitsOnPersonNameAction(resp contract.Response) {
	for _, name := range sortedKeys(resp.Resources) {
		o := resp.Resources[name].Observe
		if o == nil || o.WaitingOnPerson == nil || c.failedWaits[name] {
			continue
		}
		switch {
		case strings.TrimSpace(o.WaitingOnPerson.Action) == "":
			c.resourceProblem(name, "observed waitingOnPerson with a blank action, which must say what the person does and where")
		case o.State != contract.Unknown:
			c.resourceProblem(name, "observed %s with waitingOnPerson, but only an unknown resource waits on a person", o.State)
		default:
			continue
		}
		if c.failedWaits == nil {
			c.failedWaits = map[string]bool{}
		}
		c.failedWaits[name] = true
	}
}

// requiredOutputs reads a provided resource's secret outputs from its own entry in the answer's resourceSecretOutputs, and from the merged secretOutputs only for an answer carrying no resourceSecretOutputs, as the platform judges them.
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
		secrets := resp.ResourceSecretOutputs[name]
		if len(resp.ResourceSecretOutputs) == 0 {
			secrets = resp.SecretOutputs
		}
		for _, out := range t.Outputs {
			if !out.Required {
				continue
			}
			_, plain := o.Outputs[out.Name]
			_, secret := secrets[out.Name]
			if !plain && !(out.Secret && secret) {
				c.resourceProblem(name, "observed present after %s without output %q, which its type %s marks required", operation, out.Name, o.Type)
			}
		}
	}
}

// listTagged lists what the provider holds that carries the subscription's id.
func (c *blockCheck) listTagged() ([]observe.Tagged, error) {
	if l, ok := c.in.Provider.(desiredLister); ok {
		return l.listTaggedWith(context.Background(), c.desired)
	}
	return c.in.Provider.ListTagged(context.Background(), c.desired.SubscriptionID)
}

func (c *blockCheck) taggedMatchDeclared() {
	tagged, err := c.listTagged()
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
		case c.providerHoldsNothing(t.Type):
			c.resourceProblem(t.ID, "it carries the subscription's id as type %s, but the type snapshot marks %s providerHoldsNothing, so no provider object stands for a resource of it", t.Type, t.Type)
		case room[t.Type] == 0:
			c.resourceProblem(t.ID, "it carries the subscription's id as type %s, but neither the manifest's resources nor the parts their types list account for another of that type", t.Type)
		default:
			room[t.Type]--
		}
	}
	listable := maps.Clone(c.desired.RecordedResources)
	maps.DeleteFunc(listable, func(name string, r contract.RecordedResource) bool {
		return c.holdsNothing[name] || c.providerHoldsNothing(r.Type)
	})
	for _, name := range untagged(listable, tagged) {
		c.resourceProblem(name, "untagged: the provider lists no %s carrying the subscription's id for it, so the platform can neither find it nor tear it down; tag it where Ensure creates it", c.desired.RecordedResources[name].Type)
	}
}

// providerHoldsNothing reports whether the block's type snapshot marks typ as a type no provider holds an object of.
func (c *blockCheck) providerHoldsNothing(typ string) bool {
	t, ok := c.in.Config.Types.Lookup(typ)
	return ok && t.ProviderHoldsNothing
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
	c.emptyIdentityUnsubscribe()
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
	c.emptyIdentityUnsubscribe()
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

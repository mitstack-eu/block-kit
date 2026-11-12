# Testing a block with kittest

This page is for a block author writing the tests blockverify requires, and
any others that prove the block behaves. The kit's `verify/kittest` package
holds the checks: each serves your block through `handler.New`, as the engine
reaches it, with signed requests, and fails your test with one error per
problem. The example block's
[`block_test.go`](../examples/hetzner-firewall/block_test.go) uses the three
checks below against a fake Hetzner API.

## The tests blockverify requires

| Test name | Check it calls | What it proves |
|---|---|---|
| `TestDeclaredResources` | `kittest.AssertBlock` | Each declared resource is created, tagged, observed from the provider and gone after unsubscribe |
| `TestReplacementDrill`, for a block whose manifest hands state over by snapshot, as [Proving a replacement](#proving-a-replacement) explains | `kittest.AssertReplacement` | Replacing the block's server loses no datum, and every resource reads present and healthy after the restore |
| A name starting `TestUnsubscribeDeletes` | `kittest.AssertTeardownAfterBrokenSetup`, or your own test code | An unsubscribe deletes what a subscribe made |
| A name starting `TestNoSecretLeak` | `kittest.AssertNoSecretLeak` | No secret reaches an answer, a log line or `/logs` |
| `TestNoSecretLeak_VerifyConnection`, for a capability operator that answers `verify-connection` | `kittest.AssertNoSecretLeak` with a `verify-connection` request | The entered credential reaches no answer, log line or `/logs` |
| `TestGuardedClient`, for the same operator | `kittest.AssertGuardedClient` | The client the operator calls its vendor with refuses plain http, a redirect, an undeclared host and a loopback, private, link-local or unspecified address, each before a connection opens |

blockverify finds each test by name, runs it, and fails the block when the
test is missing or fails, as [Running the block checks](block-checks.md)
describes. A test file holding one of the last two must not build
`kittest.FakeResource`, the kit's own stand-in block, because a test over the
kit's fake judges the fake instead of your block.

## Proving the guards with AssertBlock

`kittest.AssertBlock` takes a `kittest.Block` and runs your block through
subscribe, upgrade, validate and unsubscribe. The example's test passes the
same config `main` serves, the fake provider, and a subscription:

```go
func TestDeclaredResources(t *testing.T) {
	fake := newFakeHetzner(t, nil)

	kittest.AssertBlock(t, kittest.Block{
		Config:   testConfig(t, fake.url, nil),
		Provider: kittest.ListerProvider{Lister: observe.HetznerLabelled{API: newHetzner(fake.url)}, Loser: fake},
		Desired:  subscription,
	})
}
```

| Field | What you supply |
|---|---|
| `Config` | The `handler.Config` your `main` hands `handler.New`, of which the check replaces only the signing key and the JWKS address |
| `Provider` | Your fake provider, which lists everything carrying the subscription's id and can lose one resource behind the block's back. A block declaring a file-landing type passes a `kittest.RepositoryProvider`, whose `Repository` method hands the check the `landingtest` fake its config lands on. A block landing files in more than one repository passes a `kittest.RepositoriesProvider` instead, whose `Repositories` method hands one fake per repository, keyed by `owner/name` |
| `Desired` | The subscription, with a `SubscriptionID`, a `BlockID`, an `OrgID` and the secrets your code reads |
| `Upgrade` | The `Desired` of an upgrade, the call that brings a subscription to a new version of the block, which the check sends after subscribe. It is required when a declared type is stateful |
| `Requirements` | For each requirement and each type that may meet it, the outputs the check binds, keyed by requirement and then type |

A `kittest.Provider` has two methods. `ListTagged` lists what carries the
subscription's id, and `Lose` removes one resource from the provider without
telling the block. `kittest.ListerProvider` builds one from a lister in the
`observe` package and your fake's `Lose`, and hands the lister the
subscription's `Desired`, which carries the requirement bindings.

The handler observes each resource once per call and never waits, so a
subscribe whose resources are still on their way answers pending. The check
settles that answer the way the engine settles it: it sends
`validate` calls, and a fresh subscribe when one answers `redrive`, until every
declared resource reads present or ten seconds pass. Every declared resource
must then report present with an identity, from its ensure or its observer, as
[Running the block checks](block-checks.md#how-a-block-reports-each-resources-identity)
explains. The check then
holds the block to the guards below and logs the name of every guard that ran:

| Guard | What the block must do |
|---|---|
| `required-outputs` | After subscribe and after an upgrade, each resource `spec.provides` names reports every output its type marks required |
| `tagging` | Everything the provider lists under the subscription's id belongs to a recorded resource, and every recorded resource is listed. A resource whose type `Config.Types` marks `providerHoldsNothing` needs no listed object, and an object listed as such a type fails |
| `replacement` | With `Upgrade` set, a resource the upgrade replaces is named in `replaces` by the validate before it |
| `observe-asks-the-provider` | Each resource the check loses through `Provider.Lose` then reports not present |
| `absence-after-unsubscribe` | After unsubscribe every declared resource reports absent, except one whose type `Config.Types` marks `endsWithSubscription` |
| `kit-observer` | A resource whose type carries an observe spec has no observer of the block's own in `Config.Observers` or `Config.TypeObservers`, since `Config.KitObserver` reads it. The kit observer must also be able to run that spec, which needs at least one usable read, the read that proves the resource ready for use. A failure here names the resource and type, and [Observing resources](observe.md#proving-a-resource-usable) shows how to fix it |
| `probe-reads` | A probe of such a resource makes no more provider reads than its type's `maxReads`, counted by a `Provider` that implements `kittest.ReadCounter` |
| `empty-identity-unsubscribe` | A resource whose identity is empty, as a failed create leaves it, still gets a true answer when the engine tears it down. The check sends each recorded resource a `validate` that names it, carries no identity and sets `ResourceUnsubscribing`, so its observer sees an empty `r.Identity` and `r.Unsubscribing` true. It sends it twice: while `Provider` still holds the resource, and after the check has lost every resource through `Provider.Lose`. With nothing held the observer must answer absent. While `Provider` holds one it must answer present, and absent fails. An unknown answer passes only if it sets `WaitingOnPerson`, the response asks for a redrive, or its status detail names an object `Provider` holds. A resource whose type `Config.Types` marks `providerHoldsNothing` must answer absent both times, since the provider holds none of it. A resource whose type [lands files](#proving-a-block-that-lands-files) is left out, because its landing record, the identity that names the files and repository it landed, is the only way to find what it landed |

An observer that answers unknown only because `r.Identity` is empty leaves the
engine's unsubscribe check repeating without an answer. When `r.Unsubscribing` is true and
the identity is empty, look the resource up by what the provider holds under
the subscription, such as a tag or a label, and answer absent when nothing is
found. `examples/hetzner-firewall/config.go` does this with a label selector.

On every answer, the check also reads each observe that names a resource
unhealthy. Its cause must be one the resource's type lists under `failures`
in `Config.Types`, and a problem names the resource, the cause and the type.
A cause beside a state other than present fails too, because the engine
reads one only beside present. In the same way, the check fails an observe
that sends `Removing` beside any state but present, or with a blank
`WaitsOn`, and names the resource, as
[A resource being removed](observe.md#a-resource-being-removed) explains. It
also fails an observe that sends `WaitingOnPerson` beside any state but
unknown, or with a blank `Action`, as
[A resource waiting on a person](observe.md#a-resource-waiting-on-a-person)
explains.

The tagging guard checks both directions. Everything the provider lists must
match, by type and count, a recorded resource or one of its parts, the objects
the snapshot says exist only to serve one resource of a type. The guard adds
up the parts of each type across every recorded resource, and it does not tie
a part to the resource that owns it. In the other direction, each recorded
resource needs a listed object of its type, so a resource the block forgot to
tag fails, named as `untagged`. A resource claims first the listed object
whose id equals one of its identity values, and only then any other object of
its type, so the guard names exactly the resource left untagged.

Each member of an entry marked many is a recorded resource of its own, so two
members of one entry account for two listed objects. When an upgrade stops
reporting a member, the check drops it from the record, as the engine does. It
then sends a validate naming that member, which must read absent, and runs the
tagging guard again, so a server the block left behind fails by its id.

A requirement listing more than one type runs the check once per listed type,
with `Requirements` giving each type's outputs, and each problem names the
type. `kittest.CheckBlock` does the same work and returns the problems as
strings.

## Proving a block that lands files

A file-landing type is one whose resource lands files on an app's main, so it
keeps a landing record in its identity, the record
`capabilities/github/landing` writes. The types are `github.commit`,
`github.scaffold-commit`, `github.scaffold-contribution` and
`github.workflow-file`, as `landing.LandsFiles` lists them. A type's
`endsWithSubscription` mark in the catalogue does not change whether the check
runs. Unsubscribe must
remove every file such a block created that main still carries unchanged, keep
each file the app changed, and report the kept files.

For a block declaring one of these types, `kittest.AssertBlock` reads main
after unsubscribe, in the fake repository each landing record names. Before the
unsubscribe it changes, in each repository, the first file a landing record
created there, as an app editing a landed file would. A record naming a
repository the Provider hands no fake for fails the block.

A repository the block deletes whole holds nothing to remove, as ADR 0107 of
the platform says. So the check reads nothing from a fake the block deleted
through `DeleteRepository`, and that repository's records pass. A block that
lands workflows in a CI repository of its own and deletes that repository at
unsubscribe passes a `kittest.RepositoriesProvider` naming both the app's
repository and the CI repository.

A `github.scaffold-contribution` lands nothing itself, because the app-scaffold
block commits its files, as
[Removing the files a block landed](helpers.md#removing-the-files-a-block-landed)
describes. So for such a resource the check first writes each file its
`scaffoldContribution` output offers onto the fake main. It fails an offered
file whose content's git blob hash the resource's landing record does not
name, since unsubscribe could not recognise the file to remove it.

The tagging guard reads the block's lister after these writes, so the lister
must list each contribution whose files the check wrote, as
`observe.GitHubTagged` does. Sometimes the check writes no file for a
contribution, because it offers none or main already held each one, and main
holds no file the contribution's record created. GitHub then holds nothing of
it, and the guard needs no listed object for it.

The guard runs the fake GitHub API the test hands it, so passing it says
nothing about your real client. Both implement `observe.GitHubTagAPI`,
`MainFile` included, and the compiler holds the real client to it wherever
it serves an `observe.GitHubTagged`.

After the unsubscribe, the check fails the block on each of these, naming the
path or branch:

| What the check finds | The problem it reports |
|---|---|
| A created file still at its landed blob | `main of acme/weather still carries Dockerfile unchanged after unsubscribe, want it removed` |
| The changed file gone or rewritten | `unsubscribe deleted or rewrote CLAUDE.md, which the app changed after the landing, want it kept` |
| The changed file kept, with no delete outcome naming it kept | `unsubscribe kept CLAUDE.md, which the app changed, but no delete outcome answers it kept with reason changed` |
| A branch under `mitstack/<subscription>/` | `branch mitstack/sub-block/land under the subscription's prefix remains on acme/weather after unsubscribe, want it deleted` |

A block holds all four when its `DeleteKeeping` calls `landing.Remove` and
returns the `Result.Kept` it gets back under the resource's name, as
[Serving lifecycle calls](handler.md#what-the-handler-calls-for-each-call)
describes. The check lives in `verify/kittest/block.go`, in
`commitContributions`, `changeALandedFile` and `readMain`.

## Proving teardown after a broken setup

A subscribe can fail part-way, after it has already written something to the
provider. The engine then sends an unsubscribe, which may reach a restarted
operator with no record of what that subscribe did.
`kittest.AssertTeardownAfterBrokenSetup` proves such a teardown still finds
everything:

1. It runs one subscribe with nothing broken and counts the side effects the
   block's fakes report.
2. It runs one subscribe per side effect, breaking the first, then the second,
   and so on through the last.
3. After each broken subscribe, it starts a fresh process of the block and
   sends an unsubscribe carrying no recorded resources and no prior outputs.
4. It fails the test when the fake provider still holds anything for the
   subscription, naming the broken side effect and what was left.

You pass it a `kittest.BrokenSetup` holding your `Manifest`, your type
snapshot as `Types`, the subscription as `Desired`, and `NewProvider`, which
returns an empty fake provider for each run. The fake is a
`kittest.FaultProvider`: its `Process` returns your block's `handler.Resource`
and observers as a newly started operator builds them, and its `Holds` names
everything it holds for a subscription. Each fake calls
`faults.SideEffect("<what>")` before every change it makes and returns the
error it gets back. A fake that makes several changes in one call reports
each, so the check can break the call between them. The example's test:

```go
func TestUnsubscribeDeletes_AfterASetupBrokenAtAnySideEffect(t *testing.T) {
	cfg := testConfig(t, "", nil)

	kittest.AssertTeardownAfterBrokenSetup(t, kittest.BrokenSetup{
		Manifest:    cfg.Manifest,
		Types:       cfg.Types,
		Desired:     subscription,
		NewProvider: func(faults *kittest.Faults) kittest.FaultProvider { return newFakeHetzner(t, faults) },
	})
}
```

The check counts side effects by position, so the block must make them in the
same order on every run. A block that recovers from a broken side effect, say
by retrying it, may answer the subscribe with 200, and the check still checks
the teardown that follows. `kittest.CheckTeardownAfterBrokenSetup` returns the
problems as strings.

## Proving no secret leaks

`kittest.AssertNoSecretLeak` drives your own handler with signed requests that
carry seeded secrets. It fails the test for each place a secret or an OBO
token shows up: an answer from `/hook`, the standard logger's output, or a
line `GET /logs` serves. A problem names the place and shows the secret only
in redacted form.

Seed each secret where your operator reads it: under `secrets`, the
connection secrets, under `secretInputs`, or under `appSecrets`. Mark a request
`Fails` when your code answers it with an error, so the error path runs and
its text is checked too. The check adds a fresh `nonce`, `createdAt` and
`idempotencyKey` to each body, and an OBO token minted for its `blockId`,
`orgId`, `appId` and `subscriptionId`. A body's `blockId` must therefore be
your manifest's `metadata.id`. The example's test seeds the Hetzner token and
fails one subscribe on a firewall name another firewall holds:

```go
kittest.AssertNoSecretLeak(t, kittest.SecretLeak{
	Secrets: []string{apiToken},
	Serve: func(logs *webhook.LogBuffer) (http.Handler, *kittest.Signer) {
		signer := kittest.NewSigner(t, "example", 1)
		cfg := testConfig(t, fake.url, logs)
		cfg.JWKSURL = signer.JWKSServer(t)
		h, err := handler.New(cfg)
		if err != nil {
			t.Fatalf("handler.New: %v", err)
		}
		return h, signer
	},
	Requests: []kittest.LeakRequest{
		{Name: "subscribe", Body: subscribe("sub_1")},
		{Name: "subscribe whose firewall name another firewall holds", Body: subscribe("sub_taken"), Fails: true},
	},
})
```

The check also reports a fixture whose passing would prove nothing: a seeded
secret that is empty or in no request body, no request marked `Fails`, a
`Fails` request that answers below 400, a body the kit's decoder refuses, an
answer of 401, a `Serve` that replaces the standard logger's writer, and a
`GET /logs` that answers other than 200 or serves no lines. While it runs, the
check sends the standard logger to its own capture as well as to `/logs` and
the earlier writer, so the test must not run in parallel with a test that
logs. `kittest.CheckNoSecretLeak` returns the problems as strings.

## Proving a replacement

A block that keeps data declares it under `spec.continuity`, as [The state a
block keeps](manifest.md#the-state-a-block-keeps) explains, and carries it
across a replacement of its server with the `snapshot` and `restore`
operations. A replacement is an upgrade the user confirms in which the block
creates a resource that holds data again, such as its server, on the same
subscription. A unit is one entry under `spec.continuity`, and a handover is
the string a snapshot answers for the restore to read. A block whose manifest
gives a unit `handover: snapshot` proves the whole sequence with
`kittest.AssertReplacement` in a test named `TestReplacementDrill`, which
blockverify's `replacement` suite requires. A block that declares no state, or
only state handed over by `none`, writes no such test.

```go
func TestReplacementDrill(t *testing.T) {
	dir := t.TempDir()
	kittest.AssertReplacement(t, kittest.Replacement{
		Block: testBlock(t, dir), // your own helper, described below
		Unit:  "app-data",
		Data:  &volumeData{dir: dir},
	})
}
```

The drill and the block must look at the same data. Here `testBlock` is a
helper of your own that builds the `kittest.Block` that `TestDeclaredResources`
passes, with the block under test keeping the unit `app-data` under `dir`. The
block's `Snapshot` then copies that directory out and its `Restore` writes it
back, while the drill writes, destroys and reads the same directory through
`volumeData`.

| Field | What you supply |
|---|---|
| `Block` | The same `kittest.Block` `AssertBlock` takes, so the drill serves your block through `handler.New` as the engine reaches it |
| `Unit` | The name of a `spec.continuity` entry whose handover is `snapshot` |
| `Data` | Your view of the data the unit keeps, a `kittest.DrillData`, shown below. `Replace` destroys the live state as a lost server would and leaves it empty. The drill reads the state after `Replace` and fails when a known value is still there, since a value that was never lost proves no restore |

`kittest.DrillData` is three methods over the unit's live data, which the
drill calls and your test implements:

```go
type DrillData interface {
	Write(ctx context.Context, data map[string]string) error
	Read(ctx context.Context) (map[string]string, error)
	Replace(ctx context.Context) error
}
```

For a unit kept as files in one directory, the `volumeData` of the example
above writes each value as a file, reads the files back, and empties the
directory. The drill's values are named `alpha`, `beta` and `gamma`, which are
safe as file names, and the code needs the `context`, `os` and `path/filepath`
packages:

```go
type volumeData struct{ dir string }

func (v *volumeData) Write(_ context.Context, data map[string]string) error {
	for name, value := range data {
		if err := os.WriteFile(filepath.Join(v.dir, name), []byte(value), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (v *volumeData) Read(context.Context) (map[string]string, error) {
	entries, err := os.ReadDir(v.dir)
	if err != nil {
		return nil, err
	}
	data := map[string]string{}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(v.dir, e.Name()))
		if err != nil {
			return nil, err
		}
		data[e.Name()] = string(raw)
	}
	return data, nil
}

func (v *volumeData) Replace(context.Context) error {
	if err := os.RemoveAll(v.dir); err != nil {
		return err
	}
	return os.Mkdir(v.dir, 0o700)
}
```

The drill subscribes the block and writes three known values through
`Data.Write`. It sends a `snapshot` and keeps the handover the answer
publishes under `contract.HandoverOutputKey`, calls `Data.Replace`, loses each
stateful resource from the provider, and sends an `upgrade` so the block
creates them again. It then sends a `restore` carrying the handover and
validates every declared resource until it reads present and healthy. Every
request from the snapshot on carries `replacing: true`, which the block reads
as `d.Replacing`, as
[Snapshotting and restoring state](handler.md#snapshotting-and-restoring-state)
explains. The drill reads the data back through
`Data.Read` and fails with one message per problem, as in these two:

```text
replacement lost datum "beta"
resource "aws.ebs-volume": reads unhealthy after the restore: the volume is still creating, attaching or detaching (the restored volume reports no attachment)
```

A value that comes back changed fails as `replacement changed datum`. A
resource that reads unhealthy passes only if it turns healthy within the same
ten seconds `AssertBlock` allows. The drill logs one line that
names its unit and the stages it ran, as in `kittest.AssertReplacement drill
of "app-data" ran: subscribe, write, snapshot, replace, upgrade, restore,
observe, read-back`. A block that hands several units over by snapshot calls
`AssertReplacement` once for each, since blockverify reads those lines and
fails a block that left one undrilled. `kittest.CheckReplacement` returns the
problems as strings.

## Proving a re-driven subscribe resumes the work

A block with work that outlasts one call, as [Serving lifecycle
calls](handler.md#work-that-outlasts-one-call) describes, proves its re-drive,
the fresh subscribe the engine sends once the long work is done, with
`kittest.AssertVerbSemantics`. It takes a `kittest.VerbSeams`, a struct of
functions your test supplies, each driving one property against your own
handler: `Subscribe`, `Unsubscribe`, `UnsubscribeWhenAbsent`, `Upgrade`,
`ValidateWhileConverging` and `ValidateWhenDead` are required, and `Redrive` is
set only by a block with such work. `Redrive` returns a `kittest.RedriveRun`:

| Field | What the run reports |
|---|---|
| `FirstPending` | The first subscribe answered `pending:true`, leaving the work in flight |
| `Redrive` | The validate after the work finished answered `redrive` beside `pending` |
| `Settled` | The re-driven subscribe answered without `pending` |
| `Changed` | How many resources the first subscribe made that the re-driven one changed |

The check fails a run that misses any of the first three, and one whose
`Changed` is above zero, since a re-drive must resume from what it observes
instead of redoing work. `kittest.CheckVerbSemantics` returns the problems as
strings.

`kittest.VerbSeamsOverHandler(t, manifest, fake)` checks what the kit's
handler does with your manifest before your own code is involved. It returns a
`kittest.VerbSeams` with every function but `Redrive` filled in, each driving
a handler that serves your manifest through `handler.New` with `fake`, a
`kittest.FakeResource`, as the block's code and as the observer of every
resource the manifest declares. Each request is signed for your manifest's
block. The zero `FakeResource` behaves correctly, so this test passes when the
handler serves every verb property for the resources your manifest declares:

```go
func TestManifestServesEachVerb(t *testing.T) {
	kittest.AssertVerbSemantics(t, kittest.VerbSeamsOverHandler(t, manifestYAML, &kittest.FakeResource{}))
}
```

The fake replaces your block's code, so this proves nothing about it. The
functions you write against your own handler, described above, are the ones
that judge your code.

## Reading a manifest in a test

`kittest.ReadManifest` parses a manifest file through `contract.ParseManifest`
and fails the test, naming the path, when the file cannot be read or the
parser refuses it. The `kittest.Manifest` it returns embeds the parsed
`contract.Manifest`, so `ID()`, `Version()` and `Block` stay reachable.

`Section` reads a value by dotted key path, such as `spec.lifecycle`, and
returns its typed Go value, so `spec.resources` is a
`[]contract.ManifestResource`. It returns false for a key the manifest leaves
unset, and fails the test for a key the types do not name. `SpecScalar` reads
`spec.<key>` as trimmed text:

```go
m := kittest.ReadManifest(t, "manifest.yaml")
lifecycle, ok := m.Section("spec.lifecycle")
description, ok := m.SpecScalar("description")
```

## Running a workflow script in a test

A test that matches text in a rendered GitHub Actions workflow passes over a
shell error inside a `run:` block, so `kittest` runs the script instead.
`kittest.ReadWorkflowScript` takes the rendered YAML, a job key and the
`name:` of one entry in that job, and returns the entry's `run:` body and `env`.
`kittest.RunWorkflowScript` runs that body under `bash -euo pipefail`, which is
stricter than GitHub's default, and returns its exit code, stdout and stderr.

The body sees no environment from the test process. Its `PATH` holds only the
fakes the test writes and the real tools it names, so a command the test did
not supply exits 127. The reader evaluates no `if:` and no `${{ }}`
expression, and it returns an error naming the line for a YAML shape it does
not read, such as a folded `>` body or an alias.

```go
script, err := kittest.ReadWorkflowScript(rendered, "release", "Cut the tag")
if err != nil {
	t.Fatal(err)
}
res := kittest.RunWorkflowScript(t, script, kittest.ScriptRun{
	Fakes: map[string]string{"gh": `echo "gh $*"`},
	Tools: []string{"git"},
	Env:   map[string]string{"VERSION": "1.2.3"},
})
if res.ExitCode != 0 {
	t.Fatalf("script exited %d: %s", res.ExitCode, res.Stderr)
}
```

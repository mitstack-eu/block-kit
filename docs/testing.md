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
| A name starting `TestUnsubscribeDeletes` | `kittest.AssertTeardownAfterBrokenSetup`, or your own test code | An unsubscribe deletes what a subscribe made |
| A name starting `TestNoSecretLeak` | `kittest.AssertNoSecretLeak` | No secret reaches an answer, a log line or `/logs` |

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
| `Config` | The `handler.Config` your `main` hands `handler.New`, of which the check replaces only the signing key, the JWKS address and the wait between observations |
| `Provider` | Your fake provider, which lists everything carrying the subscription's id and can lose one resource behind the block's back |
| `Desired` | The subscription, with a `SubscriptionID`, a `BlockID`, an `OrgID` and the secrets your code reads |
| `Upgrade` | The `Desired` of an upgrade, the call that brings a subscription to a new version of the block, which the check sends after subscribe. It is required when a declared type is stateful |
| `Requirements` | For each requirement and each type that may meet it, the outputs the check binds, keyed by requirement and then type |

A `kittest.Provider` has two methods. `ListTagged` lists what carries the
subscription's id, and `Lose` removes one resource from the provider without
telling the block. `kittest.ListerProvider` builds one from a lister in the
`observe` package and your fake's `Lose`, and hands the lister the
subscription's `Desired`, which carries the requirement bindings.

The check keeps the block's own `VerbBudget`, the most times a call observes
one resource while it waits for it, so a block that needs more observations
than its budget allows fails here as it would in production. A subscribe that
answers pending is settled the way the engine settles it: the check sends
`validate` calls, and a fresh subscribe when one answers `redrive`, until every
declared resource reads present or ten seconds pass. Every declared resource
must then report present with an identity, from its ensure or its observer, as
[Running the block checks](block-checks.md#how-a-block-reports-each-resources-identity)
explains. The check then
holds the block to five guards, the rules below, and logs the name of every
guard that ran:

| Guard | What the block must do |
|---|---|
| `required-outputs` | After subscribe and after an upgrade, each resource `spec.provides` names reports every output its type marks required |
| `tagging` | Everything the provider lists under the subscription's id belongs to a recorded resource, and every recorded resource is listed |
| `replacement` | With `Upgrade` set, a resource the upgrade replaces is named in `replaces` by the validate before it |
| `observe-asks-the-provider` | Each resource the check loses through `Provider.Lose` then reports not present |
| `absence-after-unsubscribe` | After unsubscribe every declared resource reports absent, except one whose type `Config.Types` marks `endsWithSubscription` |

The tagging guard checks both directions. Everything the provider lists must
match, by type and count, a recorded resource or one of its parts, the objects
the snapshot says exist only to serve one resource of a type. The guard adds
up the parts of each type across every recorded resource, and it does not tie
a part to the resource that owns it. In the other direction, each recorded
resource needs a listed object of its type, so a resource the block forgot to
tag fails, named as `untagged`. A resource claims first the listed object
whose id equals one of its identity values, and only then any other object of
its type, so the guard names exactly the resource left untagged.

A requirement listing more than one type runs the check once per listed type,
with `Requirements` giving each type's outputs, and each problem names the
type. `kittest.CheckBlock` does the same work and returns the problems as
strings.

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

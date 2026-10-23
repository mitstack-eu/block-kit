# block-kit

block-kit is the Go module you write a mitstack block's operator with. This
page, for a developer building a first block, explains what a block and its
operator are, follows one call into your code, names the packages to import
and the optional ones, and ends with a block to copy. Its commands name
v0.4.0, the first release under the name block-kit.

## What a block and its operator are

mitstack is a platform that runs managed services for apps. A block is one such
service, described by the `manifest.yaml` file in the block's repository. The
manifest names the block, its version and the resources it creates. A resource
is one object at a provider, such as a firewall in a Hetzner Cloud project.
Each resource has a resource type (the exact sort of object it is, written
`<capability>.<name>`, where the capability names the provider, as in
`hetzner.firewall`), and a name in the manifest, which is its type unless the
manifest gives another. Once created, a resource has an identity, the values
such as an id that find it at the provider, and outputs, the values such as an
address that other blocks of the app may read.

An app uses a block through a subscription, and the block marks everything it
creates with the subscription's id, as a tag or a label, so mitstack can always
find what a subscription owns. The engine, the part of mitstack that drives
every subscription, sends the block four lifecycle calls over HTTPS.
`subscribe` creates the resources, `upgrade` brings them to a new version of
the block, `validate` reports whether each one stands and changes nothing, and
`unsubscribe` deletes them. The operator is the program you write with this kit
to answer those calls, a small HTTP server shipped as a container image.

## How one lifecycle call reaches your code

Your block's code implements `handler.Resource`, an interface of three
methods. `Ensure` creates what is missing. `Delete` deletes, and returns any
outputs the answer should still carry once the resources are gone, usually
none. `Observe` only completes the interface, since observers answer in its
place, so a one-line stub like the example's will do. Each resource has an
observer, whose one method, `ObserveResource(ctx, ref)`, gets the resource's
name, type and identity and the call itself in a `contract.ResourceRef`, and
asks the provider about it. It returns a `contract.Observation`:
`contract.Present`, `contract.Absent` or `contract.Unknown`, and the
resource's outputs. The kit's `observe` package has observers for common
resource types, and you write one for any other type. `handler.New` takes your
code and the observers and returns the HTTP handler your operator serves at
your manifest's endpoint:

```mermaid
sequenceDiagram
    participant E as mitstack engine
    participant H as the handler in your operator
    participant B as your block code and its observers
    E->>H: POST /hook with a signed subscribe call
    Note over H: webhook checks the signature, then handler checks the call
    H->>B: EnsureResources(ctx, desired), which returns each identity
    loop until present, at most 20 times by default
        H->>B: ObserveResource(ctx, ref) on the resource's observer
        B-->>H: present, absent or unknown
    end
    H-->>E: the answer, with each resource and its outputs
```

1. The handler first runs the kit's `webhook` checks that mitstack signed the
   call, that it is under five minutes old and that it is not a replay. A call
   that fails one answers 401 and never reaches your code.
2. The handler decodes the call and checks a token mitstack signs into it,
   which names the block and subscription the call is for, so a call cannot be
   replayed to another block. A mismatch answers 401 too. The handler hands
   your code the call as a `contract.Desired`: the subscription, its settings,
   the provider credentials it carries and the resources mitstack recorded.
3. On `subscribe` and `upgrade`, the handler calls your `EnsureResources`,
   which does the work of `Ensure` and also returns each identity. Write it,
   as the example does, since kittest's checks need the identities; the
   handler calls `Ensure` only for code without it. On `unsubscribe` it calls
   `Delete`, and on `validate` neither.
4. The handler then waits for the call to settle, which means it asks each
   resource's observer until the provider shows the state the call asked for,
   20 times at most by default, where unknown means the provider cannot tell
   yet. A `validate` asks each observer once.
5. The handler answers with each resource's state and outputs. A call that
   has not settled answers that it is still in progress, and the engine sends
   it again later or asks with `validate`, so `Ensure` and `Delete` must be
   safe to repeat.

## What a block imports

A block must import the four packages at the root of this repository, and
its tests must import `verify/kittest`: your code is written against their
types, and blockverify, the check every block must pass before its image
ships, runs your tests' kittest checks. Everything else is optional, in
`helpers` when tied to no provider and in `capabilities` per provider:

| Package | Who imports it | What it gives you |
|---|---|---|
| `contract` | Every block | The call's types, such as `contract.Desired` and `contract.Observation`, and the manifest parser |
| `handler` | Every block | `handler.New`, which serves the lifecycle calls over your code |
| `observe` | Every block | Observers for common resource types, such as `observe.HetznerFirewall`, and listers, which your tests use to find everything marked with a subscription's id |
| `webhook` | Every block | The signature checks the handler runs, and `webhook.LogBuffer`, which holds the recent log lines mitstack reads from your operator |
| `verify/kittest` | Every block's tests | The checks blockverify requires your tests to run |
| `helpers/safe` | A block that logs values from a call, or calls an API at an address a call supplies | Secret scrubbing for logs, and an allowlist of the hosts the operator trusts, so a call cannot redirect it |
| `helpers/events` | A block that reports its own progress during long work | Progress reports to mitstack while a long call runs |
| `helpers/credrefresh` | A block whose short-lived credential must outlive one call | A cache that renews a credential before it expires |
| `helpers/hostexec` | A block that runs a shell script on a server | One script run over AWS Systems Manager or SSH |
| `helpers/reach` | A block that serves an app at a URL | A probe that records whether the app answers |
| `capabilities/aws/cloudid` | A block whose resources live in AWS | Short-lived AWS credentials for the AWS account a call names |
| `capabilities/github/ghapi` | A block that calls the GitHub API | A GitHub REST client whose host the operator decides |
| `capabilities/github/gitops` | A block that deploys to Kubernetes through Argo CD | Commits the block's files to a GitOps repository and judges the deploy, with test fakes in `capabilities/github/gitops/gitopstest` |

## A minimal block to copy

[`examples/hetzner-firewall`](examples/hetzner-firewall) is a complete block
that keeps one empty Hetzner Cloud firewall for each subscription, the
simplest resource a provider offers for free. Its manifest declares the one
resource, and its `verbConnections` asks mitstack to send the org's Hetzner
credentials with `validate` and `unsubscribe` calls too, which the observer
and `Delete` need. The manifest, from
[`examples/hetzner-firewall/manifest.yaml`](examples/hetzner-firewall/manifest.yaml):

```yaml
apiVersion: mitstack/v1beta
type: block
metadata:
  id: hetzner-firewall
  name: Hetzner firewall
spec:
  version: 0.1.0
  description: Keeps one empty Hetzner Cloud firewall for each subscription.
  resources: [hetzner.firewall]
  lifecycle:
    endpoint: https://hetzner-firewall.example.com/hook
    operations: [subscribe, unsubscribe, upgrade, validate]
    contract: kit/v1
    verbConnections:
      validate: [hetzner]
      unsubscribe: [hetzner]
```

The block's code, from
[`examples/hetzner-firewall/block.go`](examples/hetzner-firewall/block.go):

```go
package main

import (
	"context"
	"fmt"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/observe"
	"github.com/mitstack-eu/block-kit/webhook"
)

const firewallResource = "hetzner.firewall"

// block creates and deletes the subscription's firewall, and observe.HetznerFirewall reports whether it stands.
type block struct{ cloud *hetzner }

func (b block) EnsureResources(ctx context.Context, d contract.Desired) ([]contract.CreatedResource, error) {
	id, err := b.cloud.ensureFirewall(ctx, d)
	if err != nil {
		return nil, err
	}
	return []contract.CreatedResource{{Name: firewallResource, Type: firewallResource, Identity: map[string]string{"firewallId": id}}}, nil
}

func (b block) Delete(ctx context.Context, d contract.Desired) (map[string]string, error) {
	return nil, b.cloud.deleteFirewalls(ctx, d)
}

// Ensure and Observe complete handler.Resource, and the handler calls EnsureResources and the observer in their place.
func (b block) Ensure(ctx context.Context, d contract.Desired) error {
	_, err := b.EnsureResources(ctx, d)
	return err
}

func (b block) Observe(context.Context, contract.Desired) (contract.Observation, error) {
	return contract.Observation{State: contract.Unknown}, nil
}

func newConfig(cloud *hetzner, logs *webhook.LogBuffer) (handler.Config, error) {
	types, err := contract.ParseTypeSnapshot(typeSnapshot)
	if err != nil {
		return handler.Config{}, fmt.Errorf("reading type-snapshot.json: %w", err)
	}
	return handler.Config{
		Resource:  block{cloud: cloud},
		Observers: map[string]handler.Observer{firewallResource: observe.HetznerFirewall{API: cloud}},
		Manifest:  manifest,
		Types:     types,
		Logs:      logs,
	}, nil
}
```

`EnsureResources` reports the firewall under its name, its type
`hetzner.firewall`, and `newConfig` keys its observer by the same name. The
identity it reports, `firewallId`, is what `observe.HetznerFirewall` asks
Hetzner about, and `Delete` hands over nothing. The other files:

- `main.go` embeds `manifest.yaml` and `type-snapshot.json` as the `manifest`
  and `typeSnapshot` values above, and serves the handler on port 8080 with the
  URL of the keys mitstack signs calls with, from `MITSTACK_JWKS_URL`.
- `hetzner.go` makes the Hetzner Cloud calls and labels each firewall with the
  subscription's id.
- `type-snapshot.json` is the `hetzner.firewall` entry of mitstack's type
  catalogue, its list of every resource type and its outputs, and [Writing the
  manifest](docs/manifest.md) says where to read yours.
- `block_test.go` runs, against a fake Hetzner API, the three tests
  blockverify requires by name: `TestDeclaredResources`, which calls
  `kittest.AssertBlock`, and tests named `TestUnsubscribeDeletes...` and
  `TestNoSecretLeak...`, which call kittest's teardown and secret-leak checks.

To start your own block, copy the folder into a new repository, delete the
`replace` line from its `go.mod` and run `go mod tidy`. Change the module path,
`metadata.id` and the endpoint, swap the firewall for your resource in every
file, and run your tests and blockverify from the copy:

```sh
go test ./...
go run github.com/mitstack-eu/block-kit/verify/blockverify@v0.4.0 --standalone .
```

blockverify runs seven suites, groups of checks such as the one that runs your
three tests, on the directory `--standalone` names, and a passing block ends
with `VERDICT: PASS — 7 suite(s)`.

## The rest of the repository

`verify` also holds `blockverify` and tools that sign a published image's
origin, `examples` the example, `cmd` and `scripts` the kit's own commands,
and `board` mitstack's work-tracker tooling, a Go module no block uses. `docs` holds these pages:

| Page | What it covers |
|---|---|
| [Writing the manifest](docs/manifest.md) | Every manifest key, block ids, resource types and the type snapshot |
| [Serving lifecycle calls](docs/handler.md) | `handler.New`, the trust checks, each call, errors, retries and the answer |
| [Observing resources](docs/observe.md) | What an observer returns, the kit's observers, and the listers that find what a subscription holds |
| [Testing a block](docs/testing.md) | `kittest.AssertBlock` and the secret-leak and teardown checks |
| [Running the block checks](docs/block-checks.md) | What blockverify judges and where CI runs it |
| [The optional helpers](docs/helpers.md) | `helpers` and the AWS and GitHub capability packages |
| [Deploying through GitOps](docs/gitops.md) | The `gitops` package and Argo CD |
| [Maintaining the kit](docs/maintainers.md) | Releases, delivery records and the kit's own checks |

## Licence

block-kit is licensed under the Business Source License 1.1, whose full text
is in [the LICENSE file](LICENSE). You may read, copy and change the kit, and
use it in production only to build, run or operate what the mitstack platform
manages. `v0.4.0`, block-kit's first release, and every later one carry this
licence, and each becomes Apache-2.0 four years after it is published.

# The optional helpers

This page is for a block author deciding which of the kit's optional packages
a block needs. Every block imports the four core packages at the root, and
none of the packages here: you import one when your block does what it serves.
The `helpers` folder holds the ones tied to no provider, and the
`capabilities` folder holds the ones for one provider's resources. The `gitops`
package has [a page of its own](gitops.md).

| Package | Import it when your block |
|---|---|
| `helpers/safe` | Logs a value from a request, or calls an API whose host a request names |
| `helpers/events` | Reports progress during long work, or reads a platform route |
| `helpers/credrefresh` | Holds a short-lived credential across calls |
| `helpers/hostexec` | Runs a shell script on a server over AWS Systems Manager or SSH |
| `helpers/dockerhost` | Sets up a host that runs apps as Docker containers behind a caddy edge, or sizes and observes an app on one |
| `helpers/reach` | Serves an app at a URL and reports whether it answers |
| `helpers/healthz` | Serves its health check, which every block does |
| `helpers/reportproof` | Has its nodes sign the reports they post to mitstack |
| `capabilities/aws/cloudid` | Creates its resources in the org's AWS account |
| `capabilities/aws/apphostname` | Points an app's hostname in mitstack's apps zone at the app's host |
| `capabilities/github/ghapi` | Calls the GitHub REST API |
| `capabilities/github/landing` | Lands files in an app's repository, and must remove them at unsubscribe |
| `capabilities/hetzner/robot` | Adopts a Hetzner dedicated server through Hetzner's Robot webservice |

## Keeping secrets out of logs and requests out of hosts

`helpers/safe` holds two security rules an operator must keep. The handler
already scrubs the request's secrets from every answer and every log line it
writes, so you need this package only for what your own code logs or calls.

The first rule is that log sanitising and secret scrubbing are two separate jobs.
`safe.Scrub(s, secrets...)` replaces each secret of four or more characters
with `[REDACTED]`, and `safe.LogValue(s)` removes the line breaks and control
characters a crafted value could use to forge a log line.
`safe.LogSafe(s, secrets...)` does both, in that order:

```go
log.Printf("creating the firewall for %s", safe.LogSafe(d.SubscriptionID, d.Secrets["hetzner"]["api_token"]))
```

The second rule is that the operator, never the request, picks the host it
sends a token to. A request may carry an endpoint, such as a GitHub instance's
API address, and building a URL from it would let a forged request send the
token anywhere. `safe.TrustedBase(endpoint, allow)` compares the endpoint with
a `safe.Allowlist` you build from constants or operator configuration, and
returns a `safe.Base` built from the allowlist entry. It refuses an endpoint
that is not `https` or not on the list. `base.URL(path)` then joins a path onto
the trusted host.

## Reporting progress and reading the platform

The handler sends the lifecycle events for each call itself, as [Serving
lifecycle calls](handler.md#lifecycle-events) describes. `helpers/events` lets
your code send more of them during long work. `events.EmitProgress(ctx,
detail)`, `events.EmitCompleted(ctx)` and `events.EmitFailed(ctx, detail)` post
an advisory event to the request's events callback, the URL and token the
platform puts in every request. They do nothing when the request carries no
callback, and `events.ScrubDetail` removes secret-shaped `key=value` pairs from
a detail before it leaves the process.

Each event carries a `sequence`, a whole number the helper sets, so your code
passes nothing for it. Every call the platform sends carries an idempotency
key, a string it repeats when it sends the same call again. The platform keeps
one event per sequence under that key. Without a sequence it would take a
progress event identical to an earlier one for a resend and keep only the
first. The helper reads the number from the clock in microseconds and keeps it
rising within the process. A call sent again often lands in a fresh process,
and the clock still gives that process numbers the first one never sent. The
helper builds and posts a body like this one for a progress event:

```json
{"subscriptionId": "sub_3f9c2b1e8d7a6c5b", "token": "<the callback's token>", "operation": "subscribe",
 "status": "progress", "detail": "creating instance", "idempotencyKey": "sub_3f9c2b1e8d7a6c5b:subscribe:deadbeef",
 "sequence": 1793650412345678}
```

`events.PlatformFrom(ctx)` returns the platform's base URL, the callback URL
without its `/v1/webhooks/operator-events` path, and the callback's token. A
block calls another platform route with that base URL and token, acting for
the call it serves, as `observe.NodeReportsClient` does to read node reports.

## Holding a short-lived credential

`helpers/credrefresh` caches a credential of any type and renews it before it
expires. `credrefresh.New(resolve)` takes a function that mints a credential
and its expiry, and `Get(ctx)` returns a usable one. It mints a fresh one
inside the renewal margin, `credrefresh.DefaultRenewMargin`, ten minutes, which
the `RenewMargin` field overrides. It fails closed: a
mint that fails or returns an expired credential is an error, never a stale
credential, and a zero expiry counts as unknown and is never cached.

```go
tokens := credrefresh.New(func(ctx context.Context) (string, time.Time, error) {
	return mintInstallationToken(ctx)
})
token, err := tokens.Get(ctx)
```

## Running a script on a server

`helpers/hostexec` runs one shell script on a host and waits for it, without
streaming output or holding a connection. A `hostexec.Host` names the
transport in `Kind`, `hostexec.KindSSM` for an AWS Systems Manager
RunShellScript document or `hostexec.KindSSH` for an SSH connection, and the
`Target`, an EC2 instance id or `user@hostname`. `hostexec.Run` returns an
error for a failed script, and `hostexec.RunOutput` also returns its trimmed
standard output. A failed SSM command arrives as a `*hostexec.CommandFailed`
carrying its exit code and output.

An SSM script never carries a secret, because AWS records every command in
CloudTrail. An SSH host's `Credential` is its PEM private key, which
`hostexec` writes to a private file for the one call. `Region` has no default,
and a non-empty `Env` replaces the subprocess environment, as
`cloudid.Env` builds it. The options `WithComment`, `WithDeliveryTimeout`,
`WithExecutionTimeout` and `WithDetachedPoll` tune an SSM call and are refused
on SSH.

A block that runs scripts on a host another block created reads where to
reach it from the host's outputs. A host resource reports `execKind`, the
transport, and `execTarget`, its address for that transport, and the block
finds them in `Desired.Bindings` under its host requirement's name.
`hostexec.SSMHost(kind, target, credential, region, env)` builds an SSM host
from them. It accepts only the kind `aws-ssm` (`hostexec.KindSSM`) and refuses
any other with `hostexec.ErrNotSSM`, whose message names only the kind it
accepts and none of the host's values. A block that sets no credential passes
`""`:

```go
host := d.Bindings["host"]
h, err := hostexec.SSMHost(hostexec.Kind(host["execKind"]), host["execTarget"], "", region, cloudid.Env(creds))
if err != nil {
	return err
}
out, err := hostexec.RunOutput(ctx, h, "docker ps --format '{{.Names}}'")
```

Here `region` is the subscription's AWS region and `creds` the credentials
`cloudid` resolved, both your block's own.

## Setting up a Docker host

`helpers/dockerhost` holds the setup of a host that runs apps as Docker
containers, so every block that builds such a host runs the same one. The host
gets Ubuntu's `docker.io` package and nothing else of Docker's. Its apps join
the Docker network `appnet`, and a `caddy:2` container in front of them
publishes ports 80 and 443. The package renders text only, and your block
writes it to the host, through cloud-init or over SSH.

The package gives you these files and values, where `edge` is your block's
`dockerhost.Edge`, described below:

| Name | What it is |
|---|---|
| `dockerhost.SSHLockdown()` | An sshd drop-in that turns off password, keyboard-interactive and root login, named to sort before cloud-init's own drop-in |
| `dockerhost.Setup()` | The setup script at `/usr/local/sbin/mitstack-host-setup`, which installs Docker, creates `appnet` and starts the edge |
| `edge.Setup()` | The same script, which also mounts the edge's `RouteDir` into the edge. Use it in place of `dockerhost.Setup()` whenever the edge sets `RouteDir` |
| `edge.Caddyfile()` | The edge's config at `/opt/app/Caddyfile`, which the setup script mounts into the edge |
| `edge.Site(route)` | The text of one route's site, with the edge's compression, to write as a `.caddy` file in `RouteDir` |
| `dockerhost.Package` | The one Ubuntu package to install, `docker.io` |
| `dockerhost.EdgeRunning` | A shell test that passes only while the edge container runs, for a readiness check |

Each file is a `dockerhost.File` with a `Path`, an octal `Mode` and the
`Content`. The setup script tries the Docker install and the edge start up to
ten times each, ten seconds apart, so one failed package download at boot
does not leave the host without them. It exits 1 when either never succeeds.

A `dockerhost.Edge` names what the edge serves. Each `dockerhost.Route` sends
one hostname to an upstream, a container on `appnet` written as `name:port`,
and caddy gets that hostname a publicly trusted certificate. `PublicIP` is
served too, on caddy's internal certificate authority and proxied to
`IPUpstream`, and plain HTTP is redirected to HTTPS. With no `IPUpstream`,
the IP answers 204 with no body.

Two options are off by default, so an edge that sets neither renders what it
always did:

| Option | What it does |
|---|---|
| `Compress` | Writes `encode zstd gzip` in every site, so caddy compresses each response the client accepts compressed |
| `RouteDir` | A directory on the host, such as `/opt/app/routes`, whose `.caddy` files the Caddyfile imports as more sites. `edge.Setup()` mounts it read-only, so a route added after boot needs a file in it and a caddy reload, not a new Caddyfile |

`edge.Site(route)` returns the text of one site, such as
`shop.example.com {\n  encode zstd gzip\n  reverse_proxy shop-app:8080\n}\n`
with compression on. Write it to `RouteDir` as `<app>.caddy`, then run
`docker exec caddy caddy reload --config /etc/caddy/Caddyfile`. `Site` refuses
a route that `Caddyfile` would refuse.
`Caddyfile` refuses these values, since each could break out of the config:

- an address that is not IPv4, an IPv4-mapped IPv6 address included
- a hostname of one label, a hostname that is an IP address, or one with a
  character other than a lowercase letter, a digit, a dot or a hyphen
- an upstream with no port, or a port outside 1 to 65535
- a hostname routed twice
- a `RouteDir` that is not an absolute path of plain names

```go
edge := dockerhost.Edge{
	PublicIP:   "203.0.113.10",
	IPUpstream: "shop-app:8080",
	Routes: []dockerhost.Route{
		{Hostname: "shop.apps.mitstack.dev", Upstream: "shop-app:8080"},
		{Hostname: "blog.example.org", Upstream: "blog-app:3000"},
	},
	Compress: true,
	RouteDir: "/opt/app/routes",
}
caddyfile, err := edge.Caddyfile()
if err != nil {
	return err
}
```

### Sizing an app against what the host holds

Every app on a Docker host reserves CPU and memory, and an app that keeps data
of its own also reserves disk, so a host takes no more apps than it can run. A
per-app block is one an app subscribes to on its own, such as
docker-app-slice, the block that lands an app's container on the host. The host repository is the private Git repository the host's own block,
such as hcloud-docker-host, creates for the host. The host's pull agent, a
container on the host, starts each of its top-level directories as one app.

Each per-app block records its app's reservation in that repository at
`<app>/reservations/<block>.json`, where `<app>` is the app's directory and
`<block>` the block's id. `cpuMillis` counts thousandths of a CPU core and
`memoryBytes` counts bytes, so this record reserves half a core and 512 MiB:

```json
{"cpuMillis": 500, "memoryBytes": 536870912}
```

A block whose app keeps data, such as a database, adds `diskBytes`, the bytes
its volume may fill. A record without it reserves no disk, so this one also
reserves 10 GiB:

```json
{"cpuMillis": 250, "memoryBytes": 268435456, "diskBytes": 10737418240}
```

`dockerhost.SumReservations(files, except)` takes the repository's files as a
`map[string][]byte` of each path, relative to the repository's root, to its
content. Your block reads them through its GitHub client, for example by
listing the tree at the head of `main` and reading each path that ends in
`.json` under a `reservations` directory. The function adds up every record,
whichever block wrote it, into one `dockerhost.Reservation`, whose fields are
`CPUMillis`, `MemoryBytes` and `DiskBytes`. It leaves out the path `except`, so
a block counts only what the other blocks hold. A top-level directory whose
name starts with a dot holds no app, so its files never count.

The function returns an error naming the record's path when a record does not
parse, holds more than one JSON value, lacks its CPU or memory amount, holds a
field a `dockerhost.Reservation` lacks, reserves a negative amount or takes the
sum past what an `int64` holds.

The host's allocatable capacity is what the host can give its apps once it
has kept back what its system needs. The host sends it in its node report, the
signed status a host posts to mitstack every few minutes, which [Signing a node
report](#signing-a-node-report) describes. It sits in the report's body under
`host.capacity.allocatable`, in the same three keys, as hcloud-docker-host's
host sends them:

```json
{"host": {"capacity": {"allocatable": {
  "cpuMillis": 3500, "memoryBytes": 7516192768, "diskBytes": 150323855360}}}}
```

`dockerhost.NewestReport` reads the host's newest report and returns it as a
`dockerhost.Report`, whose `Allocatable` field holds the report's allocatable
capacity as a `dockerhost.Reservation`, or `nil` when the report gives none.
[Reading what the host last synced](#reading-what-the-host-last-synced) covers
its arguments and the rest of what it returns. Your block sums the other blocks' records and checks that its own
reservation fits in what is left before it writes anything. When it does
not fit, the block's subscribe returns a `handler.Refuse` error naming the
free and the requested amounts, so the platform shows the refusal and nothing
lands on the host.

Five functions keep every block's sizes the same as the console's, which
reads the same inputs on its subscribe page:

| Function | What it does |
|---|---|
| `dockerhost.ParseCPU(v)` | Reads vCPUs to three decimals, such as `0.5`, or millicores, such as `500m`, into millicores |
| `dockerhost.ParseSize(v)` | Reads a whole number of `Mi` or `Gi`, such as `512Mi` or `10Gi`, into bytes, for memory and disk alike |
| `dockerhost.Free(allocatable, others)` | Takes what the others hold from what the host can give, an amount below zero when they overfill it |
| `want.Fits(free)` | Holds each amount of `want` within `free`. A reservation of no disk fits whatever disk is free |
| `dockerhost.Describe(r)` | Writes the amounts as the console does, such as `0.5 vCPU, 512 MiB and 10 GiB of disk`, rounded down, with the disk only when `r` holds some |

Each parse function returns an error saying what to give, which your block
puts in its refusal of the input:

```go
others, err := dockerhost.SumReservations(files, app+"/reservations/docker-app-slice.json")
if err != nil {
	return nil, err
}
free := dockerhost.Free(allocatable, others)
if !want.Fits(free) {
	return nil, handler.Refuse("host-full", "the host has %s free, and the app asks for %s",
		dockerhost.Describe(free), dockerhost.Describe(want))
}
```

### Reading what the host last synced

The host's pull agent fetches the host's repository each minute and starts the
apps it holds, which this page calls a sync. A per-app block's observe answers
`contract.Present`, the app in place, only once the agent has synced a commit
that holds the block's files, and `contract.Unknown` while it cannot tell.
Every block on the host reads that commit the same way, through
`dockerhost.NewestReport`, so a deploy never reads differently from one block
to the next.

`dockerhost.NewestReport(ctx, api, d, hostSubscription)` takes the
`contract.Desired` your observe received as `d`, and as `hostSubscription` the
`subscriptionId` that `d.Bindings` gives under your block's host requirement.
`api` is `observe.NodeReportsClient{}`, which [Observing a resource the
platform cannot reach](observe.md#observing-a-resource-the-platform-cannot-reach)
describes, or a fake of `observe.NodeReportsAPI` in your tests. It returns
three values: the `dockerhost.Report`, a `found` flag that is `false` while
the host has sent no report, and an error. An empty `hostSubscription` is
refused through `handler.Refuse` with the class `host-binding`, so the
platform shows the subscriber why. A platform that does not answer or a
report that is not JSON returns a plain error. The report gives you three
things:

| Field or method | What it gives |
|---|---|
| `report.Allocatable` | The host's allocatable capacity, or `nil` when the report gives none |
| `report.SyncedCommit()` | The commit the agent last synced, from the report's `host.agent.syncedCommit` |
| `report.Decode(&v)` | The report's whole body decoded into your own struct, for fields only your block reads, such as each app's containers |

`SyncedCommit` never reads the agent's log. The report carries only the log's
last 20 lines, and a deploy's image pulls can fill them all. When the report
cannot name a commit, `SyncedCommit` returns an error, and your block answers
`contract.Unknown` with that error as its status detail.

The field is absent when the host runs a version of the hcloud-docker-host
block older than `dockerhost.SyncedCommitSince`, 0.8.0. The error then says
to upgrade the host's subscription to 0.8.0 or later. The field is `null`
from the moment the agent restarts on a new version until its first sync,
and the error says the next sync names the commit.

```go
report, found, err := dockerhost.NewestReport(ctx, observe.NodeReportsClient{}, d, hostSubscription)
if err != nil {
	return contract.Observation{}, err
}
if !found {
	return contract.Observation{State: contract.Unknown, StatusDetail: "the host has sent no node report yet"}, nil
}
commit, err := report.SyncedCommit()
if err != nil {
	return contract.Observation{State: contract.Unknown, StatusDetail: err.Error()}, nil
}
```

## Reporting whether an app answers

`helpers/reach` judges whether an app is serving at its URL and records the
verdict as the output keys every provider reports: `appUrl`, `reachable`,
`certTrusted`, `appHostname` and `publicIp`. A `reach.Checker` probes the
`appUrl` in a resource's outputs, and when the hostname does not resolve yet it
probes the host's `publicIp` instead. `Checker.Evaluate(ctx, outputs)` returns
a `reach.Result`, and `reach.Apply(outputs, result)` copies the outputs with
the verdict written in. Outputs without an `appUrl` count as reachable, so a
block that serves no web app is never held back.

The probe dials only a host under `apps.mitstack.dev`, a custom domain
`reach.VerifiedCustomDomain` confirmed, or a public IP address, so a request
cannot point it at a private address or the cloud's metadata endpoint. A
status below 500 counts as serving, and so does a TLS failure on certificate
trust alone, since it proves the app's edge is up.

## Answering the health check

The platform's edge and a cluster's pod probe ask each operator for `/healthz`,
and an operator that answers at all is up. `helpers/healthz` holds the one
handler every operator mounts, so no operator writes its own:

```go
mux.Handle(healthz.Path, healthz.Handler())
```

The handler answers 200 with the body `ok`. It is the Go module
`github.com/mitstack-eu/block-kit/helpers/healthz`, released by its own tags
such as `helpers/healthz/v0.1.0`. A block requires it beside whatever kit
release it builds on, so taking it moves no other pin.

## Signing a node report

This section is for the author of a block whose machines report to mitstack,
such as a Kubernetes cluster block or a block that runs one Docker server.
A node is one machine of that cluster or that server. A node report is the
JSON body a node posts to mitstack's `/v1/cluster-reports` on a schedule,
saying what it finds, such as its nodes and the objects labelled with a
subscription's id. The node sends it with its report token, a secret the block
writes onto the node for that one subscription.

A report token alone proves only that the sender holds the token. Whoever
holds a leaked one could post a report claiming a subscription's objects are
gone. A report proof ties each report to a key that only the node's own side
holds, so mitstack can tell the node's report from a forged one.
`helpers/reportproof` holds the signing and the check, so every block and
mitstack share one implementation.

### What a proof is

A proof is a JSON Web Token, or JWT: three base64url parts joined by dots, a
header, the claims and a signature. The header names the signing algorithm as
`alg`, either `RS256` for an RSA key or `EdDSA` for an Ed25519 key, and the
key's id as `kid`. The claims hold:

| Claim | What it holds |
|---|---|
| `aud` | The audiences the token is for: mitstack's origin, such as `https://api.mitstack.dev`, and `reportproof.Audience(body)`, which is `report-sha256:` followed by the lowercase hex SHA-256 of the exact bytes posted |
| `exp` | When it expires, ten minutes after it is made |
| `nbf` | When it starts to hold |

The node sends the proof in the `Mitstack-Report-Proof` header, named by
`reportproof.Header`. The public keys a proof is checked against travel in
the report's `jwks` member, a JSON Web Key Set or JWKS: an object whose `keys`
array holds one public key per entry, each named by its `kid`.

### What the block does and what the node does

Your block puts the signing onto its nodes, and each node then signs every
report it posts. A node signs in one of two ways:

| Node | What your block gives it | What the node does for each report | The `jwks` member it reports |
|---|---|---|---|
| A Kubernetes node | `reportproof.TokenRequestScript`, a Go string holding the script `scripts/token-request.sh`, written into the node's job, for example as an environment variable it runs with `bash -c`. Also a Role that grants the report's service account `create` on `serviceaccounts/token` for its own name alone | It runs the script, which asks the Kubernetes API server to mint a token through the TokenRequest API. The API server signs it with its own service-account key, so the node holds no private key | The API server's key set, read from `/openid/v1/jwks` |
| A server with no Kubernetes that runs Go, such as an agent your block builds | An Ed25519 key it generates on the server and keeps there, with a `kid` you choose, such as the server's name | It calls `reportproof.Sign(key, kid, body, "https://api.mitstack.dev", time.Now())`, which returns the proof as a string | `reportproof.KeySet(kid, publicKey)` |
| A server with no Kubernetes and no Go, such as a Docker host built from cloud-init | `reportproof.SignScript`, the script `scripts/sign.sh`, and a key the server generates with `openssl genpkey -algorithm ed25519` | It runs the script's `proof` mode, which prints byte for byte the proof `Sign` makes for that key and time | What the script's `keyset` mode prints, byte for byte what `KeySet` makes |

The script takes the body file, the report URL, the namespace and the
service account, in that order, and prints the token on its standard output:

```bash
bash token-request.sh /tmp/body.json https://api.mitstack.dev/v1/cluster-reports mitstack-node mitstack-node-report
```

That runs `kubectl create token mitstack-node-report -n mitstack-node
--audience https://api.mitstack.dev --audience report-sha256:<digest>
--duration 10m`. The script refuses a report URL that is not https.

The sign script needs only bash and openssl, which Ubuntu's base image holds.
It takes the key file and the `kid`, and for a proof also the platform's
origin, the body file and the Unix time to mint the proof at:

```bash
bash sign.sh keyset /var/lib/mitstack/report.key host-1
bash sign.sh proof /var/lib/mitstack/report.key host-1 https://api.mitstack.dev /tmp/body.json "$(date +%s)"
```

`keyset` prints the JSON the report carries as its `jwks` member, and
`proof` prints the value of the report's `Mitstack-Report-Proof` header. It
refuses a `kid` of anything but letters, digits, dots, underscores and
hyphens, an origin that is not https, and a time that is not a whole number
written without leading zeros, since each is written into the proof's JSON. It
also refuses a key file that holds no Ed25519 key.

### How a proof is checked

Your block does not call `Verify`. It is the check the receiving side runs:
`reportproof.Verify(proof, body, keySet, origin, now)` answers nil when every
condition below holds, and otherwise an error naming the first that fails,
such as `the proof was minted for another body`:

1. The key set parses, and no `kid` in it is empty or repeated.
2. The header's `kid` names a key of the set, and the header's `alg` is the
   one that key's type signs with, so an RSA key accepts only `RS256` and an
   Ed25519 key only `EdDSA`.
3. That key verifies the signature.
4. `aud` holds both the origin and `reportproof.Audience(body)`.
5. `exp` is present and not past, `nbf` if present has come, and `exp` is no
   more than ten minutes away. Each time allows `reportproof.Leeway`, one
   minute, for clocks that drift.


## Getting AWS credentials from the request

A request for an org with a connected AWS account carries a cloud identity:
the role to assume and a short-lived web-identity token.
`capabilities/aws/cloudid` turns it into temporary AWS credentials, with no
stored key. `cloudid.Resolve(ctx, id)` assumes the role through the `aws`
CLI, so the operator's image needs it, and passes the token by file so it
never reaches a command line. `cloudid.Env(creds)` returns an environment for
a subprocess, and a `cloudid.Refresher` holds the credentials across calls,
failing closed as `credrefresh` does. `Identity.Region` is required, because a
default region that shifts would strand what already runs in the real one.

## Pointing an app's hostname at its host

An app's hostname, such as `shop.apps.mitstack.dev`, lives in mitstack's own
apps zone in Route 53, in the platform's AWS account rather than the org's.
`capabilities/aws/apphostname` writes it with the operator's own credentials
through the `aws` CLI, so the operator's image needs the CLI and the operator
runs where the platform's instance role may change records in that zone.

Beside the A record it writes a TXT record, `mitstack-subscription=<id>`,
naming the one subscription that holds the hostname, so a second subscription
for the same name is refused rather than moving the first app's visitors.

1. `apphostname.ZoneID(domain)` reads `MITSTACK_APPS_ZONE` and
   `MITSTACK_APPS_ZONE_ID`. It refuses with `apps-zone-unset` when either is
   empty, and with `apps-zone-mismatch` when the zone is not `domain`, the zone
   your block's hostnames live in.
2. `apphostname.NewRecord(zoneID, name, addresses...)` is the record you want.
3. `apphostname.ReadClaim(ctx, w, rec, subscriptionID)` reads the hostname
   before your block writes anything else. It refuses with `hostname-taken`
   when another subscription holds the name, or when an A record that names
   no subscription points somewhere else, and with `no-address` when the
   record has no address.
4. `claim.Apply(ctx)` writes the changes in one batch once your block's own
   writes have landed. A batch Route 53 rejects because a racing call changed
   the records is read again, and passes when the records are now this
   subscription's.
5. `apphostname.Release(ctx, w, rec, subscriptionID)` deletes only what the
   subscription holds, and counts records already gone as deleted.
6. `apphostname.Observe(ctx, w, rec, subscriptionID)` reads the hostname present,
   with the `appHostname`, `address` and `appUrl` outputs of
   `mitstack.app-hostname`, while it points at the record's addresses.

`w` is `apphostname.Route53{}` in the operator. Record the resource's identity
with `apphostname.Identity(rec)`, and read it back on a later call with
`apphostname.FromIdentity`. Your tests pass an `apphostnametest.Zone` instead
of Route 53, which applies a batch whole or not at all, as Route 53 does, and
whose `HeldBy(subscriptionID)` names what a subscription left in the zone.

## Calling the GitHub API

`capabilities/github/ghapi` is a GitHub REST client whose host the operator
decides. `ghapi.Base(endpoint, extraHosts...)` resolves the API host a request
supplies against an allowlist of `api.github.com` and the hosts you add, and
refuses a private, loopback, bare-IP or metadata host even when you listed it.
`ghapi.BaseFromEnv(endpoint)` adds the hosts in `MITSTACK_GITHUB_API_HOSTS`.
`ghapi.NewClient(base.String(), token, httpClient)` then creates repositories,
commits files and sets Actions variables, each idempotently.

`EnsureRepo(ctx, org, name, appID)` creates a private repository, or finds one,
and keeps a ruleset on its main branch. The ruleset refuses every update,
force-push and deletion of main, except those of the GitHub App that `appID`
names. Pass the request's `secrets.github.appId`, which is the id of the
mitstack App whose installation minted the token, so the block's own commits
still land. Each call repairs the ruleset: it creates a missing one, puts back
one whose rules, branch or bypass list changed, and leaves a matching one
alone. An empty `appID` fails with `ghapi.ErrNoAppID` before any request. An
org owner can still delete the ruleset, and the next call restores it.

A block that reads a repository at one commit takes main's head with
`HeadCommit`, the commit's tree with `CommitTree`, and the path of every file
in that tree with `TreePaths`, which refuses a listing GitHub truncated past
100,000 entries. `ReadFileAt` then reads one file at that commit, and
`CommitChanges` commits onto it, wrapping `ghapi.ErrNotFastForward` when main
has moved on since.

A block that lands its commit on a ref of its own, such as a pull request's
branch, reads that branch's head with `BranchHead`, which answers `""` for a
branch that does not exist yet. It then builds the commit with `BuildCommit`,
which makes the tree and the commit on the parent you name and moves no ref.
Its `BuiltCommit` answer carries the commit and tree SHAs, and `Unchanged`
reports that the changes left the parent's tree as it was, so no commit was
made.

Every call reads GitHub's answer whole and refuses one longer than its bound,
naming the call and the bound, so a cut-off copy never decodes into an empty
SHA. An answer over 8 KiB is ordinary: a create-tree answer lists each root
entry, and a commit answer carries the whole message. The bounds sit in
`revision.go`.

blockverify holds a block that calls the GitHub API to this rule: it must
resolve the host through `ghapi.Base`, `ghapi.BaseFromEnv` or an allowlist of
its own, as [Running the block checks](block-checks.md) describes.

## Removing the files a block landed

`capabilities/github/landing` serves a block that writes files into an app's
GitHub repository, such as a starter or a workflow file. It keeps the rule of
the platform's
[ADR 0107](https://github.com/mitstack-eu/mitstack/blob/main/docs/adr/0107-unsubscribe-removes-every-file-a-block-landed-that-main-still-carries-unchanged.md):
at unsubscribe the block removes every file it wrote that the app has not
changed since, and keeps the rest. Here main is the repository's `main`
branch, and a landing is one commit of the block's files onto it.

`landing.Land(ctx, client, l)` takes a `*ghapi.Client` and a `landing.Landing`
of `Repository`, `Message`, `Files` (each path and its content) and `Prior`.
It commits the files straight onto main in one commit and returns a
`landing.Record`. For each path the record holds the git blob hash of what was
written and `created`, which is true only when main lacked the path before the
block's first landing. Pass the record of an earlier landing as `Prior`, so a
path the block created stays recorded created. `Record.Identity()` turns the
record into the identity of the resource that landed the files, with the files
as one JSON list:

```json
{"repository": "acme/weather",
 "files": "[{\"path\":\"Dockerfile\",\"blob\":\"c901703072104919a085c3bba881ebf634c4728c\",\"created\":true}]"}
```

The platform stores that identity and sends it back at unsubscribe, where
`landing.RecordFromIdentity` reads it. A file is unchanged when main carries
it at the recorded blob hash, and changed when main carries it with any other
content. `landing.Remove(ctx, client, landing.Removal{Subscription:
d.SubscriptionID, Records: records})` reads main and, per repository, opens
one pull request from the branch `mitstack/<subscription>/remove`. That pull
request deletes each unchanged file recorded `created`, and its description
names the changed files it keeps. The result's `Kept` lists those, each with
the reason `changed`, as `contract.Kept` values. Your block's `DeleteKeeping`
returns them under the resource's name, so the unsubscribe answer reports
them, as [Serving lifecycle calls](handler.md#what-the-handler-calls-for-each-call)
describes. A path main held before the landing is never removed.

The pull request merges on the checks main requires, by branch protection or
by a ruleset, and never past them. Until each required check reports success
on it, `Remove` returns the `*handler.Waiting` that `handler.WaitOnRemoval`
builds, and while GitHub refuses the merge, the one `handler.Wait` builds.
Returned from `Delete`, either makes the unsubscribe answer `absent:false`,
and the platform sends the unsubscribe again, as
[Serving lifecycle calls](handler.md#what-the-handler-calls-for-each-call)
describes. A later call reuses the branch and pull request while main has not
moved, and rebuilds the removal onto main's new head when it has. Once the
merge lands, `Remove` deletes every branch whose name starts with
`landing.BranchPrefix(subscription)`, `mitstack/<subscription>/`. A block that
also opens pull requests of its own names their branches under that prefix,
so the removal clears them too.

The removal's own commit and the squash the merge puts on main both end with
the trailer `Mitstack-Subscription: <subscription>`. The merge passes its own
`commit_title` and `commit_message`, so the repository's squash setting cannot
drop the trailer. mitstack reads it to start no build from the merge, as the
README's section
[How mitstack reads a teardown's commits](../README.md#how-mitstack-reads-a-teardowns-commits)
describes.

`landing.Observer` reads such a resource from main. It is present while main
carries at least one file recorded `created` at its recorded blob, and absent
once main carries none. It also reads a repository proven deleted absent:
the repository answers 404, and the installation the token belongs to lists
its repositories as `repository_selection: "all"`, which means every
repository of the owner, with one of the same owner. Any read that fails,
such as a 403, a 5xx, a rate limit or a missing token, reads unknown, and
`Remove` then waits with no write sent.

During an unsubscribe, a present answer also reads the subscription's open
removal pull request. While a check main requires is queued or running on it
and none has failed, the answer carries `Removing`, whose `WaitsOn` names the
pull request and that check, such as
`removal pull request #4 of acme/weather, whose required check "managed-ci" is
running`. The platform then waits on the removal without spending one of the
unsubscribe's attempts. A pending commit status, and a check run that GitHub
has queued but not started, count as queued. Once a required check fails, the
pull request closes unmerged, or no required check is queued or running, the
answer carries no `Removing`, and the attempts count again. A check that stays
queued or running keeps the removal reading under way for as long as it does.
A read of the pull request or its checks that fails reads unknown. Outside an
unsubscribe the observer reads no pull request.

A scaffold contribution is a resource of type `github.scaffold-contribution`.
Its block offers files for the app's repository through the resource's
`scaffoldContribution` output, a JSON object such as
`{"files": {"Dockerfile": "FROM nginx:alpine\n"}}`. The app-scaffold block
reads every such output and commits the files onto main in one commit of its
own, so the contributing block writes nothing to the repository itself.

The contributing block still records what it offered, so its unsubscribe can
remove the files. Its `EnsureResources` calls `landing.Offer(ctx, client, l)`
with the same `landing.Landing` that `Land` takes, leaving `Message` empty.
`Offer` reads main and returns the `landing.Record` that `Land` would, but
commits nothing. Set the landing's `Prior` to the record the resource's
identity already holds, so a path stays recorded created after app-scaffold
has committed it.

`landing.OfferObserver` observes a scaffold contribution. Before app-scaffold
commits the files, main carries none of them. The contribution must still read
present then, because the engine hands its `scaffoldContribution` output to
app-scaffold only once the contribution's subscription is active. So outside
an unsubscribe the observer reads it present while the repository can be read.
When the handler observes it after an unsubscribe's delete, where
`contract.ResourceRef.Unsubscribing` is true, the observer answers as
`landing.Observer` does. A record that lists no files reads absent then. A
repository proven deleted reads absent either way, and an unreadable one reads
unknown.

`capabilities/github/landing/landingtest` is a fake of the GitHub routes the
package calls, holding one repository's main, branches, pull requests and
checks. A block's own tests use it too: `Seed` and `Remove` change main as
another writer would, `Head` names its commit, and `BeforeWrite` runs a hook
ahead of each write. A hook that returns an error refuses that write, and one
that seeds main moves it under the block's commit, so the block's retry can be
tested. A refused write is not listed by `Writes`. The hook runs once per
request, in parallel when the client sends requests in parallel, so a hook
that keeps state guards it.


## Adopting a Hetzner dedicated server

`capabilities/hetzner/robot` calls Robot, Hetzner's webservice for dedicated
servers. `robot.New(user, password)` takes the Robot login as its two
arguments, which the block reads from its own connection, and stores it
nowhere else. The login reaches every
server of the Robot account, so a block's README names that reach as a limit.
The calls follow the shapes and timings Robot answered on a live trial,
recorded in the workspace note
[Talos on a Hetzner Robot server behind a vSwitch gateway](https://github.com/mitstack-eu/mitstack-workspace/blob/main/product/knowledge/2026-10-28-talos-on-a-hetzner-robot-server-behind-a-vswitch-gateway.md).

| Call | What it does |
|---|---|
| `Name(subscriptionID)` | Gives the name a block adopts a server under: `robot.NamePrefix` and the whole subscription id, lowered, with any character outside a-z and 0-9 written as a hyphen, such as `mitstack-sub-0123456789ab` |
| `Adopt(ctx, number, name)` | Names a ready server, whose name begins with `robot.NamePrefix`, and refuses one that is cancelled, absent, without a firewall or named by another subscription, before any write |
| `Release(ctx, number, name)` | Clears the rescue activation, the keys named after the server and its name, and admits all traffic, but only while the server carries `name` |
| `Couple(ctx, name, number)` | Finds or creates the vSwitch `name` on the lowest free VLAN and waits until Robot reads the server ready on it, which took under three minutes on the trial |
| `Uncouple(ctx, id, number)` and `CancelVSwitch(ctx, id)` | Takes the server off and waits until Robot stops listing it, which took 152 seconds, then cancels the vSwitch |
| `WriteFirewall(ctx, number, fw)` | Writes the firewall and waits until Robot reads it active, which took 62 to 131 seconds. It never sends Robot's two internal mail-port rules back, since Robot keeps them ahead of every write |
| `OpenRescueWindow(ctx, number, installer)` | Adds one rule admitting SSH from the installer's address alone and admits Hetzner's own services. The function it returns writes back the firewall it found |
| `NewRescueKey()` and `BootRescue(ctx, number, keyName, key)` | Mints a one-time ed25519 key, registers it and resets the server into a rescue system that admits that key alone |
| `LeaveRescue(ctx, session)` | Clears any activation Robot still holds and deletes the key, so the next reset boots the disk |

A rescue boot answers the host keys Robot names in `RescueSession.HostKeys`.
On the trial Robot named none, so a client cannot tell the rescue system from
an impersonator on the path to it. A block therefore sends that session no
secret, and its secrets reach the disk by a channel an on-path attacker cannot
impersonate.

`Installer.Install(ctx, target, handOff)` runs that whole install once, from
adoption to the reset that boots the written disk. It refuses a server outside
`robot.Locations`, fsn1, nbg1 and hel1, where a vSwitch reaches a Cloud
network. It then adopts the server, couples its vSwitch, writes the target's
firewall and boots the rescue system. Over the rescue session it runs the
block's own rescue script with `read`, which reports the hardware. `handOff`
then renders the short-lived Cloud server for that hardware, and the block's
`Cloud` creates it on the vSwitch. The script's `write` receives only the
VLAN, the addresses, the port, the pin of the server's TLS key and the disk's
serial, each matched against a pattern, then fetches the secret over the
vSwitch and checks the pin. Whether the hand-off succeeds or fails, the
install deletes the short-lived server and the rescue key, restores the
firewall and resets the server into its disk.

The kit's module holds no SSH client, since it takes no dependency beyond the
standard library. `Installer.Dial` opens the session, and the module
`github.com/mitstack-eu/block-kit/capabilities/hetzner/robot/rescuessh`
provides one: `rescuessh.Config{Script: script}.Dial` accepts only a host key
Robot named, and any key when Robot named none, and it runs nothing but the
script. `rescuesshtest.New(t, admit)` is a fake rescue system for a block's
tests, which runs each command with bash.

Robot's documentation at
[robot.hetzner.com/doc/webservice](https://robot.hetzner.com/doc/webservice/en.html)
limits each call per hour. It allows `GET /server/{number}` 200 times an hour,
and `GET /firewall/{number}`, `GET /vswitch` and `GET /vswitch/{id}` 500 times.
Past a limit Robot answers `403 RATE_LIMIT_EXCEEDED`, and after three failed
logins it blocks the calling address for ten minutes. While a subscription
settles, the platform calls the block again every few seconds, as
[Work that outlasts one call](handler.md#work-that-outlasts-one-call)
describes. The client therefore answers a repeat read from its last answer. A
read of a server, the vSwitch list, a vSwitch or a firewall answers the same
read for `robot.ReadWindow`, one minute, which keeps each at 60 reads an hour.
Every client of the same login in the process shares those answers, and no
other login reads them. A write drops the answers it changes.

`Adopt`, `Release`, `Couple`, `Uncouple`, `WriteFirewall` and
`OpenRescueWindow` read Robot afresh, so they never act on an earlier answer.
Each of them writes, so call it only when a write is due rather than on every
call the platform sends. A client's `ReadWindow` field set to zero sends every
read to Robot, and its `Now` field is the clock the window is judged by, which
a test sets to move time.

A `403 RATE_LIMIT_EXCEEDED` answer matches `errors.Is(err,
robot.ErrRateLimited)`, and no other answer does. It passes once Robot's
hourly allowance refills, so a block waits it out instead of failing, for
example by returning `handler.Wait` from `EnsureResources` or `Ensure`, as
[Errors, refusals and panics](handler.md#errors-refusals-and-panics)
describes. The client keeps that answer for the read window too, so it does not
ask Robot again in the meantime.

`robottest.New(t)` is a fake of the webservice for a block's tests. It lists
the internal rules ahead of the rules a write sent, reads a firewall `in
process` once after each write, clears a rescue activation once it boots it,
and lists a server joining or leaving a vSwitch as `in process`.
`fake.Client()` returns a client logged in to it that sends every read to the
fake, so a test that changes the fake reads the change at once.
`fake.Calls(method, path)` counts the requests sent, and
`fake.SetRateLimited(true)` answers every request `403 RATE_LIMIT_EXCEEDED`
until it is set false.

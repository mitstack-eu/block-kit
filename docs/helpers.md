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
| `helpers/bounded` | Calls an API with its own HTTP client and reads the answer |
| `helpers/guardedhttp` | Is a capability operator that calls its vendor with an org's credential |
| `helpers/events` | Reports progress during long work, or reads a platform route |
| `helpers/reqcache` | Wants a read repeated within one request to cost one call, for a source of its own that cannot change under the request |
| `helpers/credrefresh` | Holds a short-lived credential across calls |
| `helpers/lease` | Runs a program on its node that needs a short-lived credential of a resource the block requires or provides, such as a snapshot's upload link |
| `helpers/hostexec` | Runs a shell script on a server over AWS Systems Manager or SSH |
| `helpers/dockerhost` | Sets up a host that runs apps as Docker containers behind a caddy edge, sizes and observes an app on one, or runs an app's database on one |
| `helpers/reach` | Serves an app at a URL and reports whether it answers |
| `helpers/healthz` | Serves its health check, which every block does |
| `helpers/reportproof` | Has its nodes sign the reports they post to mitstack |
| `helpers/redundancy` | Removes or replaces one member of a redundant set, such as a gateway or an etcd member |
| `helpers/tools` | Copies a tool onto a machine or a runner while it runs, such as age onto a Docker host or k3s onto a node |
| `helpers/signin` | Builds the sign-in companion, a program a Docker host runs to open an app only to its org's members, as [Opening an app only to its org's members](#opening-an-app-only-to-its-orgs-members) describes. A block reads only its constants, such as `signin.ContainerName` |
| `helpers/age` | Encrypts or decrypts a file in the age v1 format with an X25519 key, so the public `age` tool opens what a block wrote |
| `helpers/continuity` | Keeps state that must survive a replacement of its server, such as a volume or an OpenBao store, and serves the `snapshot` and `restore` operations |
| `capabilities/aws/cloudid` | Creates its resources in the org's AWS account |
| `capabilities/aws/apphostname` | Points an app's hostname in mitstack's apps zone at the app's host |
| `capabilities/aws/adopt` | Retries a subscribe that may find its own AWS resources already made, judging each by the kit's subscription tag |
| `capabilities/aws/awsnames` | Names an app's account-unique AWS resources under the caller's prefix, such as its ECR repository and IAM roles |
| `capabilities/aws/handover` | Hands an app's data volume to a replacement host through an EBS snapshot marked under the caller's tag prefix |
| `helpers/tftpl` | Renders in Go an OpenTofu template its module also renders with `templatefile` |
| `capabilities/github/ghapi` | Calls the GitHub REST API, or reads the GitHub account or App id a request carries |
| `capabilities/github/landing` | Lands files in an app's repository, and must remove them at unsubscribe |
| `capabilities/hetzner/cloud` | Creates Hetzner Cloud servers whose public address DNS records name, reads a server's `serverType` input, or waits on the Cloud actions its calls start |
| `capabilities/hetzner/robot` | Adopts a Hetzner dedicated server through Hetzner's Robot webservice |

## Keeping secrets out of logs and requests out of hosts

`helpers/safe` holds two security rules an operator must keep. The handler
already scrubs the request's secrets from every answer and every log line it
writes, so you need this package only for what your own code logs or calls.

The first rule is that log sanitising and secret scrubbing are two separate jobs.
`safe.Scrub(s, secrets...)` replaces each secret of four or more characters
with `[REDACTED]`, in whatever order you pass them. Where one secret holds
another, or two overlap in the text, the whole stretch they cover reads as one
`[REDACTED]`, so no part of either stays readable beside it.
`safe.LogValue(s)` removes the line breaks and control
characters a crafted value could use to forge a log line.
`safe.LogSafe(s, secrets...)` does both, in that order:

```go
log.Printf("creating the firewall for %s", safe.LogSafe(d.SubscriptionID, d.Secrets["hetzner"]["api_token"]))
```

To scrub every secret the request carries, as the handler does, pass
`d.SecretValues().All()`. `contract.SecretValues` keeps the values apart by
where the request carries them: its connections' secrets, its secret inputs,
the app's secrets and the block's own secrets.

The second rule is that the operator, never the request, picks the host it
sends a token to. A request may carry an endpoint, such as a GitHub instance's
API address, and building a URL from it would let a forged request send the
token anywhere. `safe.TrustedBase(endpoint, allow)` compares the endpoint with
a `safe.Allowlist` you build from constants or operator configuration, and
returns a `safe.Base` built from the allowlist entry. It refuses an endpoint
that is not `https` or not on the list. `base.URL(path)` then joins a path onto
the trusted host.

## Calling a vendor with an org's credential

A capability operator that answers `verify-connection` holds a credential a
person has just typed, so a vendor host that resolves to the operator's own
network would be an attack. `guardedhttp.New` returns an `*http.Client` that
sends a request only when it is `https`, names a host in `Config.Hosts`, and
resolves to no loopback, private, link-local or unspecified address. The
client follows no redirect, since the checks cover only the first URL, and it
uses no proxy from the environment. It dials the address it checked rather
than the host name, so a second lookup cannot answer differently. Every
refusal wraps `guardedhttp.ErrRefused`.

```go
client, err := guardedhttp.New(guardedhttp.Config{Hosts: []string{"panel.sendcloud.sc"}})
```

`Config.Resolve`, `Config.Dial` and `Config.RootCAs` let a test stand a local
TLS server in place of the vendor. The address checks apply whatever they
return. blockverify's `guarded-client` suite requires an operator that answers
`verify-connection` to use this client alone.

## Reading a service's answer

`helpers/bounded` reads an answer in bounded memory and never hands your code a
cut copy, so a person debugging your block never hunts for a malformed answer
the service did not send. `bounded.Read(resp, call, limit)` returns the body.
It refuses a 2xx answer longer than `limit`, naming the call and the limit, and
cuts a refusal's text to `limit`, since the status already says what happened.

```go
raw, err := bounded.Read(resp, "GET /server/42", 1<<20)
// err: GET /server/42: the answer is longer than 1048576 bytes
```

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

`events.ReportCommit(ctx, repository, commit)` tells the platform that a commit
landed on a repository a cluster or a Docker host reads. The platform passes
the report down that cluster's or host's commit stream, so the cluster's Argo
CD or the host's agent pulls the repository at once instead of waiting for its
own timer. It posts one request to `POST /v1/gitops-commits` with the
callback's token, and makes no second request, even after a failure. The
repository is the GitHub `owner/name` the reader pulls:

```json
{"repository": "acme/docker-host-sub-host0123456789", "commit": "2d266ec0449adf437f9226b938817a45da1ed95c"}
```

It returns an error when the platform answers anything but 202, cannot be
reached, or the request carries no callback. Log that error and carry on with
the commit, never undoing or failing it, since the reader's own timer still
takes it. `hostRepo.commit` in `helpers/dockerhost` reports
each commit it lands on a host's repository this way.

## Reading each thing once in a request

The handler gives every request a cache, so a block's passes over the same
data cost one read each. A block that reads through `gitops.GitHubRepository`
and `observe.NodeReported` gets this with no code of its own.

| What is read | Kept for |
|---|---|
| A file of a commit, present or absent, and the files a commit changed | The request, since a commit never changes |
| The newest node report of a cluster | One `validate`, which judges one view of the reports |

Three reads are never kept. The repository's head is read on every call,
because a branch moves, so a read after your own commit sees the new head.
The node reports are read afresh in every other verb, so a `Wait` that polls
still sees a report that arrives during the call. A read that failed is not
remembered, so the next ask tries again. Nothing is shared between two
requests.

A block with a source of its own calls `reqcache.Immutable(ctx, key, read)` for
data that cannot change, and `reqcache.Viewed` for data that changes but may
be read once in a `validate`. Both read every time when the context carries no
cache.

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

## Asking the platform for a lease

A lease is one short-lived credential for a resource, such as a signed upload
link for a storage bucket. The resource's provider, the block whose
subscription created the resource and manages it, mints the lease for the
caller that asks.
[Leasing a short-lived credential](handler.md#leasing-a-short-lived-credential)
describes the provider's side. `helpers/lease` is the asking side. This
section is for a block author whose block runs a program on its node, the
machine that reports to mitstack, and that program needs such a credential.

The node asks with its report token, the secret it also sends its reports
with. The node receives that token once, in the platform's answer when it
enrols, and keeps it on its own disk: a Docker host in a file, a cluster in a
Kubernetes Secret. Only the node holds the token, so the program that asks for
a lease runs on the node. An operator holds no report token, since the platform keeps only a digest of it
and sends none in a lifecycle call.
[ADR 0124](https://github.com/mitstack-eu/mitstack/blob/main/docs/adr/0124-a-block-or-host-leases-a-short-lived-credential-from-a-resources-provider-through-one-platform-route-and-one-kit-operation.md)
holds the reasoning. The program hands the helper the platform's address and
the token:

```go
platform := lease.Platform{BaseURL: "https://api.mitstack.dev", Token: reportToken}
answer, err := platform.Ask(ctx, "scaleway.cockpit", map[string]string{"signals": "metrics,logs"})
```

`Ask` sends one `POST /v1/cluster-leases` with the token as its bearer and
this body:

```json
{"resource": "scaleway.cockpit", "want": {"signals": "metrics,logs"}}
```

`resource` is the reference your manifest uses for a resource the block
requires or provides: its exact type, or its name where two share a type.
`want` is nil or any value that encodes as a JSON object, which the platform
forwards to the provider unread. `answer.Credential` holds the provider's
credential as raw JSON for your code to decode, and `answer.ExpiresAt` its
expiry.

| The route answers | `Ask` returns |
|---|---|
| 200 with a credential that has not expired | The `lease.Answer` |
| 401, for an unknown token or a subscription being removed | A `*lease.Refusal` with `Status` 401 |
| 404, when the node's block neither requires nor provides the resource | A `*lease.Refusal` with `Status` 404 |
| 409, when the provider's block does not list `lease` | A `*lease.Refusal` with `Status` 409 |
| 502, when the provider refused or failed to mint | A `*lease.Refusal` with `Status` 502 and the route's fixed message |
| 503, when the provider is not ready, such as while its subscription is still being built, or when the platform has paused its engine | A `*lease.Refusal` with `Status` 503 and, in `RetryAfter`, the wait the answer names |
| Any other status, or a refusal whose body is not the route's own, as a proxy in front of the platform answers | A `*lease.Refusal` with that status and a fixed message |
| 200 with an expiry that has passed, or with no credential or expiry | An error naming what the answer lacks |

`Ask` sends one request and waits in no loop of its own. After a 503 your
program decides when to ask again, such as on its next scheduled run, and
`RetryAfter` is how long the platform asks it to wait. Read a refusal's fields
with `errors.As`. An error's text names the resource, the status and the
platform's message. It never holds the token, and a message that holds a URL
is withheld:

```text
lease of "scaleway.cockpit": the platform answered 503: lease is not available yet; retry later (Retry-After 30s)
```

`Platform.HTTPClient` is optional. Without one, `Ask` waits ten seconds at most
and follows no redirect, so the token reaches no other host. A client of your
own brings its own timeout.

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

### Opening an app only to its org's members

The sign-in companion is a small program that runs as one container on each
Docker host, beside the edge, the caddy container that takes the host's web
traffic. The edge asks it about each request to an app that opens only to its
org's members, through caddy's `forward_auth` directive, which lets a request
through only when another program answers it with a 2xx status. The companion
signs a visitor in through mitstack's OAuth routes, `GET /oauth/authorize`
and `POST /oauth/token`, the same ones an MCP client uses. It remembers each
member in a cookie only it can read, sealed with a key it keeps on the host.
It asks mitstack again whether the visitor is still a member at most five
minutes after it last asked. A removed member therefore loses the app within
five minutes. A person who is no member of the app's org sees mitstack's own
page saying so, with status 403, and the app never sees the request.

A `dockerhost.Route` turns this on with its `SignIn` field. With `SignIn`
unset, the route renders exactly what it did before. `dockerhost.SignIn` has
three fields:

| Field | What it holds |
|---|---|
| `AppID` | The app's mitstack id, such as `app_8d21b0c4`. The companion signs a visitor in under the OAuth client id `app:app_8d21b0c4`, which mitstack derives from the app itself, so no block registers a client |
| `SignInHostname` | The app's hostname under `apps.mitstack.dev`, such as `costs.apps.mitstack.dev`, where mitstack sends a member back after sign-in |
| `Hostnames` | Every hostname the app's route serves, the only ones a sign-in sends a visitor back to |

A docker-app-slice route for an app that also serves a custom domain renders
one site per hostname, each with the same `SignIn`:

```go
signIn := &dockerhost.SignIn{
	AppID:          "app_8d21b0c4",
	SignInHostname: "costs.apps.mitstack.dev",
	Hostnames:      []string{"costs.apps.mitstack.dev", "shop.example.com"},
}
edge := dockerhost.Edge{Compress: true}
var route strings.Builder
for _, name := range signIn.Hostnames {
	site, err := edge.Site(dockerhost.Route{Hostname: name, Upstream: "costs-app:8080", SignIn: signIn})
	if err != nil {
		return err
	}
	route.WriteString(site)
}
```

A visitor who opens the custom domain signs in on `SignInHostname`, since
mitstack sends members back only to a hostname under `apps.mitstack.dev`. The
companion then hands the session over to the custom domain with a link that
works once, within 60 seconds, and only in the browser that started the
sign-in.

Each route's site does three things, in order:

1. It deletes the five member headers, `X-Mitstack-User`, `X-Mitstack-Email`,
   `X-Mitstack-Name`, `X-Mitstack-Org` and `X-Mitstack-Role`, from every
   request a visitor sends.
2. It sends every path under `/.mitstack/` to the companion, which answers
   mitstack's return after sign-in, the custom domain's handoff and sign-out
   there. Sign-out ends the session only on a `POST` from the app's own page,
   and only on that hostname. A custom domain's sign-out leaves the session on
   `SignInHostname`, so the visitor's next visit there signs them back in.
3. It asks the companion about every other request through caddy's
   `forward_auth`, and passes it to the app with the five headers only on a
   2xx answer.

`Site` refuses an app id that is not 1 to 64 letters, digits, `-` or `_`, a
hostname in `Hostnames` that a route could not hold, and a `Hostnames` that
leaves out the route's own hostname or `SignInHostname`.

The companion's code is the package `helpers/signin`. Its program,
`cmd/mitstack-signin`, is built from that package into the kit's tools image,
at `tools.SignInPath`, with the CA bundle it trusts beside it. The
hcloud-docker-host block runs it from that image as a container named
`signin.ContainerName` on `appnet`. A volume at `/data` keeps its key and the
sessions signed out across restarts. It holds no mitstack credential. Each
call it makes carries the token mitstack issued for that one member and app,
which opens only `GET /oauth/userinfo`.

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
`dockerhost.Report`. The kit reads the capacity strictly: all three keys must
be present, and each must hold a whole number of zero or more. A capacity
that passes is a `dockerhost.Allocatable`, which the report's `Allocatable`
method returns. One that fails counts as none, and the report's
`CapacityError` method names the key, as in
`host.capacity.allocatable lacks memoryBytes`.
[Reading what the host last synced](#reading-what-the-host-last-synced) covers
its arguments and the rest of what it returns.

Your block checks that its own reservation fits before it writes anything, by
calling `dockerhost.CheckRoom(report, found, ask, others)`. The `report` and
`found` arguments are what `dockerhost.NewestReport` returned, and `others` is
what `dockerhost.SumReservations` added up. The `ask` argument is a
`dockerhost.Ask` with three fields. `Part` names what is placed, such as
`shop` or `shop's database`. `Kind` names what the developer should give less
of, such as `app` or `database`. `Size` is the reservation it asks for.

`CheckRoom` answers one of five ways, so every per-app block treats a host the
same:

| The host | `CheckRoom` returns |
|---|---|
| Has sent no node report, or a report without allocatable capacity | A `handler.WaitForSignal` wait naming the missing capacity report, so the engine sends the subscribe again once the host reports |
| Sent a report whose capacity carries an `error` in place of `allocatable` | The same wait, quoting that error |
| Sent a report whose `allocatable` lacks a key, or gives one below zero or as anything but a whole number | The same wait, quoting the key, so a missing measure never reads as a full host |
| Is read absent by the platform, such as a server deleted outside mitstack | A `host-absent` refusal |
| Has too little free beyond what the others reserve | A `host-full` refusal naming the free and the requested amounts |

Otherwise it returns `nil` and your block writes its files. A refusal shows
in the console and nothing lands on the host. A wait writes nothing either,
and [Errors, refusals and panics](handler.md#errors-refusals-and-panics) says
how the engine answers it. `dockerhost.Capacity(report, found, part)` gives
the allocatable capacity alone, with the same wait and `host-absent` refusal,
for a block that checks something else between reading it and sizing.

Five more functions keep every block's sizes the same as the console's, which
reads the same inputs on its subscribe page:

| Function | What it does |
|---|---|
| `dockerhost.ParseCPU(v)` | Reads vCPUs to three decimals, such as `0.5`, or millicores, such as `500m`, into millicores |
| `dockerhost.ParseSize(v)` | Reads a whole number of `Mi` or `Gi`, such as `512Mi` or `10Gi`, into bytes, for memory and disk alike |
| `dockerhost.Free(allocatable, others)` | Takes what the others hold from what the host can give, an amount below zero when they overfill it |
| `want.Fits(free)` | Holds each amount of `want` within `free`. A reservation of no disk fits whatever disk is free |
| `dockerhost.Describe(r)` | Writes the amounts as the console does, such as `0.5 vCPU, 512 MiB and 10 GiB of disk`, rounded down, with the disk only when `r` holds some |

Each parse function returns an error saying what to give, which your block
puts in its refusal of the input. A block that lands an app named `shop`
checks its room like this:

```go
others, err := dockerhost.SumReservations(files, "shop/reservations/docker-app-slice.json")
if err != nil {
	return nil, err
}
ask := dockerhost.Ask{Part: "shop", Kind: "app", Size: want}
if err := dockerhost.CheckRoom(report, found, ask, others); err != nil {
	return nil, err
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
report that is not JSON returns a plain error. A reader that already holds a
report's body, such as the platform, reads it with
`dockerhost.ReadReport(body)` instead, which fails only on a body that is not
JSON. Each method below decodes its own part of the report, so a capacity or
an agent the kit cannot read never stops your block reading the report's logs
or releases. The report gives you eight things:

| Method | What it gives |
|---|---|
| `report.Allocatable()` | The host's allocatable capacity as a `dockerhost.Allocatable`, and `false` when the report gives none or one the kit cannot read whole |
| `report.CapacityError()` | The error the host sent under `host.capacity.error` in place of its allocatable capacity. hcloud-docker-host sends `nproc, free -b or df -B1 / printed nothing the report can read`, which names the three commands it reads its CPU, memory and disk with. When the report gives a capacity the kit cannot read whole, the method names the key that is missing, below zero or no whole number instead. The method returns an empty string when the report gives its whole capacity or names no error. `Capacity` and `CheckRoom` read the error through this method, so a page that shows a host's room, such as the platform's Room for apps, can quote the error a block waits on |
| `report.SyncedCommit()` | The commit the agent last synced, from the report's `host.agent.syncedCommit` |
| `report.AppRelease(app, subscription)` | The app's newest release command as a `dockerhost.AppRelease`, described below |
| `report.ReleaseAgainst(app, subscription, asked)` | The same release command judged against the image the app's newest deploy asked for, described below |
| `report.AppContainers(app)` | The containers the report lists under the app, each a `dockerhost.Container` that judges its own health, described below |
| `report.AppHealth(app, subscription, causes)` | The health of the app's own containers as one answer, described below |
| `report.Decode(&v)` | The report's whole body decoded into your own struct, for fields only your block reads, such as the host's volumes |

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

An app's release command is a command, such as `php artisan migrate --force`,
that its block writes for the host to run once on each new image of the app,
before that image replaces the app's container. A command that fails leaves the
app on the image it ran, and the host keeps the failed run in its report until
the next release command runs. The report's `releases` entry holds each app's
newest run, and its `apps` entry each app's containers. The host labels each
container `mitstack.dev/subscription` with the subscription of the block whose
compose service made it.

`report.AppRelease(app, subscription)` reads `app`'s newest run, and the image
of the app's running container whose label is `subscription`. Pass your own
block's subscription, `d.SubscriptionID`, not the host's. It returns a
`dockerhost.AppRelease`:

| Field or method | What it gives |
|---|---|
| `Failed` | `true` while the app's newest release command exited non-zero |
| `FailedDigest` | The digest of the image that command failed on, empty while it passed |
| `ServingDigest` | The digest of the image the app's running container serves, empty when none runs |
| `Serves()` | Whether any image of the app serves, `false` after a failed first deploy |

An app with no release command, or one the report names no release of, reads
as not failed. While `Failed` holds, your block still answers
`contract.Present`, since its files are in place, and sets `Unhealthy` on the
observation, so the platform shows that cause and detail until a release
passes. The cause must appear, word for word, under failures in the
platform's catalogue entry for your resource:

```go
const releaseFailed = "the app's release command failed, so the host serves the previous image or none"

release, err := report.AppRelease(app, d.SubscriptionID)
if err != nil {
	return contract.Observation{}, err
}
obs := contract.Observation{State: contract.Present}
if release.Failed {
	serving := "no digest serves the app"
	if release.Serves() {
		serving = "the host serves " + release.ServingDigest
	}
	obs.Unhealthy = &contract.Unhealthy{Cause: releaseFailed,
		Detail: "the release command failed on " + release.FailedDigest + ", and " + serving}
}
```

### Judging a release against the image a deploy asked for

A deploy is mitstack writing a new image for an app into the host's
repository, after the builder, the person who owns the app, pushes a commit.
When the new image's release command fails, or its container never turns
healthy, mitstack rolls back: it writes the earlier image into the file again,
and the host returns to it.

The host keeps a failed run in its report until the next release command runs,
and it runs none for an image whose release already passed. So when a builder
reverts a bad commit and the deploy asks for the earlier image again, the
report still names the failed run on the newer image.
`report.ReleaseAgainst(app, subscription, asked)` judges that newest run
against `asked`, the image the app's newest deploy asked for. A failure on an
older image then stops counting once the asked image serves, meaning a running
container of the app runs it. The platform calls the same function when it
judges whether a deploy turned healthy, and every block calls it for the app's
health, so both read one record by one rule.

The asked image comes from the app's image file, `image.env` in the host's
repository, which `dockerhost.ImageFile` reads and writes. A deploy writes one
line, `APP_IMAGE=`, naming the image it asks for. A rollback writes two lines.
The first, `APP_IMAGE=`, names the earlier image the host returns to, and is
the only line the host's agent reads. The second, `APP_ROLLED_BACK_FROM=`,
names the image the failed deploy asked for. After image `bbbb` fails and
mitstack returns the app to image `aaaa`, the file reads:

```text
APP_IMAGE=ghcr.io/acme/shop@sha256:aaaa
APP_ROLLED_BACK_FROM=ghcr.io/acme/shop@sha256:bbbb
```

The asked image is then `bbbb`, so the app reads failed until the next deploy
rewrites the file with one line, even a deploy of `aaaa` again.

`dockerhost.ParseImageFile(text)` reads the file, skipping any line it does
not define, as the host's agent does. `.Asked()` gives the asked image: the
`APP_ROLLED_BACK_FROM=` image when the file names one, and the `APP_IMAGE=`
image otherwise. `ImageFile.String()` writes the file back. A file that names
`APP_IMAGE=` twice, which mitstack never writes, reads by its last line.

Read the file at the commit the host's agent synced, the commit
`report.SyncedCommit()` names, so the asked image is the one the report
describes. Read it with your block's GitHub client, from the host's repository,
at `<app>/image.env`, where `app` is the app's directory in that repository. A
file the commit does not hold reads as empty.

`ReleaseAgainst` returns a `dockerhost.AskedRelease`, which carries the
`AppRelease` fields and two more. `AskedDigest` is the digest the asked image
pins, empty when `asked` is. `ExitCode` is the newest release command's exit
code, 0 when it passed or none ran, for a message that names it:

| Method | When it holds |
|---|---|
| `FailedOnAsked()` | The newest release command failed on the asked image, or failed at all when `asked` is empty |
| `AskedAbsent()` | A release failed on an older image and no container of your subscription runs the asked one |

A block that calls `ReleaseAgainst` checks these two methods in place of
`Failed`, which still holds for a failure on any image.

1. While `FailedOnAsked` holds, the block reports the release failed, with the
   cause and detail `AppRelease` describes above.
2. While `AskedAbsent` holds, no release failed on the asked image. The block
   reports that no container runs the asked image, under a cause of its own,
   and never that a release failed.
3. Once the asked image serves, neither holds, and the block reports the app
   healthy whatever the older failure was.

In this sample, `client` is your block's GitHub client, `repository` the
host's repository, `commit` the synced commit and `app` the app's directory:

```go
text, _, err := client.ReadFileAt(ctx, repository, commit, app+"/image.env")
if err != nil {
	return contract.Observation{}, err
}
file := dockerhost.ParseImageFile(string(text))
release, err := report.ReleaseAgainst(app, d.SubscriptionID, file.Asked())
if err != nil {
	return contract.Observation{}, err
}
switch {
case release.FailedOnAsked():
	// the release command failed on release.FailedDigest
case release.AskedAbsent():
	// no container runs release.AskedDigest
}
```

### Judging a container's health

A part is one piece of an app that a block's subscription creates, such as
the app's container or its database, and the console lists each part on the
app's page. The Docker host sends a node report each time one of its
containers starts, stops or changes health, and the report lists each
container's state and health. A block reads its part's container from the
host's newest report, so the console never shows the part ready while its app
is down. Every reader judges a container by one rule, `container.Read()`. The
rule reads the state first and the health after it:

| What the report shows | `Read()` answers |
|---|---|
| State `restarting`, `exited`, `dead` or `paused` | `dockerhost.ContainerNotRunning` |
| State `running`, health `unhealthy` | `dockerhost.ContainerUnhealthy` |
| State `running`, health `healthy`, or no health check (`none` or no field) | `dockerhost.ContainerHealthy` |
| State `running`, health `starting`, or a state such as `created` | `dockerhost.ContainerStarting`, neither healthy nor failed |

`reading.Failed()`, on the value `Read()` returns, holds for the two failed
answers.

`container.Unhealthy(causes)` turns a failed answer into the observation's
`Unhealthy`, and answers nil otherwise. `causes` is a
`dockerhost.ContainerCauses` naming the cause your catalogue type lists, word
for word, for a container not running and for one reading unhealthy. The
catalogue type is the platform's description of what your block creates, and
your block's `type-snapshot.json` copies it. The kit's test harness,
`kittest`, which your block's tests run, fails on a cause the type does not
list. The detail names the container and what it reads, such as
`shop-web reads restarting`, and quotes none of its output. One failure then reads the
same at every restart. The platform applies the block again once for an
unhealthy cause, a re-drive, and then waits naming the cause while that cause
and its detail stay the same. The container's last lines stay in the part's
logs.

`report.AppHealth(app, subscription, causes)` judges the containers the report
lists under the app with that subscription. It answers the first failed one's
`Unhealthy`. While none has failed and one still starts, it answers why
instead, and your observer reads unknown. The platform then keeps the healthy
or unhealthy verdict it last recorded for the part. A deploy whose new image
is still starting therefore starts no re-drive, and a crash loop that
alternates between restarting and starting keeps its one failure until a start
runs healthy.

A deploy recreates the container, and the report can catch the old one
stopped. Compose first creates the replacement under a name that puts the
first 12 characters of the old container's id before the old name, such as
`ff02de420708_shop-web`, and then stops the old one. The host sends a report
as soon as a container stops, so that report lists the old container `exited`
or `dead` beside its replacement. `container.Replacement(listed)`, given the
app's containers from `report.AppContainers(app)`, finds that replacement, and
`AppHealth` waits while it is listed, so a deploy reads no failure.

Two cases hide a failure from this rule. A container whose start failed stays
`created` with no further event, so its part waits, though the deploy run that
started it fails. A crash-looping container with no health check reads
`running` and healthy in a report taken between restarts, since the report
carries no restart count. So give every container your block runs a health
check in the compose file it writes. A block calls `AppHealth` this way, where
`app` is the app's name, `d` is the request's `contract.Desired` and `obs` is
the observation the observer builds:

```go
var causes = dockerhost.ContainerCauses{
	NotRunning: "the app's container is not running on the host",
	Unhealthy:  "the app's container reads unhealthy on the host",
}

unhealthy, waits, err := report.AppHealth(app, d.SubscriptionID, causes)
switch {
case err != nil:
	return contract.Observation{}, err
case waits != "":
	return contract.Observation{State: contract.Unknown, StatusDetail: waits}, nil
}
obs.Unhealthy = unhealthy
```

#### Checking an app's answer from inside its container

An app's image may hold no shell, no `wget` and no `curl`, so a health check
that needs one of them fails an app that serves. The kit's tools image, the
public image each kit release publishes, holds a probe for this case,
`cmd/mitstack-probe`, at `tools.ProbePath`. It fetches
the one URL it is given and exits 0 when the answer is below 400, judging a
redirect by its own status without following it. It exits 1 for an answer of
400 or above, a refused connection or no answer within four seconds, and
prints one line saying which. Given no URL or more than one, it prints its
usage and exits 2.

A block copies the probe out of the tools image onto the host, with the image
named by digest as
[Copying a tool out of an image by digest](#copying-a-tool-out-of-an-image-by-digest)
says. `tools.Probe` names the probe's path in the image and its sha256, so
`copy-tool.sh` copies it as it copies any other tool, and the image's build
checks the probe it built against that pin. The block then bind-mounts that copy read-only into the app's container and
names it in exec form, so no shell runs. In the compose file a block writes,
with the copy's path on the host in place of `<copy>`, that reads:

```json
"volumes": [{"type": "bind", "source": "<copy>", "target": "/mitstack-probe/mitstack-probe", "read_only": true}],
"healthcheck": {"test": ["CMD", "/mitstack-probe/mitstack-probe", "http://127.0.0.1:8080/"]}
```

The copy must be executable by every user, mode `0755` for instance, because
under userns-remap the container's users map to unprivileged users on the
host. The directory holding it may stay closed. On Docker 29.1.3 with the
overlay2 storage driver and userns-remap, a bind mount survives a restart of
the Docker daemon, and the probe then runs as it did before.

Do not mount the tools image itself into the container with a Compose image
mount, since on a Docker host it fails in two ways:

1. Docker 29.1.3 names an image mount's directory after the image reference.
   A reference pinned by digest makes that name longer than Linux allows, so
   the container is never created and Compose prints
   `mkdir …: file name too long`. Ubuntu 24.04's package is
   `docker.io-app 29.1.3-0ubuntu3~24.04.2` as of November 2026, and Docker
   29.1.4 carries the fix.
2. On the overlay2 storage driver, with or without userns-remap, an image
   mount with a subpath does not survive a restart of the Docker daemon, as
   [moby issue 50999](https://github.com/moby/moby/issues/50999) reports. The
   container fails to start after the restart, and a later
   `docker compose up` with the same file cannot start it either. A run on
   Docker 29.1.3 with userns-remap also found that a mount with no subpath
   comes back empty, so the health check fails from then on.

A reboot restarts the daemon, so the second failure can stop an app that was
serving.

### Running a command in an app's container

A builder can run one command, such as `php bin/add-user.php alice@example.com`,
in a fresh container of their app's image, through the `run` operation that
[Running a one-off command](handler.md#running-a-one-off-command) describes.
On a Docker host the block cannot reach the server, so it asks through the
host's repository and reads the answer from the host's node report.
`dockerhost.Runner` holds both halves, so each block answers a run with one
line of wiring:

```go
cfg.Run = dockerhost.Runner{Block: "docker-app-slice", Processes: []string{"web"}, Reports: observe.NodeReportsClient{}}.Run
```

| Field | Meaning |
|---|---|
| `Block` | The block's id, which names the request file `<block>.run-request` beside the block's compose file |
| `Processes` | The compose services the block runs, its entry first. A run naming no process runs the entry |
| `Reports` | The node reports API, `observe.NodeReportsClient{}`, or a fake in your tests |
| `GitHub` | Makes the client of the host's repository, `nil` for one that waits 30 seconds at most |
| `Now` | The clock the deadline is counted from, `time.Now` when `nil` |

`Run` reads the host from the requirement named `host`: its `subscriptionId`,
its repository from `clusterRepoUrl`, its `execKind` and its `execTarget`. An
hcloud-docker-host subscription gives `execKind` `git-pull`, since its agent
pulls the repository, and gives its Hetzner server id as `execTarget`. The
request must carry the GitHub connection's token and owner, as a subscribe
does, because the request file is committed with them.

Each call takes these steps:

1. It refuses a process the block does not run, listing those it runs, with
   the class `run-process`, before it reads or writes anything.
2. It refuses a host binding whose `execKind` is not `git-pull`, or that names
   no `execTarget`, with the class `host-binding`, and a host the platform
   attests absent with the class `host-absent`.
3. It reads the host's newest node report. When the report lists the run id,
   it answers from that entry and commits nothing.
4. Otherwise it reads `<app>/<block>.run-request` on main. When the file names
   the same run id, an earlier call committed it, so nothing is committed
   again, even after the block restarted between its commit and its answer.
5. Otherwise it commits the request, replacing the request of an earlier run,
   and answers pending. When another block's commit moved main first, it
   takes steps 4 and 5 again on the newest main, in three attempts at most.

`Run` leaves the request file in place after its run ends, and the next run
replaces it. Its run id, server id and deadline keep the host from running it
twice. `Runner.RequestPath(app)` names the file, `<app>/<block>.run-request`,
so the block's unsubscribe removes it with the app's other files.

The request file is one JSON object, which
`helpers/dockerhost/testdata/run-request.json` holds as the golden the host
reads, and `dockerhost.RunRequestFile` types:

```json
{
  "runId": "run_4c1e9a7f2b3d",
  "serverId": "61432178",
  "deadline": "2026-11-11T14:15:00Z",
  "service": "web",
  "command": "php bin/add-user.php alice@example.com"
}
```

`serverId` is the host's `execTarget`, so a host that finds the request on
another server refuses it. `deadline` is `dockerhost.RunDeadline`, 15
minutes, after the commit, in UTC to the second, and a host that finds the
request later refuses it. `service` is the process the run names, or the
entry.

The host lists each run under `host.runs` of its node report, which
`helpers/dockerhost/testdata/node-report-runs.json` holds as the golden, and
`dockerhost.ReportedRun` types. A run that ended carries `exitCode` and
`output`, beside the image's `digest` and the `startedAt` and `endedAt` times.
A run the host refused carries `refusal` and no exit code, and one still
running carries neither:

```json
{"host": {"runs": [
  {"runId": "run_4c1e9a7f2b3d", "digest": "sha256:5f1c", "startedAt": "2026-11-11T14:00:09Z", "endedAt": "2026-11-11T14:00:10Z", "exitCode": 0, "output": "added alice@example.com\n"},
  {"runId": "run_9d2b7e4a1c0f", "exitCode": null, "refusal": "the request names server 61432178, and this host is server 70011223"}
]}}
```

| The newest report | `Run` answers |
|---|---|
| Lists the run with an exit code | The exit code, the output whole, the digest and the times, whatever the exit code |
| Lists the run with a refusal | A refusal of class `run-refused` naming the run id and the host's words |
| Lists the run with neither | Pending, since the command is still running |
| Does not list the run, or the host has sent no report | Pending, once main holds the request, by step 4 or step 5 |

`Run` keeps whatever output the report carries, so the host alone decides how
much of it to keep.

### How a block waits for its final backup

A per-app block that keeps data on the host takes a last backup before it
removes that data. `dockerhost.FinalBackup` holds that wait, so each block runs
one copy. The block gives it two functions over its own repository client:
`Held` reads the request in `<block>.backup-request`, and `Ask` commits a new
one.

```go
done, err := dockerhost.FinalBackup{
	App: "shop", Block: "docker-app-slice", Kept: "shop's persisted files", Now: time.Now(),
	Held: readRequest, Ask: commitRequest,
}.Done(ctx, observe.NodeReportsClient{}, d, hostSubscription)
if err != nil || !done {
	return err
}
```

`Done` answers true once the newest node report names that request uploaded,
and at once when the report lists no volume of the block, which then holds
nothing. While the host has not uploaded it, the error is a `handler.Wait`. A
failed run, or a host that takes no backups, gives an error naming why and
nothing may be removed.

A lost host can send no node report, so `Done` would wait on one until the
delete budget ends. The wait ends sooner when the platform has seen the host
gone itself: its own observer read the host's server absent, and the request
carries that as `d.Absences`, as [The request your code
receives](handler.md#the-request-your-code-receives) describes.

`Done` reads the host through `NewestReport(ctx, api, d, hostSubscription)`,
which first calls `d.AttestedAbsent(hostSubscription)`. For an attested host,
`NewestReport` returns a report that lists no volume and reads no node report,
so `Done` finishes at once: a host holding no volume of the block has nothing to
back up. On that report `SyncedCommit` returns an error naming the observer and
the resource instead of a commit, since a gone host syncs none. A host with no
attestation that has uploaded no report yet still makes `Done` return a
`handler.Wait`.

### Running an app's database on the host

An org's Docker host is a server that runs the org's apps as Docker
containers, as "Setting up a Docker host" describes. The host follows a GitHub
repository with one directory per app. Its pull agent, a program on the host,
applies each new commit of that repository by starting or stopping the
containers the app's directory describes.

A block that runs one app's database there, such as docker-postgres, does the
same work whatever its engine, the database program it runs, such as
PostgreSQL or MariaDB. `dockerhost.Database` holds that work once, so the
block writes only what its engine needs: the image, the entrypoint, the dump
and the restore.

#### Wiring a Database into the handler

A `Database` is the block's `handler.Resource`, the part that answers a
subscribe, an upgrade and an unsubscribe, as [Building a
handler](handler.md#building-a-handler) describes. It is also the observer of
its one resource, the code that reads what the resource is now, as [What an
observer answers](observe.md#what-an-observer-answers) describes. It reads the
database container's output for the block's logs call too.

```go
db := dockerhost.Database{
	Block:   "docker-postgres",
	Type:    "hetzner.docker-postgres-database",
	Engine:  postgres(),
	Reports: observe.NodeReportsClient{},
}
h, err := handler.New(handler.Config{
	Resource: db, Observers: db.Observers(), SubscriptionLogs: db.SubscriptionLogs,
	Manifest: manifestYAML, JWKSURL: jwksURL, Logs: logs,
})
```

In this example, `postgres()` is the block's own function that returns its
`Engine`, which the table below describes. `manifestYAML`, `jwksURL` and
`logs` are what every block hands `handler.New`, as [Building a
handler](handler.md#building-a-handler) describes. The fields of a `Database`
are these:

| Field | What it is |
|---|---|
| `Block` | The block's id, which names its files in the app's directory and labels its volume and backups |
| `Type` | The catalogue type of the database resource, the type the manifest's resource names |
| `Engine` | What the engine needs, in the table below |
| `Reports` | Where the host's node reports come from: `observe.NodeReportsClient{}` in production, and the fake in a test |
| `GitHub` | Makes the client of the host's repository. Leave it nil in production, and the client waits 30 seconds at most per call |
| `Now` | The clock, nil for the system clock |

The manifest declares one resource, named `dockerhost.DatabaseResource`, and
requires `host`, the org's Docker host subscription. The `Database` reads the
host's `subscriptionId` and `clusterRepoUrl` from that binding, and the
subscription's `github` connection gives it the token for the repository. The
host itself uploads each backup to its own backup bucket, so the `Database`
reads no bucket binding.

#### What a subscribe writes

A subscribe or an upgrade reads these inputs:

| Input | What it is | Default |
|---|---|---|
| `appName` | The app's directory on the host: lowercase letters, digits and hyphens | none |
| `dbCpu` | The CPU the database reserves, such as `0.5` or `250m` | `0.5` |
| `dbMemory` | The memory it reserves, such as `512Mi` or `1Gi` | `512Mi` |
| `dbDisk` | The size of its volume, such as `1Gi` | `1Gi` |

The `Database` then writes four files in the app's directory, in one commit:

| File | What it holds |
|---|---|
| `<block>.compose.yaml` | The Compose file that runs the engine's image as the container `<app>-<Service>` on the app's own network, `app-<app>`, with no port published on the host |
| `<block>.env.names` | The one line `DB_PASSWORD=generated:<block>:password`. The host reads this file into the database container's own environment. It makes a random password once, keeps it on the host, and hands it to the container as `DB_PASSWORD` |
| `<block>.backup` | The lines `service=`, `command=` with the engine's dump, `restore=` with its restore and `every=24h`, which tell the host how and how often to back the database up |
| `reservations/<block>.json` | The CPU, memory and disk the database reserves, which "Sizing an app against what the host holds" reads |

The compose file's volume, `<Service>-data`, carries the label
`eu.mitstack.size` with the disk the database reserves. The host makes a
volume with that label as a file of that size, so the database can never
fill more of the host's disk.

#### How the app reaches its database

The `Database` hands the app's other blocks its connection only through the
resource's outputs, and the password never leaves the host. Once the database
reads present, the outputs for the app `shop` and the block `docker-postgres`
read:

| Output | Value |
|---|---|
| `host` | `shop-postgres`, the container's name on the app's network |
| `port` | The engine's port, such as `5432` |
| `dbName` | `app`, which `dockerhost.DatabaseName` names |
| `username` | `app`, which `dockerhost.DatabaseUser` names |
| `passwordSecret` | `generated:docker-postgres:password`, the host's name for the password |

An `env.names` file is the host's list of `NAME=SOURCE` lines: the app's own
file, `env.names`, fills the app's environment, and a block's
`<block>.env.names` fills that block's container. The block that runs the app
writes `DB_PASSWORD=generated:docker-postgres:password`, or any variable name
it chooses, into the app's `env.names`. The host then fills that variable from
the same password the database's container reads.

#### When a subscribe is refused

The `Database` writes nothing, and answers a refusal naming its class, when
any of these holds:

| Class | When |
|---|---|
| `app-taken` | The app's directory already holds this block's compose file for another subscription |
| `invalid-param` | An input does not parse |
| `disk-fixed` | An upgrade asks for a disk other than the volume's, which the host cannot resize |
| `host-takes-no-backups` | The host's node report says its backups fail, such as for a host made without a backup public key |
| `host-absent` | The platform reads the database's host absent |
| `host-full` | The database's reservation does not fit in what the host can give its apps less what its other apps reserve |

A host that has sent no node report giving its capacity yet gets no refusal.
The subscribe writes nothing and waits for that report, as
[Sizing an app against what the host holds](#sizing-an-app-against-what-the-host-holds)
describes.

#### What the engine gives

An `Engine` names the rest:

| Field | What it is |
|---|---|
| `Service` | The compose service, which also names the container and the volume |
| `Image` | The engine's image, pinned by digest |
| `Port`, `DataDir` | The port the database listens on, and where its volume mounts |
| `Entrypoint` | The container's entrypoint. It reads the password as `$DB_PASSWORD`, which `dockerhost.PasswordVariable` names, and the kit escapes each `$` for Compose |
| `Command`, `Environment`, `Healthcheck` | The container's command, its fixed environment and its health check's test, as Compose takes them |
| `Dump`, `Restore` | One command line each, which the engine writes to connect as `dockerhost.DatabaseUser` to `dockerhost.DatabaseName`, reading the password from `$DB_PASSWORD`. The host splits `Dump` into words as a shell would, runs it in the container without a shell, and keeps what it writes to its output as the backup, so a line that reads a variable calls `sh -c` itself. A host that replaces a lost one runs `Restore` through `sh -c` in the new container, with the newest backup on its input |
| `Trouble` | A regular expression. While the database waits on a container that starts, or that is down before the database ever read active, the newest output line it matches is quoted in the status detail of that wait, and nil quotes none. A failure's detail quotes no output |

#### Removing the database and reading it

An unsubscribe asks the host for a final backup through `FinalBackup`, as "How
a block waits for its final backup" describes, and removes the files only once
that backup is uploaded to the host's backup bucket.

The observer reads the database present once the newest node report shows
that the pull agent has applied a commit holding the database's files as main
holds them, and that its container is running and past starting. A commit
that changed only other files leaves the database current.

A database has read active once its subscription has been active, which the
platform tells the block by sending a request without `neverActive`. From then
on, a container that stops running is present but unhealthy with
`dockerhost.DatabaseNotRunning` as its cause, and one that reads unhealthy with
`dockerhost.DatabaseUnhealthy`. Before that, either one waits, since a new
database may restart while it sets itself up. The database also waits while a
recreate replaces its container, as "Judging a container's health" describes.
`container.DatabaseHealth` holds this rule for a block that reads its database
container itself.

A block's test drives the `Database` against `dockerhosttest.Host`, a fake of
the host's repository and of the node report, the signed status the host sends
the platform. The fake's fields set what its next node report holds, such as a
silent host, a lagging agent or a container's state.
`dockerhosttest.NewDatabase(t, db)` serves the block's own `Database` from a
fresh fake at a fixed clock. It fails the test on a request to the host whose
GitHub token is not `dockerhosttest.Token`, the token every subscription the
harness builds carries. The `dockerhosttest.Database` it answers gives a
block's tests these:

| Name | What a test gets |
|---|---|
| `Desired(app, params)` | The app's subscription, with its host and bucket requirements met by the fake |
| `dockerhosttest.Requirements()` | The same two requirements, as `kittest.Block` takes them |
| `Land(t, desired)` | A subscribe that fails the test unless the database lands |
| `Compose`, `BackupLine` | The app's compose file, and one `key=value` line of its backup file |
| `Observe` | What the platform reads of the database: its state and its outputs |
| `Process` | The database served as a newly started operator |
| `ListTagged` | Each app whose compose file carries a subscription's label, which `kittest.AssertBlock` reads to find what a subscription made |
| `Lose` | An app's files removed from the host, as a person deleting them by hand would, for `kittest.AssertBlock` |
| `Breaking(faults)` | The database with each write to the host made a side effect that `kittest.AssertTeardownAfterBrokenSetup` may break |
| `Holds` | Every file on the host but its README, which `kittest.AssertTeardownAfterBrokenSetup` reads after the teardown |

`dockerhosttest.AssertDatabase(t, db)` runs the scenarios every database block
holds against the block's own `Database`, each as a subtest named for what it
proves:

- Each app lands on its own network with no published port and its volume at
  its disk.
- The password stays on the host, and the app's directory holds the
  database's four files alone, so no file hands the app its connection.
- The outputs carry the connection and the password's secret name, and no
  password.
- A reservation past the host's free CPU, memory or disk is refused, and
  nothing is written.
- An upgrade keeps the volume, its size and its mount.
- Nothing is removed until the final backup reads uploaded.

The block's own tests then check only what its engine owns, such as its image,
its data directory, its dump and restore lines, and its port.

`dockerhosttest.Create(t, service, network, env...)` makes a `Container`,
without starting it, under the local Docker daemon. `service` is one service
of a compose file the `Database` rendered, such as
`d.Compose(t, "shop").Services["postgres"]`. `network` is a Docker network to
join, or empty for the default one, and each `env` is a `NAME=value` the test
sets, such as the password the host would generate. The container runs the
service as the host would, with its labels and environment, on a volume of its
own. It leaves out the service's restart policy, so a
test that stops the container decides itself when it starts again. A block's
tests use it to run their real engine through these, and each wait fails the
test after four minutes:

| Name | What it does |
|---|---|
| `dockerhosttest.Scratch(t, what)` | A fresh Docker name whose container, volume and network are removed when the test ends |
| `dockerhosttest.Docker(stdin, args...)` | One docker command, answering its output |
| `Container.Must(args...)` | One docker command that fails the test on an error |
| `Container.WaitForLine(text)` | A wait on the container's own output, followed as Docker streams it, for a line holding `text` |
| `Container.WaitHealthy()` | A wait on Docker's health events for the container to read healthy |
| `Container.Exec(line, stdin)` | A shell line run in the container, as the host runs a backup file's line |

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
cannot point it at a private address or the cloud's metadata endpoint.
`reach.ValidateCustomDomain` is the check `VerifiedCustomDomain` runs before
any lookup, and a block calls it alone when it validates a requested domain.
It refuses anything but a bare hostname, such as a scheme, a port, a path, a
wildcard, a brace or a control character, so the value never becomes config in
an edge's Caddy or ACME setup, and it accepts an empty domain. A
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

A Docker host also holds its commit stream open, the connection over which the
platform passes it each commit that `events.ReportCommit` reports, as
[Reporting progress and reading the platform](#reporting-progress-and-reading-the-platform) describes. The
`stream` mode prints the token the host opens that stream with, byte for byte
what `reportproof.SignStream(key, kid, origin, subject, now)` makes in Go. It
takes the key file, the `kid`, the platform's origin, the subject and the Unix
time, in that order. The token lasts ten minutes. Its audience names the
platform's origin and `cluster-signals`, the value of
`reportproof.StreamAudience`. Its subject is the host's own subscription id.
The mode refuses a subject of anything but
letters, digits, dots, colons, underscores and hyphens:

```bash
bash sign.sh stream /var/lib/mitstack/report.key docker-host https://api.mitstack.dev sub_host0123456789 "$(date +%s)"
```

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

## Copying a tool out of an image by digest

This section is for the author of a block whose machines or workflows need a
tool while they run, such as age on a Docker host, k3s on a cluster node or a
scanner in a build. A tool fetched from a GitHub release page fails whenever
GitHub's release downloads do. A block therefore copies each tool out of a
container image named by digest, which fixes its bytes and comes from a
registry, and the kit's [runtime-download check](block-checks.md#where-ci-runs-it)
fails a block that still names a release download.

`helpers/tools` gives you three things:

1. `tools.CopyScript`, the script `copy-tool.sh`, which your block writes onto
   the machine or into the workflow that needs the tool.
2. `tools.Image`, the kit's tools image, `ghcr.io/mitstack-eu/block-kit-tools`
   named by tag and digest, which each kit release publishes and pins.
3. A `tools.File` for each file that image holds, naming its path in the image
   and the sha256 the file must have.

### What the tools image holds

The tools image holds each tool a block reads while it runs whose publisher
ships no image of its own. `helpers/tools/Dockerfile` fetches each one once,
when the image is built, and checks it against `helpers/tools/tools.sha256`.
Each tool sits in a layer of its own, so a machine reading the image over the
registry's HTTP API downloads only the layer it needs.

| Go name | Path in the image | What it is |
|---|---|---|
| `tools.Age`, `tools.AgeKeygen` | `/age/age`, `/age/age-keygen` | age v1.3.2, the encryption tool a Docker host seals its backups with |
| `tools.NSAFramework`, `tools.NSAConfigInputs` | `/kubescape/nsa`, `/kubescape/default_config_inputs` | The NSA hardening framework kubescape checks a cluster against, and its default inputs, from kubescape's rule library regolibrary v2.0.38 |
| `tools.K3sInstaller` | `/k3s/install.sh` | The k3s installer at k3s-io/k3s commit `2d0f82fa2f933cd227fe38e1482558ce4769f464` |
| `tools.KubeBenchConfig` | `/kube-bench/cfg/config.yaml` and `/kube-bench/cfg/k3s-cis-1.11/` with `config.yaml`, `master.yaml`, `etcd.yaml`, `controlplane.yaml`, `node.yaml` and `policies.yaml` | kube-bench's config at commit `e278a5915a982f2756026de6e2f9ca77077ef9f9`, since its releases stop at k3s-cis-1.9 |
| `tools.CopyScriptPath` | `/copy-tool/copy-tool.sh` | The helper itself, for an image build to copy with `COPY --from` |
| `tools.Probe`, at `tools.ProbePath` | `/mitstack-probe/mitstack-probe` | The health probe an app's container runs from a copy on its host, as [Checking an app's answer from inside its container](#checking-an-apps-answer-from-inside-its-container) says, built from `cmd/mitstack-probe` and checked against its pin |

A tool whose publisher ships an image comes from that image instead, named by
digest, such as k3s from `registry.rancher.com/rancher/k3s-upgrade` at
`/opt/k3s`, kubescape from `quay.io/kubescape/kubescape-cli` at
`/usr/bin/kubescape`, and kube-bench from `public.ecr.aws/aquasecurity/kube-bench` at
`/usr/local/bin/kube-bench`.
gitleaks, Trivy and golangci-lint ship images too. Describe such a file with a
`tools.File` of your own, giving its image, path and sha256.

### Running copy-tool.sh

The script takes four arguments:

```sh
copy-tool.sh <registry>/<repository>[:<tag>]@sha256:<digest> <path in the image> <sha256> <destination>
```

`File.Args(dest)` returns them for a file, so a block that copies age to its
host's state directory runs this, with `tools.Age.Args` giving everything
after the script's name:

```sh
bash copy-tool.sh ghcr.io/mitstack-eu/block-kit-tools:<tag>@sha256:<digest> /age/age \
  eb7dd1b518f0a307c99cd97782623c5321da049154b04acd2d98d21aa7bc9b2c /var/lib/mitstack-agent/age
```

The script picks one of two ways to read the image:

1. Where Docker runs, such as on a Docker host or a GitHub runner, it creates a
   container from the image, copies the file out and removes the container.
2. Elsewhere, such as on a k3s node before k3s is installed, it reads the
   image's manifest, the linux/amd64 entry of a multi-arch index, its config
   and the one layer holding the file over the registry's HTTP API. It asks
   for an anonymous pull token when the registry answers 401, and it checks
   the digest of everything it reads.

Either way it checks the file's sha256 against the one it was given, and it writes the
destination only once that matches. Any failure exits 1 and names the image
and the read that failed, such as
`copy-tool: ghcr.io/...@sha256:...: layer sha256:...: the registry answered HTTP 504`,
and a digest that differs is named beside the one asked for. The HTTP route
needs only bash, curl, Python 3, tar and coreutils, which a fresh Ubuntu 24.04
cloud image and GitHub's ubuntu-24.04 runner both hold. Compressed with
`gzip -9` and base64-encoded, the script stays under 4,000 bytes, so it fits
in a k3s node's user data, and a test in `helpers/tools` fails above that.

### Testing a block that copies a tool

`helpers/tools/toolstest` is a fake registry for your block's own tests. It
serves the images a test pushes over the registry's HTTP API, in plain HTTP,
behind the same anonymous token challenge ghcr.io answers with. Setting
`COPY_TOOL_REGISTRY_URL` to its address makes `copy-tool.sh` read every image
from it over the HTTP API, even where Docker runs:

```go
reg := toolstest.New(t)
img := reg.Push(t, "mitstack-eu/block-kit-tools", toolstest.File{Path: "/age/age", Body: "age binary"})
script, dest := filepath.Join(t.TempDir(), "copy-tool.sh"), filepath.Join(t.TempDir(), "age")
if err := os.WriteFile(script, []byte(tools.CopyScript), 0o755); err != nil {
	t.Fatal(err)
}
sum := sha256.Sum256([]byte("age binary"))
cmd := exec.Command("bash", script, img.Ref, "/age/age", hex.EncodeToString(sum[:]), dest)
cmd.Env = append(os.Environ(), reg.Env())
```

`reg.Reads()` lists each digest the script read. `reg.Fail(digest, status)`
answers a read with an error status, `reg.Swap(digest, body)` serves other
bytes under a digest, and `reg.RefuseTokens(status)` refuses the pull token as
a private package does, so a test can prove what your block does when a copy
fails.

## The continuity hooks

A block that keeps data declares it under `spec.continuity` and serves the
`snapshot` and `restore` operations, as [The state a block keeps](manifest.md#the-state-a-block-keeps)
and [Snapshotting and restoring state](handler.md#snapshotting-and-restoring-state)
explain. `helpers/continuity` holds the part every such block would write the
same way. This section is for a block writer who keeps a volume or an OpenBao
store. A hook is the code that takes one snapshot or restores it, and a
handover is the string a snapshot publishes for the restore to read.

A `continuity.Hook` joins three parts. A `State` knows how to read one kind of
data out and back in, and the two ready-made ones below are the ones to use.
`Links` hands out the presigned upload and download links, each an address
that carries its own signature and so needs no storage key. `Keys`, a
`continuity.Keys`, hands out the age keys: a public key the snapshot is
encrypted to, and the restore key, the private half that decrypts it. The
kit's own `Keys` is `continuity.OwnSecretKeys`, which
[Where the age keys are kept](#where-the-age-keys-are-kept) describes. The
`Hook` itself does the rest:

```go
hook := continuity.Hook{
	Links: continuity.LeasedLinks{ // asks the platform for each link
		Platform: lease.Platform{BaseURL: "https://api.mitstack.dev", Token: reportToken},
		Resource: "scaleway.snapshot-bucket",
	},
	Keys:  keys, // a continuity.Keys holding the unit's age keys
	State: continuity.Volume{Dir: "/var/lib/app"},
}

handover, err := hook.Snapshot(ctx, "app-data")

// Later, on the server that replaced the first:
err = hook.Restore(ctx, "app-data", handover)
```

`app-data` is the unit, the name of the manifest's `spec.continuity` entry.
`hook.Snapshot` returns the handover string, and `hook.Restore` takes the same
string back, which the restore request carries. A block with several units
picks the `State` by unit, since each unit keeps its own data.

### Where a hook runs

A hook runs in the process that calls it, and that process needs three things
within reach: the data, the unit's age keys and the links. The data is the
directory of a `Volume` or the address of an OpenBao store.
`continuity.LeasedLinks` gets the links from the platform with a node's report
token, as [Asking the platform for a lease](#asking-the-platform-for-a-lease)
explains. A hook built on it therefore runs in a program on the node, where
the data is. The snapshot is then encrypted before it leaves the node, as
[ADR 0125](https://github.com/mitstack-eu/mitstack/blob/main/docs/adr/0125-a-confirmed-upgrade-replaces-a-stateful-server-through-a-recorded-handover-with-keys-from-a-per-subscription-escrow-and-two-drills-that-keep-it-working.md)
asks. An operator holds no report token, so a hook that an operator's
`Snapshot` calls in its own process cannot use `LeasedLinks`.

The kit ships the hook, the two states, `LeasedLinks` and the key helpers
below. It ships no program for the node, and it leaves two steps to your
block:

1. Your operator's `Snapshot` and `Restore`, which
   [Snapshotting and restoring state](handler.md#snapshotting-and-restoring-state)
   describes, start the program on the node and return its answer. The
   handover your `Snapshot` answers is the string that program's
   `hook.Snapshot` returned.
2. The unit's age keys reach the node. The platform hands a block's own
   secrets to its operator alone, so a key kept there is not on the node until
   your block puts it there.

The kit holds no helper for either step, and no decision record sets how a
block takes them.

### How a hook gets its links

`Links` is the one interface a hook needs from outside its own process. It
hands the hook a presigned link for one upload and one for each download:

```go
type Links interface {
	Upload(ctx context.Context, unit string) (Link, error)
	Download(ctx context.Context, unit, ref string) (Link, error)
}

type Link struct {
	URL string
	Ref string
}
```

`Upload` answers the link to upload to and, in `Ref`, the name of the object
that link writes. The handover carries that `Ref`, and `Download` receives it
back and answers a link to read the same object. `URL` is a complete address:
the hook sends one `PUT` with the ciphertext to an upload link and one `GET`
to a download link, and adds nothing to either.

`continuity.LeasedLinks` is the kit's own `Links`. `Platform` is the address
and the report token it asks with, and `Resource` is the reference your
manifest uses for the snapshot store your block requires or provides. Each
`Upload` and each `Download` is one lease. A snapshot asks for its upload link
once it has read the data out and encrypted it. It then asks for one download
link, to read the stored copy back and check it, as
[What a snapshot and a restore do](#what-a-snapshot-and-a-restore-do)
describes. A restore asks for one download link. `LeasedLinks` keeps no link between calls,
so a snapshot that follows a refused upload asks for a new link. When the
lease route refuses, the snapshot or the restore fails with the error
[Asking the platform for a lease](#asking-the-platform-for-a-lease) lists,
and the snapshot publishes no handover.

The want and the credential of a snapshot link are defined once, in
`helpers/continuity`, for the hook and for the block that provides the store.
`LeasedLinks` sends a `continuity.LinkWant`, as in these two requests:

```json
{"resource": "scaleway.snapshot-bucket", "want": {"use": "upload", "unit": "app-data"}}
```

```json
{"resource": "scaleway.snapshot-bucket", "want": {"use": "download", "unit": "app-data", "ref": "sub_a1b2c3d4e5/app-data/0001"}}
```

The block that provides the store mints the links in its `Lease`, which
[Leasing a short-lived credential](handler.md#leasing-a-short-lived-credential)
describes. It reads the want with `continuity.ParseLinkWant(l.Want)`, which
refuses a want no hook sends, and answers a `continuity.Link` as the lease's
credential:

```go
func (b *block) Lease(ctx context.Context, d contract.Desired, l contract.LeaseRequest) (contract.Lease, error) {
	want, err := continuity.ParseLinkWant(l.Want)
	if err != nil {
		return contract.Lease{}, err
	}
	link, expiresAt, err := b.sign(ctx, d, l.Caller.SubscriptionID, want) // yours
	if err != nil {
		return contract.Lease{}, err
	}
	credential, err := json.Marshal(link)
	return contract.Lease{Credential: credential, ExpiresAt: expiresAt}, err
}
```

That credential reads `{"url": "https://...", "ref": "sub_a1b2c3d4e5/app-data/0001"}`
for an upload link, and holds `url` alone for a download link. The provider
names the object of each upload itself: a new one for every ask, so a snapshot
never overwrites an earlier one, under the caller's subscription id from
`l.Caller`. It signs a download only for a `ref` under that same id, since the
platform fills `l.Caller` from its own records and the caller writes `want`.

### What a snapshot and a restore do

`hook.Snapshot` runs these steps in order, and fails at the first that does not
hold:

1. It reads the unit's data out of the `State` and encrypts it with age, using
   only the public key, into a temporary file that holds ciphertext alone. It
   refuses a `State` that exports nothing, before anything is uploaded.
2. It asks `Links` for the upload link only now, so a long export cannot
   outlive the link, and uploads the file with a `Content-Length`, since a
   presigned upload refuses a chunked body.
3. It downloads the stored object through a download link, decrypts it with
   the restore key and gives it to `State.Verify`, which restores it into a
   scratch copy, checks the copy and discards it.
4. Only after the check passes does it return the handover, a JSON string
   holding the object's reference, the ciphertext's SHA-256 and size, and the
   time of the snapshot.

`hook.Restore` parses the handover, downloads the object, refuses one whose
digest or size differs, decrypts it and gives the bytes to `State.Import`. A
hook holds no storage key, so the bucket's credentials never reach it. The
links come from the `Links` you pass, and an error from the hook never carries
a link or its query string, since the signature in a link is a credential. No
plain text of the data goes to the bucket: the upload sees ciphertext, and the
restore key stays outside the bucket. The scratch check does handle plain
data, in a scratch directory for a `Volume` and in the `Scratch` store for
OpenBao.

| State | What it keeps | How `Verify` checks it |
|---|---|---|
| `continuity.Volume{Dir}` | The directories and regular files under a directory, each with its mode and owner, as a tar archive that ends with each file's path and SHA-256 | Extracts the archive into a scratch directory, refusing a path that leaves it, rehashes every file, and gives each entry its mode and owner back |
| `continuity.OpenBao{Live, Scratch, Restart, Discard}` | An OpenBao store, through its raft snapshot routes | Force-restores the snapshot into the `Scratch` instance, requires its health route to read initialised, and calls `Discard` |

`Volume` refuses a `Dir` that is not a directory itself, such as a link to the
volume, since a walk of a link visits nothing. A restore gives each entry its
recorded owner back, which needs a process allowed to do so, such as one that
runs as root. A process that is not allowed fails the scratch check, so the
snapshot publishes no handover it could not restore. Two limits are yours to
handle. The snapshot records the entries under `Dir` and not `Dir` itself, so
a restore creates a missing `Dir` with mode 0755 and the restoring process as
its owner. Create `Dir` with the mode and owner it needs before your `Restore`
calls the hook. `Volume` also copies the files one by one while nothing stops
other writers, so your `Snapshot` stops what writes to the directory before it
calls the hook.

`Live` and `Scratch` are each a `continuity.Endpoint{Addr, Token, Client}`:
the address of an OpenBao server, a token allowed to read and restore its
snapshot, and an optional HTTP client. `Live` is the store the block keeps,
which on a restore is the store of the new server. `Scratch` is a second
OpenBao server your block provides only for the check. `Restart` and `Discard`
are each a `func(ctx context.Context) error` you supply. `Restart` restarts
every member of `Live` and returns once they are back, since nothing checks
the store after it. `Discard` removes what the check put into `Scratch`, such
as by deleting that server. With no `Client`, an endpoint follows no redirect,
so the token reaches no other host.

```go
State: continuity.OpenBao{
	Live:    continuity.Endpoint{Addr: liveAddr, Token: liveToken},
	Scratch: continuity.Endpoint{Addr: scratchAddr, Token: scratchToken},
	Restart: b.restartStore,   // yours
	Discard: b.deleteScratch,  // yours
},
```

`OpenBao.Verify` calls `Discard` whatever the check found, and refuses to
restore into a `Scratch` that has none, because the scratch copy holds every
secret of the live store. The check itself proves two things: the scratch
instance accepted the snapshot, and it still reports itself initialised. It
does not prove that the restored data unseals, which is OpenBao's step of
decrypting its storage with its seal key. `OpenBao.Import` calls `Restart`
after the force restore, since
[ADR 0125](https://github.com/mitstack-eu/mitstack/blob/main/docs/adr/0125-a-confirmed-upgrade-replaces-a-stateful-server-through-a-recorded-handover-with-keys-from-a-per-subscription-escrow-and-two-drills-that-keep-it-working.md)
has a restored store unseal once every member restarts.

### Where the age keys are kept

`continuity.OwnSecretKeys` keeps each unit's key in the block's own secrets,
which [What the answer carries](handler.md#what-the-answer-carries) describes,
so the restore key never sits in the bucket. A hook on a node reads them only
once your block has brought them there, the second step under
[Where a hook runs](#where-a-hook-runs). `continuity.MintKeys` returns an
age identity for each unit that holds none yet, under the name
`continuity.KeyName(unit)`, and never replaces a held one. A block returns
them from its `EnsureResourcesOwning`, which runs on a subscribe and on an
upgrade, so the key exists before the first snapshot reads it. `MintKeys`
returns only the keys it minted, which is what the answer needs. The unit
named here is the one `Snapshot` and `Restore` are called with. A subscription
that has run neither a subscribe nor an upgrade since the block gained the
hooks holds no key, and a snapshot of it fails naming the own secret that is
missing. A replacement takes its snapshot before its own upgrade, so such a
subscription needs an ordinary upgrade first, which mints the key:

```go
func (b *block) EnsureResourcesOwning(ctx context.Context, d contract.Desired) ([]contract.CreatedResource, map[string]string, error) {
	keys, err := continuity.MintKeys(d.OwnSecrets, "app-data")
	if err != nil {
		return nil, nil, err
	}
	created, err := b.ensure(ctx, d) // your own ensure
	return created, keys, err
}
```

`helpers/age` is the encryption underneath. It writes and reads the age v1
format with an X25519 recipient, so the public `age` tool opens a snapshot with
the restore key. It seals with the ChaCha20-Poly1305 of `golang.org/x/crypto`,
the one module the kit requires beyond the standard library, and a test reads
files made by the reference age implementation.

## Removing a member of a redundant set

This section is for the author of a block whose step removes or replaces one
member of a redundant set. A redundant set is a group of members that serve
the same thing, so the cluster keeps working while one of them is gone:
gateways, control-plane nodes with their etcd members, OpenBao members,
database instances or workers. etcd is the database a Kubernetes control plane
keeps the cluster's state in, and OpenBao is a store of secrets; each runs as a
few members that agree by majority. A replacement that runs while a second member
is down can take the set below its quorum, the number of members it needs to
keep serving. `helpers/redundancy` holds the one guard every such step calls
first, so no block writes its own check.

The guard judges only readings taken since the step began. A member that no
read has seen counts as not serving, and so does a member whose latest reading
is older than the step. An old reading proves only that the member served
then, and a removal must rest on what serves now.

### Calling the guard

Build a `redundancy.Set` from the readings your block already observes, then
call `AllowRemoval` with the member to remove and the time the step began.
Return its error as it stands:

```go
set := redundancy.Set{
	Name: "etcd members",
	Members: []redundancy.Member{
		{Name: "cp-1", Reading: &redundancy.Reading{At: reportTime, Serving: true}},
		{Name: "cp-2", Reading: &redundancy.Reading{At: reportTime, Serving: false}},
		{Name: "cp-3", Reading: &redundancy.Reading{At: reportTime, Serving: true}},
	},
	Minimum: redundancy.Majority(),
}
if err := set.AllowRemoval("cp-3", stepStart); err != nil {
	return err
}
```

A `Reading` holds when the member was read and whether it served. Take `At`
from the source you read. For a node report, take the time the node began
its read, the body's `madeAt`. When the body carries none, take the time the
platform received the report, `observe.NodeReport.ReceivedAt`.
[Observing a resource the platform cannot reach](observe.md#observing-a-resource-the-platform-cannot-reach)
describes both fields, and `NodeReported.ObserveResource` in `observe/node_reported.go` picks
between them the same way. For a provider read, take the time your block ran
it. The guard trusts `At` as you
give it, so take it from the source and never from your own clock after the
fact. A member nothing has read gets a nil
`Reading`. `stepStart` is the moment your step began, such as the start of the
`upgrade` call, so a reading from an earlier call never counts.

### The minimum each set keeps

`Set.Minimum` is the number of members that must still serve once the member
goes. Its size comes from the whole set, the member being removed included,
while only the other members' readings count toward it. A `Minimum` left unset is an error, so no set falls back to keeping
none.

| Set | Minimum | Members that must still serve |
|---|---|---|
| etcd members, OpenBao members | `redundancy.Majority()` | More than half the set: 2 of 3, 3 of 4, or 3 of 5 |
| Gateways, database instances, workers | `redundancy.AllButOne()` | Every member but the one removed, and at least one |
| A set of one member with no redundancy | `redundancy.NoRedundancy()` | None, and only a set of one may declare it |

`AllButOne` keeps at least one member serving, so a set of one refuses its
own replacement. A block whose single gateway may go while its replacement
comes up declares `NoRedundancy` for that set.

### What a refusal says

A removal that would leave fewer serving members than the minimum returns a
`*handler.Refused` of class `redundancy-below-minimum`, the
`redundancy.ClassBelowMinimum` constant. Its detail names the member being
removed, the minimum and every other member with its reading:

```text
refused:redundancy-below-minimum: removing "cp-3" from the etcd members would leave 1 serving, below their minimum of 2 (a majority of 3), judged by readings since 2026-11-08T09:00:00Z: "cp-1" read serving at 2026-11-08T09:01:00Z; "cp-2" read not serving at 2026-11-08T09:01:00Z
```

A call the guard cannot judge returns a plain error, since the mistake is the
block's own. That covers a set with no minimum, a zero `stepStart`, a member
the set does not hold, a name listed twice, and `NoRedundancy` on a set of
more than one. `ExampleSet_AllowRemoval` in
`helpers/redundancy/redundancy_test.go` runs the call above.

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
   your block's hostnames live in, such as `apps.mitstack.dev`.
2. `apphostname.NewRecord(zoneID, name, addresses...)` is the record you want.
   `zoneID` is what step 1 returns, `name` is the full hostname, such as
   `shop.apps.mitstack.dev`, and each address is a string.
3. `apphostname.ReadClaim(ctx, w, rec, subscriptionID)` returns a claim and an
   error, and reads the hostname before your block writes anything else. It
   refuses with `hostname-taken` when another subscription holds the name, or when an A record that names
   no subscription points somewhere else, and with `no-address` when the
   record has no address.
4. `claim.Apply(ctx)` writes the changes in one batch once your block's own
   writes have landed. A batch Route 53 rejects because a racing call changed
   the records is read again, and passes when the records are now this
   subscription's.
5. `apphostname.Release(ctx, w, rec, subscriptionID)`, called from your block's
   `Delete`, deletes only what the subscription holds, and counts records
   already gone as deleted.
6. `apphostname.Observe(ctx, w, rec, subscriptionID)`, called from the
   `ObserveResource` of the hostname's `Observer`, answers present while the
   A record holds the record's addresses and the TXT names this subscription or
   none, with the `appHostname`, `address` and `appUrl` outputs that a
   `mitstack.app-hostname` resource declares. It answers absent when the hostname has no
   record, points elsewhere or is held by another subscription.

In these steps `w` is `apphostname.Route53{}` in the operator, `rec` is the
record from step 2, and `claim` is what step 3 returns.

Record the resource's identity from your `EnsureResources`, the method and the
`Observer` interface that
[Serving lifecycle calls](handler.md#what-the-handler-calls-for-each-call)
shows. Return a `contract.CreatedResource` for the hostname with its `Identity`
set to `apphostname.Identity(rec)`. The platform stores it and sends it back as
the `Identity` of the `contract.ResourceRef` your observer receives, and
`apphostname.FromIdentity` reads the record from it.

Your observer can call `apphostname.ObserveRecorded(ctx, w, ref.Identity,
subscriptionID)` in place of `Observe`. It reads as `Observe` does when the
identity names a record, and answers absent with no read when it names none. A
new app has no identity yet, so its dry run, the check the platform makes before
it creates anything, spends no time on Route 53. That dry run also says nothing
about a taken hostname, which `ReadClaim` refuses at subscribe. If your block
must refuse a taken hostname on validate, call `Observe`, because only a read
can see another subscription's record.

Your tests pass an `apphostnametest.Zone` as `w` in place of Route 53. It
applies a batch whole or not at all, as Route 53 does. Its
`HeldBy(subscriptionID)` names what a subscription left in the zone, and its
`Lookups()` counts the reads it answered.

## Calling the GitHub API

`capabilities/github/ghapi` is a GitHub REST client whose host the operator
decides. `ghapi.Base(endpoint, extraHosts...)` resolves the API host a request
supplies against an allowlist of `api.github.com` and the hosts you add, and
refuses a private, loopback, bare-IP or metadata host even when you listed it.
`ghapi.BaseFromEnv(endpoint)` adds the hosts in `MITSTACK_GITHUB_API_HOSTS`.
`ghapi.NewClient(base.String(), token, httpClient)` then creates repositories,
commits files and sets Actions variables, each idempotently.

A request names the account the org's GitHub connection is installed on, and
the id of the mitstack GitHub App whose installation minted its token.
`ghapi.Owner(d)` and `ghapi.AppID(d)` read them from the `contract.Desired`
your block already holds, and answer `""` when the request carries none. Each
reads the connection's plain settings first, which a request carries in
`Instances["github"].Config`. It falls back to the connection's secrets in
`Secrets["github"]` only when the settings lack the value. Read both through
these two helpers and never from the request yourself, so your block keeps
working when the place a request carries them changes.

An empty owner means the org has no GitHub connection, so refuse the request
before you name a repository with it:

```go
owner := ghapi.Owner(d)
if owner == "" {
	return handler.Refuse("missing-github-access", "the request carries no GitHub owner; reconnect GitHub and retry")
}
repo := owner + "/" + d.Params["appName"]
```

Neither value is a credential. The kit's handler, which serves every request
your block receives, scrubs from each answer and each log line it writes every
value of four or more characters that a request's secrets hold. A message
naming such an owner therefore reads `[REDACTED]` in its place for as long as
the request carries the owner in `Secrets["github"]`.

`EnsureRepo(ctx, org, name, appID)` creates a private repository, or finds one,
and keeps a ruleset on its main branch. The ruleset refuses every update,
force-push and deletion of main, except those of the GitHub App that `appID`
names. Pass `ghapi.AppID(d)`, so the block's own commits still land. Each call
repairs the ruleset: it creates a missing one, puts back one whose rules,
branch or bypass list changed, and leaves a matching one alone. An empty
`appID` fails with `ghapi.ErrNoAppID` before any request. An org owner can
still delete the ruleset, and the next call restores it.

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

A GitHub answer that a retry cannot change fails the subscription at once.
`ghapi.AnswerError(route, status, header, body, token)` turns a 422, or a 403
that is no rate limit, into a refusal of class `ghapi.ClassGitHubRefused`. The
refusal carries GitHub's own message, so the operator answers 400 and the
builder reads why. A 403 counts as a rate limit when it carries `Retry-After`,
an `X-RateLimit-Remaining` of `0`, or "rate limit" in its message. That kind of
403 stays a plain error, as do a 429 and a 5xx, and the platform retries them.
The client's own writes and its ruleset reads use it, and an operator passes
its own GitHub answers through it. A ruleset create refused with a 403 reads
like this, GitHub's message last:

```text
refused:github-refused: github POST /repos/acme/widget/rulesets returned 403: Upgrade to GitHub Pro or make this repository public to enable this feature.
```

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
removal pull request. While a check main requires is queued, running or failed
on it, the answer carries `Removing`, whose `WaitsOn` names the pull request,
that check and its run, and says what ends the wait, such as
`removal pull request #4 of acme/weather
(https://github.com/acme/weather/pull/4), whose required check "managed-ci"
failed (run https://github.com/acme/weather/runs/812). The wait ends once
"managed-ci" passes, or once the pull request is merged or closed`.
The platform then waits on the removal without spending one of the
unsubscribe's attempts. A new result for that check moves the removal on, and
so does the pull request merging or closing.

Every wait the kit reports on an open removal pull request names it by its
number, its repository and its web address. That holds for the wait `Remove`
returns and for the observer's `WaitsOn` alike. The address is the `html_url`
GitHub answered for the pull request, which `openPullRequest` and
`pullRequest` in `capabilities/github/landing/remove.go` read, so it carries
the host that serves the repository.

Each wait also says what ends it. A wait on a required check ends once that
check passes. While no required check is queued, running or failed on the
pull request, a review main requires holds it when an approving review is
missing or a reviewer requested changes. The observer's answer carries
`Removing` then as well, and that wait ends once an approving review is given
and no reviewer requests changes. Every wait ends once the pull request is
merged or closed.

`Remove` and the observer read one judgement of the required checks,
`requiredCheckWait` in `capabilities/github/landing/checks.go`. While a
required check is queued, running or failed, the wait `Remove` answers for
one repository is word for word the one the observer names. When several
required checks hold the pull request, the wait names a failed one before a
queued or running one. A pending commit status, and a check run that GitHub has
queued but not started, count as queued. For a check that a commit status
reports, the run is the status's `target_url` without its credentials, query or
fragment, and the wait names no run when that address is not http or https. Once the pull request closes
unmerged, or every required check has passed or not yet reported, the answer
carries no `Removing` for the checks, and the attempts count again. A read of
the pull request or its checks that fails reads unknown. Outside an
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


## Keeping a Hetzner Cloud server's public address

A server block whose hostnames resolve to its server's own public address keeps
that address for the life of the subscription. Hetzner calls such an address a
primary IP, and a primary IP with Hetzner's `auto_delete` setting on is deleted
with its server. A deleted address returns to Hetzner's pool, and another
customer can then draw it. That customer could obtain certificates for every
hostname still naming it, through a certificate authority's HTTP challenge, so
a replaced server has to come back on the same address.

`capabilities/hetzner/cloud` holds this rule. `cloud.New(token)` takes the
Cloud API token of the block's connection, which is the Hetzner Cloud project
the subscriber connected, and the request carries it in `d.Secrets`. Each call
finds the subscription's addresses by the Hetzner labels you pass, so those
labels must name the subscription. The constant
`observe.HetznerSubscriptionLabel` holds the key `mitstack.dev/subscription`
for that. No labels fail before any call, because an empty selector reaches
every address in the project.

The table names where each call belongs. The first two run inside your
`EnsureResources`, the method the handler calls on `subscribe` and `upgrade` to
make a subscription's resources stand, and the third in your `Delete`.

| Call | When to call it | What it does |
|---|---|---|
| `KeepPrimaryIPs(ctx, labels)` | First in `EnsureResources`, also while a server stands | Turns `auto_delete` off on each address that has it on, so an address made by an older release survives its server's next deletion |
| `PrimaryIPForNewServer(ctx, spec)` | Only when `EnsureResources` finds no server and creates one | Returns the address the last server left, or creates one with `auto_delete` off when the labels hold none |
| `DeletePrimaryIPs(ctx, labels)` | In `Delete`, after the servers are deleted | Deletes every address of the labels, and takes one Hetzner already removed as deleted |

Here is how a block calls them, as
[hcloud-docker-host](https://github.com/mitstack-eu/hcloud-docker-host) does.
`d` is the request's `contract.Desired`, and `name` and `location` are your
block's own:

```go
labels := map[string]string{observe.HetznerSubscriptionLabel: d.SubscriptionID}
ips := cloud.New(d.Secrets["hetzner"]["api_token"])
if err := ips.KeepPrimaryIPs(ctx, labels); err != nil {
	return nil, err
}
// With no server standing:
ip, err := ips.PrimaryIPForNewServer(ctx, cloud.PrimaryIPSpec{Name: name, Location: location, Labels: labels})

// In Delete, once the servers are gone:
err = ips.DeletePrimaryIPs(ctx, labels)
```

`PrimaryIPSpec` names the address to make: its `Name`, its `Location` and the
`Labels` every later call finds it by. The answer is a `PrimaryIP`, whose `IP`
is the address as text for your server's configuration. The package creates no
server, so your block creates it with its own Hetzner client and passes `ip.ID`
as the server's `public_net.ipv4`.

`PrimaryIPForNewServer` deletes nothing. An address still assigned to a server
answers an error naming both. The engine
[retries that error](handler.md#retries-and-the-idempotency-cache), and Hetzner
frees the address once its server is deleted. An address in another location
answers `handler.Refuse` with the class `primary-ip-elsewhere`, which the engine
reads as permanent, because Hetzner assigns an address only in its own location
and moving it would mean releasing it. Hetzner also refuses to delete an
assigned address with `must_be_unassigned`, so a `DeletePrimaryIPs` that runs
before its server is gone fails naming the address.

An address with `auto_delete` off stays in the subscriber's project until
something deletes it, so `Delete` must call `DeletePrimaryIPs`. A block that
serves through a load balancer, whose address no server holds, has no need for
this rule.

`capabilities/hetzner/cloud/cloudtest` holds a fake of the API's servers,
firewalls and primary IPs for your block's tests. `cloudtest.New(t, token)`
returns a `Fake` that keeps state and refuses what Hetzner refuses. `Fake.Drop`
deletes a server behind your block's back, so a test can watch the next
`EnsureResources` bring it back on the same address. `cloud.NewAt(fake.URL(),
token)` points the client at the fake. Set `Fake.Hook`, a
`func(http.ResponseWriter, *http.Request) bool`, to answer a request yourself,
as a fault does, and return true when it has answered.

Hetzner's API reference at
[docs.hetzner.cloud](https://docs.hetzner.cloud/reference/cloud#primary-ips)
describes each call.

## Waiting on a Hetzner Cloud action

Hetzner Cloud answers a call that changes a resource, such as creating a
server, with an action: Hetzner's record of that work, which runs until Hetzner
marks it a success or an error. Hetzner refuses a call on a resource with
`locked` while an action on it still runs, so a block waits for each action it
starts before its next call. Hetzner sends no signal when an action ends, so
the wait asks Hetzner again until it does.

`cloud.WaitForActions` is that wait for every Hetzner Cloud block. It takes the
request's context, your client's waiter, a few words naming what the actions
do, and the actions. The waiter is the function in your block's own Hetzner
Cloud client that asks Hetzner until the actions end, such as
`Client.Action.WaitFor` in hcloud-go, Hetzner's Go client. The waiter does the
asking, so the kit adds no timer of its own, as [the kit's wait
standard](maintainers.md#judging-the-waits-a-pull-request-adds) requires.
hcloud-go's waiter at v2.49.0 skips a nil action, so a call that may start
none needs no check of its own. Creating a primary IP is such a call, since
Hetzner may create an unassigned address with no action.

Here is how a block built on hcloud-go creates a server and waits on it, with
`client` the block's `*hcloud.Client`:

```go
res, _, err := client.Server.Create(ctx, opts)
if err != nil {
	return fmt.Errorf("creating server %s: %w", opts.Name, err)
}
return cloud.WaitForActions(ctx, client.Action.WaitFor, "server "+opts.Name+" to be created", res.Action)
```

The table gives what the call returns in each case.

| When | What `WaitForActions` returns |
|---|---|
| Every action succeeds | `nil` |
| Hetzner marks an action an error | `waiting for server web-1 to be created: server limit exceeded (server_limit_exceeded, 42)`, wrapping the client's error, so `errors.As` still reaches hcloud-go's `hcloud.ActionError` and its `Code` |
| The context ends while an action runs | An error that names what it waited for and wraps the context's error, so `errors.Is(err, context.DeadlineExceeded)` holds. hcloud-go words it `waiting for server web-1 to be created: context deadline exceeded: remaining running actions: [42]` when the context ends between asks, or with the error of the ask in flight, as `waiting for server web-1 to be created: Get "https://api.hetzner.cloud/v1/actions?id=42...": context deadline exceeded` |

In these errors, 42 is the id Hetzner gave the action. Test the context's end with `errors.Is`, since the wording depends on when the context ends.

The context bounds the wait, so it lasts no longer than the call it serves.
When the call's time runs out first, the call fails with that error, and the
engine retries it, as [Errors, refusals and
panics](handler.md#errors-refusals-and-panics) explains. How often the waiter
asks Hetzner is your client's own setting, such as hcloud-go's `WithPollOpts`.

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
| `WriteFirewall(ctx, number, fw)` | Waits out an earlier change, writes the firewall and waits until Robot reads it settled, which took 62 to 131 seconds. Each of the two waits gives up after five minutes, so a call under `context.WithoutCancel` still ends. The call then fails naming each field Robot reads back otherwise than written: the status, `filter_ipv6`, `whitelist_hos`, the input rules and the output rules. It never sends Robot's two internal mail-port rules back, since Robot keeps them ahead of every write |
| `OpenRescueWindow(ctx, number, installer)` | Adds one rule admitting SSH from the installer's address alone and admits Hetzner's own services. The function it returns writes back the firewall it found |
| `NewRescueKey()` and `BootRescue(ctx, number, keyName, key)` | Mints a one-time ed25519 key, registers it and resets the server into a rescue system that admits that key alone |
| `LeaveRescue(ctx, session)` | Clears any activation Robot still holds and deletes the key, so the next reset boots the disk |

A rescue boot answers the host keys Robot names in `RescueSession.HostKeys`.
On the trial Robot named none, so a client cannot tell the rescue system from
an impersonator on the path to it. A block therefore sends that session no
secret, and its secrets reach the disk by a channel an on-path attacker cannot
impersonate.

`Installer.Install(ctx, target, handOff)` runs the whole install of one
dedicated server, from adoption to the reset that boots the written disk. It
refuses a server outside `robot.Locations`, fsn1, nbg1 and hel1, where a
vSwitch reaches a Cloud network. It then adopts the server, couples its
vSwitch, writes the target's firewall and boots the rescue system.

Over the rescue session the install runs the block's own rescue script twice,
each time as `bash -s -- <step> <arguments>` with the script on its standard
input. The first argument names the step, and the script handles two:

- `read` takes no other argument and prints the hardware, one fact a line:
  `efi yes` or `efi no`, `mac <address>` for the network card the default
  route leaves by, and `disk <name> <serial> <size>` for each disk. The
  install chooses the disk whose serial sorts first, and refuses a server
  where no disk has a serial.
- `write` takes, in this order, the VLAN, the server's vSwitch address with
  its prefix, the vSwitch gateway, the short-lived server's address, its port,
  the `sha256//` hash of its TLS key and the chosen disk's serial. The
  install matches each against a pattern before it sends them. The script
  fetches the secret from the short-lived server over the vSwitch, checks
  the hash and writes the disk.

Either step fails by exiting non-zero. The install's error then ends with the
end of what the step printed, if it printed anything. Between the two steps,
`handOff` renders the short-lived Cloud server for the hardware `read`
reported, and the installer's `Cloud`, the Hetzner Cloud calls the block
supplies, creates it on the vSwitch. Whether the hand-off succeeds or fails,
the install deletes the short-lived server and the rescue key, restores the
firewall and resets the server into its disk.

The kit's module holds no SSH client. `Installer.Dial` opens the session, and the module
`github.com/mitstack-eu/block-kit/capabilities/hetzner/robot/rescuessh`
provides one: `rescuessh.Config{Script: script}.Dial` accepts only a host key
Robot named, and any key when Robot named none, and it runs nothing but the
script. The session's `Run` returns the end of the script's output, the last
whole lines that fit 64 KiB, even when the script fails or the context ends
first. `Run`'s error quotes none of that output, so scrub the output before
you report it if your script can print a secret. A last line longer than
64 KiB leaves no output at all, since a line cut at its start could lose the
words your scrub looks for. `rescuesshtest.New(t, admit)` is a fake rescue
system for a block's tests, which runs each command with bash and admits a
client key while `admit` returns true for it.

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
`fake.ClientAt(base)` returns the same client sent to `base` instead, such as
a proxy the test puts in front of the fake. `fake.Calls(method, path)` counts the requests sent, and
`fake.SetRateLimited(true)` answers every request `403 RATE_LIMIT_EXCEEDED`
until it is set false.

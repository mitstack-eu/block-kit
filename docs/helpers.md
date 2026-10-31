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
| `helpers/dockerhost` | Sets up a host that runs apps as Docker containers behind a caddy edge |
| `helpers/reach` | Serves an app at a URL and reports whether it answers |
| `helpers/reportproof` | Has its nodes sign the reports they post to mitstack |
| `capabilities/aws/cloudid` | Creates its resources in the org's AWS account |
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

Every app on a Docker host reserves CPU and memory, so a host takes no more
apps than it can run. A per-app block records each reservation as a file in
the host repository, the Git repository the host's pull agent applies, at
`<app>/reservations/<block>.json`:

```json
{"cpuMillis": 500, "memoryBytes": 536870912}
```

`dockerhost.SumReservations(files, except)` takes the repository's files,
each path and its content, and adds up every such record, whichever block
wrote it. It leaves out the path `except`, so a block sizing its own app
counts only what the other blocks hold. A top-level directory whose name
starts with a dot holds no app, so its files never count. A record that does
not parse, holds a field a `dockerhost.Reservation` lacks or reserves a
negative amount returns an error naming its path.

Your block compares the sum with the host's allocatable capacity, which the
host's node report gives, and refuses an app whose own reservation does not fit
in what is left.

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

## Calling the GitHub API

`capabilities/github/ghapi` is a GitHub REST client whose host the operator
decides. `ghapi.Base(endpoint, extraHosts...)` resolves the API host a request
supplies against an allowlist of `api.github.com` and the hosts you add, and
refuses a private, loopback, bare-IP or metadata host even when you listed it.
`ghapi.BaseFromEnv(endpoint)` adds the hosts in `MITSTACK_GITHUB_API_HOSTS`.
`ghapi.NewClient(base.String(), token, httpClient)` then creates repositories,
commits files and sets Actions variables, each idempotently.

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
on it, and while GitHub refuses the merge, `Remove` returns a
`*handler.Waiting`, the error `handler.Wait` builds. Returned from `Delete`, it
makes the unsubscribe answer `absent:false`, and the platform sends the
unsubscribe again, as
[Serving lifecycle calls](handler.md#what-the-handler-calls-for-each-call)
describes. A later call reuses the branch and pull request while main has not
moved, and rebuilds the removal onto main's new head when it has. Once the
merge lands, `Remove` deletes every branch whose name starts with
`landing.BranchPrefix(subscription)`, `mitstack/<subscription>/`. A block that
also opens pull requests of its own names their branches under that prefix,
so the removal clears them too.

`landing.Observer` reads such a resource from main. It is present while main
carries at least one file recorded `created` at its recorded blob, and absent
once main carries none. It also reads a repository proven deleted absent:
the repository answers 404, and the installation the token belongs to lists
its repositories as `repository_selection: "all"`, which means every
repository of the owner, with one of the same owner. Any read that fails,
such as a 403, a 5xx, a rate limit or a missing token, reads unknown, and
`Remove` then waits with no write sent.

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
checks.


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

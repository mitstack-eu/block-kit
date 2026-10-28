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
| `helpers/reach` | Serves an app at a URL and reports whether it answers |
| `capabilities/aws/cloudid` | Creates its resources in the org's AWS account |
| `capabilities/github/ghapi` | Calls the GitHub REST API |
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

## Adopting a Hetzner dedicated server

`capabilities/hetzner/robot` calls Robot, Hetzner's webservice for dedicated
servers. `robot.New(user, password)` takes the Robot login from the call's
`hetzner` connection and stores it nowhere else. The login reaches every
server of the Robot account, so a block's README names that reach as a limit.
The calls follow the shapes and timings Robot answered on a live trial,
recorded in the workspace note
[Talos on a Hetzner Robot server behind a vSwitch gateway](https://github.com/mitstack-eu/mitstack-workspace/blob/main/product/knowledge/2026-10-28-talos-on-a-hetzner-robot-server-behind-a-vswitch-gateway.md).

| Call | What it does |
|---|---|
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
impersonate. The package holds no SSH client, since the kit takes no
dependency beyond the standard library: `RescueKey.PrivateKey()` hands the
block the key for its own SSH client.

`robottest.New(t)` is a fake of the webservice for a block's tests. It lists
the internal rules ahead of the rules a write sent, reads a firewall `in
process` once after each write, clears a rescue activation once it boots it,
and lists a server joining or leaving a vSwitch as `in process`.
`fake.Client()` returns a client logged in to it.

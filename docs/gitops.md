# Deploying through GitOps with the gitops package

This page is for a block author whose resources run on a Kubernetes cluster
that Argo CD manages. Argo CD is the controller on the cluster that applies
whatever a GitOps repository holds, so a block deploys by committing files
there instead of calling the cluster. The kit's
`capabilities/github/gitops` package commits the Kubernetes files a block
renders to that repository on GitHub, waits for Argo CD, and observes each
resource from Argo CD's report of the deploy.

## How a deploy works

A block's `EnsureResources` ships its whole desired state with
`gitops.Deploy` before it reports the resources it rendered. `Deploy` takes a
`gitops.BlockDeploy` holding the app, every file, the commit message, the
request's secret values and the poll interval. It commits every file once and
waits on that revision, or on the branch's head when nothing changed. The
`gitops.Deployed` it returns carries the revision, whether it committed, and
Argo CD's verdict, so a failed sync comes back in `Deployed.Outcome` for the
block to refuse:

```go
func (b *block) deploy(ctx context.Context, d contract.Desired) error {
	out, err := gitops.Deploy(ctx, b.repo, b.notes, gitops.BlockDeploy{
		App:     d.AppID,
		Files:   render(d),
		Message: "deploy " + d.AppID,
		Secrets: gitops.SecretValues(d),
		Poll:    5 * time.Second,
	})
	if err != nil {
		return err
	}
	if out.Outcome.Verdict == gitops.VerdictFailed {
		return handler.Refuse("deploy-failed", "Argo CD's sync of %s failed: %s", out.Revision, out.Outcome.Notification.Message)
	}
	return nil
}
```

The block's `Delete` ships the removal the same way, handing `Deploy` each
rendered path with no content. Paths already gone commit nothing, so deleting
what was never created succeeds. `gitops.Revert` restores every path one
revision changed to its content at that revision's parent.

## Observing a deployed resource

A `gitops.Resource` is the `handler.Observer` of a resource that deploys
through Argo CD. You give it three functions: one returns the
`gitops.Repository` the resource lives in, one returns its source of Argo CD
notifications, and one renders its files.
`gitops.NotificationsFromContext` builds the notification source from the
request's events callback, the URL and token the platform puts in every
request, which the package reuses to read the notifications the platform
relayed:

```go
resource := &gitops.Resource{
	Repository: func(ctx context.Context, d contract.Desired) (gitops.Repository, error) {
		return repoFor(ctx, d)
	},
	Notifications: func(ctx context.Context, _ contract.Desired) (gitops.Notifications, error) {
		return gitops.NotificationsFromContext(ctx)
	},
	Render: renderNamespace,
}
```

`ObserveResource` renders the files from the request its
`contract.ResourceRef` carries, so it judges exactly what the block's code
committed for that request. Argo CD may sync a later revision than the one the
block committed, so the notification for a change can name a later commit.
Observe therefore reads the app's newest notification, whatever revision it
names, and judges it only when the files at that revision match the render.
A revision whose sync reports phase `Succeeded` and health `Healthy` is
deployed, and the sync status and the images are never read.

Observe answers unknown when the branch's head holds the rendered files only
in part or with other content, when Argo CD has reported no deploy of the app
yet, and when the newest notification names a revision that does not carry the
change. A resource never committed is therefore absent only in an empty
repository, and in a repository holding other apps' commits it stays unknown
until Argo CD reports on the app. A sync in phase `Error` or `Failed`, or with
health `Degraded`, makes observe return an error carrying the phase, the
health and the message.

`gitops.Check` and `gitops.Wait` judge only the notification for the exact
revision you hand them. Use them when you need the outcome of one particular
commit, since a notification for any other revision leaves them pending.
`Wait` polls until the deploy succeeds or fails, and a failed deploy is an
outcome there, not an error.

## Which notifications a block reads

The platform relays Argo CD's notifications and answers a block only those of
the cluster that met its cluster requirement, within the block's own org. That
requirement is the block's one requirement whose type's snapshot `kind` is
`kubernetes-cluster`, such as `hetzner.k3s-cluster`. A block whose requirement
binds no cluster, or several, reads none, so its observe answers unknown. A
cluster that hosts apps of several orgs needs a scoping rule of its own,
which the platform does not provide. A notification looks like this:

```json
{"app":"app-alpha","project":"app-alpha","sync":"Synced","revision":"9ee9b7e28c000b16a89b7b717e5f3b9fa6fcf611","health":"Healthy","phase":"Succeeded","message":"successfully synced (all tasks run)","images":null,"reconciledAt":"2026-09-28T10:47:23Z"}
```

## Keeping secrets out of the repository

`gitops.CommitManifests`, and `gitops.Deploy` through it, refuses a file that
carries any of the request's secrets, verbatim or base64-encoded on its own,
with `gitops.ErrSecretInManifest` before anything is written. That check is a
backstop. A secret encoded inside a larger value, such as the token in a
`.dockerconfigjson` auth field, passes it, and so does a secret in raw or
URL-safe base64 or any other encoding. A block therefore never renders secret
material into a file it commits. A secret reaches the app's workloads through
the External Secrets Operator on the cluster, which copies it from the
cluster's secret store into a Kubernetes Secret.

## The GitHub repository and the test fakes

`gitops.NewGitHubRepository` is the kit's `gitops.Repository` over GitHub, and
it commits to the repository's `main`. It takes the API host from the request's
`github` instance, resolved through `ghapi.BaseFromEnv`, and the token from the
request's `github` secret. Apart from the empty revision `""`, every revision
it reads at or commits on must be a full lowercase hex SHA of 40 or 64 digits,
and anything else is refused before a request goes out. File content must be
UTF-8, because the Git Data API carries it as a JSON string. A commit whose
parent is no longer the tip lands nothing, so `CommitManifests` and `Revert`
retry it on a fresh head a bounded number of times.

In tests, `gitopstest.NewRepository` and `gitopstest.NewNotifications` give
in-memory fakes of the repository and the notification source.

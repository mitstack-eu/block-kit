# Deploying through GitOps with the gitops package

This page is for a block author whose resources run on a Kubernetes cluster
that Argo CD manages. Argo CD is the controller on the cluster that applies
whatever a GitOps repository holds, so a block deploys by committing files
there instead of calling the cluster. The kit's
`capabilities/github/gitops` package commits the Kubernetes files a block
renders to that repository on GitHub. It then judges the deploy by the objects
those files declare, as the cluster's node reports show Argo CD applied them.

## How a GitOps block judges its deploy

Several blocks can deploy into one Argo CD Application, as an app's
deployment and its database do. An Application is the set of objects Argo CD
applies from one place in the repository. Argo CD's notification of a sync
speaks for the whole Application, so a sibling's broken object would fail
every block in it. Argo CD sends a notification when one of its triggers, the
rules that name the changes worth sending, fires, and the trigger for a
`Degraded` Application fires only once. A commit that lands while the
Application stays `Degraded` can therefore get no notification at all.

The verdict therefore reads the cluster's node reports. A node report is what
a node of the cluster sends the platform, and
[Observing a resource the platform cannot reach](observe.md#observing-a-resource-the-platform-cannot-reach)
describes it. Each report lists every object an Application applied, with
Argo CD's sync status and health of each. `gitops.Check` reads the objects
the block's own files declare, finds each one in that list, and judges the
commit by those objects alone.

`Check` is the one call every GitOps block makes, whatever kinds of object it
deploys. It takes a `gitops.Deployment` and the revision to judge, which is
the id of a commit in the repository, and returns a `gitops.Outcome`. The `Deployment` names where each input comes from:

| Field | What it holds | Where a block gets it |
|---|---|---|
| `Repository` | The GitOps repository | `gitops.NewGitHubRepository` |
| `Notifications` | Argo CD's notifications, which the platform relays | `gitops.NotificationsFromContext(ctx)` |
| `NodeReports` | The cluster's node reports | `observe.NodeReportsClient{}` in production, `gitopstest.NewNodeReports()` in a test |
| `Desired` | The request, a `contract.Desired`, and only a report made after its `VerbAt`, the time the request's verb began, counts. The verb is the platform's call to the block, such as a deploy or a removal | The request the kit's handler hands the block |
| `Application` | The Argo CD Application that applies the block's files | The name the block also records as `argocdApplication` in a resource's identity |
| `Cluster` | The cluster subscription whose reports to read | The cluster binding's `subscriptionId`, which the block also records as `clusterSubscription` |
| `Paths` | The files the block owns, whose objects are judged | A block hands `Check` the paths its render writes, and `Deploy` sets them from the files it commits |

`Check` refuses a `Deployment` missing any field but `Desired` with an
error. A block's observe calls it like this:

```go
notes, err := gitops.NotificationsFromContext(ctx)
if err != nil {
	return gitops.Outcome{}, err
}
out, err := gitops.Check(ctx, gitops.Deployment{
	Repository:    repo,
	Notifications: notes,
	NodeReports:   observe.NodeReportsClient{},
	Desired:       d,
	Application:   "app-" + d.AppID,
	Cluster:       d.Bindings["cluster"]["subscriptionId"],
	Paths:         []string{"apps/" + d.AppID + "/deployment.json", "apps/" + d.AppID + "/service.json"},
}, revision)
```

`out.Verdict` is `gitops.VerdictSucceeded`, `gitops.VerdictFailed` or
`gitops.VerdictPending`. Whenever `Check` returns no error, `out.Detail` says
what the verdict rests on. A block can put it in a refusal or an unknown
observation as it stands.
`gitops.Wait` calls `Check` every poll interval until the verdict is no longer
pending. A failed deploy is an outcome there, and an error means a source
could not be read.

### Which node report counts

`Check` reads the newest node report of `Cluster` made since the request's
`VerbAt`. The report counts only when its Application's `revision` is the
block's own commit, or a later one with no commit since the block's own that
changed one of `Paths`. The block's own commit is the newest one at or before
the revision handed to `Check` that changed one of `Paths`.

In every other case the verdict is pending, and `Detail` says which case it
is:

- No report has arrived since the verb, with what the cluster's enrolment
  shows, as in `no node report made since the verb has arrived`.
- The report carries no revision, as a report from a talos-cluster-kit
  release before 0.50.0 does. Talos-cluster-kit is the Go module that
  cluster blocks use to set up Kubernetes, and it writes the report.
- The report's revision is `null`, which gives no revision to judge. The
  report holds `null` before Argo CD first compares the Application, and for
  an Application with several sources, whose status lists their revisions
  instead.
- The revision is an earlier one, or a later one after a commit that changed
  the block's files. `Detail` names that commit and the file.
- The report names no such Application, or carries no list of its objects, as
  a report cut to fit its size does.

### What each verdict means

| What the counting report shows | Verdict | What `Detail` names |
|---|---|---|
| An object the block's files declare reads `Degraded` | Failed | The object and Argo CD's message |
| Argo CD notified a sync in phase `Error` or `Failed` at a revision holding the block's files, and one of the block's objects is not `Synced` | Failed | The sync's phase and message, and the object |
| Every object is listed and `Synced`, and reads `Healthy` or has no health check, and no object the commit removed is still listed | Succeeded | How many objects it judged |
| Anything else, such as an object reading `Progressing` or missing from the list | Pending | The first object that is not ready, and why |

A sibling's objects decide nothing, whatever their health. An object Argo CD
runs no health check for, such as a Secret, counts as healthy once it is
`Synced`. A commit that deletes the block's files succeeds once the
Application no longer lists the objects those files declared. A commit whose
files declare no object and that removed none stays pending, since no report
can show its deploy. A mistyped path therefore never reads as a deploy that
succeeded.

### How an object is matched

`gitops.Objects` reads the objects a file declares, from one JSON object, a
JSON array of objects, or YAML documents separated by `---`. Each object is
named by its API group, its kind, which is the object's Kubernetes type such
as `Deployment`, its namespace and its name. The group comes from
`apiVersion`. `Check` finds an object in the report only when all four match.

A document that names neither `apiVersion` nor `kind` declares no object, so
a workflow file among the block's paths is passed over. A document that names
only one of them, or no `metadata.name`, fails the read with an error that
names the document.

A manifest that names no namespace stands for a cluster-scoped object, such
as a Namespace, which the report lists with a `null` namespace. Argo CD lists
a namespaced object under the namespace it applied it in, even when the
manifest leaves that to the Application. Write `metadata.namespace` on every
namespaced object, since the report carries no Application namespace to match
against. When a manifest leaves it out, the pending `Detail` names the
namespace Argo CD applied the object in.

### What Argo CD's notification decides

A notification only prompts a fresh look, and it decides the verdict in one
case alone. That case is a failed sync at a revision holding the block's
files while one of the block's objects is not `Synced`, as the table above
shows.

When Argo CD's newest notification is at an earlier revision, or it has sent
none, `Check` judges from the node report all the same. `Detail` then ends by
naming that notification, or by saying that none exists. This is the case of
a commit that lands while the Application already reads `Degraded`.

The `Outcome` also carries these fields:

- `Revision` is the block's own commit, or the revision handed to `Check` when
  Argo CD notified exactly that one.
- `Judged` is the revision the counting report shows, and is empty until a
  report counts.
- `Notification` is the newest notification at a revision holding the block's
  files, and the zero value when there is none.

The code is `Check` in `capabilities/github/gitops/verdict.go`, `Objects` in
`capabilities/github/gitops/objects.go`, and
`observe.NodeReported.ArgoCDApplication` in `observe/node_reported.go`.

## How a deploy works

A block's `EnsureResources`, the handler method that creates or updates its
resources, ships its whole desired state with `gitops.Deploy` before it
reports the resources it rendered. `Deploy` takes
the block's `gitops.Deployment` and a `gitops.BlockDeploy` holding every file,
the commit message, the request's secret values and the poll interval. It
commits every file once, sets the deployment's `Paths` to those files, and
waits on that revision.

When nothing changed, `Deploy` waits on the block's own last commit, the
newest one that changed one of its files. The `gitops.Deployed` it returns
carries the revision, whether it committed, and the verdict. A failed deploy
comes back in `Deployed.Outcome` for the block to refuse:

```go
func (b *block) deploy(ctx context.Context, d contract.Desired, deployment gitops.Deployment) error {
	out, err := gitops.Deploy(ctx, deployment, gitops.BlockDeploy{
		Files:   render(d),
		Message: "deploy " + d.AppID,
		Secrets: gitops.SecretValues(d),
		Poll:    5 * time.Second,
	})
	if err != nil {
		return err
	}
	if out.Outcome.Verdict == gitops.VerdictFailed {
		return handler.Refuse("deploy-failed", "the deploy of %s failed: %s", out.Revision, out.Outcome.Detail)
	}
	return nil
}
```

The block's `Delete`, the handler method that removes its resources, ships
the removal the same way, handing `Deploy` each rendered path with no content.
Paths already gone commit nothing. `Deploy` then reads back from head, the
repository's newest commit, at most 50 commits:

- When it reaches the repository's first commit and no commit it read changed
  one of the block's files, the removal has nothing to remove, and `Deploy`
  returns a succeeded verdict at once.
- When a commit it read changed one of them, `Deploy` waits for the verdict on
  the newest such commit, which succeeds once none of the objects it removed is
  still applied.
- Otherwise none of the 50 commits changed the files and the history goes on
  past them, so `Deploy` cannot tell whether an older commit held them. The
  files may have left the repository longer ago, or never been there in a
  longer history, as in a shared repository whose block never deployed. The
  verdict then stays pending until the context ends.

## Reverting a failed deploy

When a revision fails, `gitops.Revert` puts the block's files back as they
stood at the last revision Argo CD reported healthy. Argo CD on the cluster
posts a notification of each sync and health change to the platform, which
keeps it for the block to read through the same `gitops.Notifications` source
`Check` reads.

You hand `Revert` a `gitops.BlockRevert` holding four things:

- `App`, the Argo CD Application the notifications name.
- `Failed`, the failed revision, as `Deployed.Revision` or a validate's check
  of the recorded revision gives it.
- `Owned`, a function naming every path the block owns at a revision, such as
  the files its render writes and any it wrote before and has since dropped.
- `Message`, the revert commit's message.

The revert leaves every other path alone, and it brings back a file the block
owned at the healthy revision but no longer renders:

```go
reverted, err := gitops.Revert(ctx, b.repo, b.notes, gitops.BlockRevert{
	App:    "app-" + d.AppID,
	Failed: failed,
	Owned: func(ctx context.Context, revision string) ([]string, error) {
		return ownedPaths(ctx, b.repo, revision)
	},
	Message: "revert the failed deploy " + failed,
})
```

`Reverted.Revision` is the revert commit, or head itself when the block's files
already stood as they did at the healthy revision. `Reverted.Healthy` names the
healthy revision, and is empty when the revert removed the block's files.

The revert walks back from the failed revision one parent at a time and reads
the newest notification of each. The first revision whose notification has
phase `Succeeded` and health `Healthy` is the last healthy one. Argo CD posts
such a notification when it syncs a revision and the whole Application turns
healthy. The walk reads notifications, which speak for the whole Application,
so it takes a revision as healthy only when the whole Application was. Every
owned file that a commit since then changed goes back to its content
there, in one commit. A deploy that landed two commits before the verdict is
therefore reverted whole.

The platform answers only the newest notification at a revision. A revision
that ran healthy and later turned degraded therefore reads as unhealthy, and
the walk passes it for an older healthy revision.

The block's first commit is the one whose parent holds none of the files the
block owns. When the walk reaches it without a healthy revision, the revert
removes the block's files only if the failed revision is that first commit,
since then no revision of the block came before it. An earlier revision of the
block may have run healthy before its newest notification read otherwise, or
had a healthy notification the platform has since pruned, so the revert would
remove a working app. In that case it fails and changes nothing. It fails the
same way when 50 commits back from the failed revision hold neither a healthy
revision nor the block's first commit.

The revert lands only when no commit after the failed revision changed a file
the block owns there or at head. A later deploy of the same block waits for its
own verdict, so the revert fails with `gitops.ErrHeadMoved` and changes
nothing. The block then refuses with the failed verdict and leaves the later
deploy to its own check. A later commit that changed only other paths lets the
revert land on top of it.

## Judging a commit in a repository other blocks share

Several blocks can commit to one GitOps repository. Argo CD compares only the
newest revision, so a sibling's later commit can be the only revision a report
or a notification ever names.

`Check` walks back from head to find the block's own commit, the newest at or
before the revision you hand it that changed one of `Paths`. A revision a block
recorded from a sibling's head therefore leads back to its own commit. A later
revision counts for the block when every commit between its own commit and
that revision left `Paths` alone. That revision then holds the block's files
as its own commit does. When a commit in between changed one of them, the
verdict stays pending, and `Detail` names that commit and the path.

The walk reads at most 50 commits back from head, through `Repository.Changed`.
`CommitManifests` uses the same walk to find the block's own last commit when
nothing differs. `LastChanged` gives you that commit from any revision, such as
when an observe has no recorded commit and must name the one head still
holds. The walk is `walkLineage` in `capabilities/github/gitops/repository.go`.

## Which notifications a block reads

The platform relays Argo CD's notifications and answers a block only those of
the cluster that met its cluster requirement, within the block's own org. That
requirement is the block's one requirement whose type's snapshot `kind` is
`kubernetes-cluster`, such as `hetzner.k3s-cluster`. A block whose requirement
binds no cluster, or several, reads none, so `Check` judges from the node
report alone and says that Argo CD has sent no notification. A cluster that
hosts apps of several orgs needs a scoping rule of its own, which the platform
does not provide. A notification looks like this:

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
and anything else is refused before a request goes out. A SHA read from
GitHub's answer must have the same form, so a call whose answer does not
decode or carries any other SHA fails and names the request it sent. File
content must be UTF-8, because the Git Data API carries it as a JSON string. A
commit whose parent is no longer the tip lands nothing, so `CommitManifests`
and `Revert` retry it on a fresh head a bounded number of times.

In tests, `gitopstest.NewRepository` and `gitopstest.NewNotifications` give
in-memory fakes of the repository and the notification source.
`gitopstest.NewNodeReports` gives a fake of the cluster's node reports.
`Report` adds a report of a cluster made at a given time, holding the
Applications you list. `AppliesHead` makes every read also answer a report
made a minute after the verb. That report shows the Application at the
repository's head, with each object head's files declare `Synced` and
`Healthy`, except the states you set:

```go
reports := gitopstest.NewNodeReports()
reports.AppliesHead("sub-cluster", "app-alpha", repo,
	gitopstest.Object{Object: gitops.Object{Kind: "ServiceAccount", Namespace: "app-alpha", Name: "app-alpha-runtime"}, Sync: "OutOfSync", Health: "Missing"})
```

// Package gitops commits a block's rendered manifests to its GitOps
// repository and judges the deploy from Argo CD's notification, which the
// platform relays to the block.
//
// CommitManifests commits a set of files on top of the repository's head,
// commits nothing when they already stand there, and retries a bounded
// number of times when another writer moves the tip. Revert restores the
// paths one revision changed. Judge, Check and Wait decide a deploy from the
// notification for its exact revision: phase Succeeded with health Healthy
// is a success, phase Error or Failed or health Degraded is a failure, and
// the sync status and images are never read. PlatformNotifications reads
// those notifications from the platform, and NotificationsFromContext builds
// it from the events callback the handler stashes on a request.
//
// The platform answers a block only the notifications of the cluster its
// kubernetes-cluster requirement binds, within the block's own org, and a
// block that binds no cluster or several reads none. A cluster that hosts
// apps of several orgs needs a scoping rule of its own, which the platform
// does not provide.
//
// CommitManifests refuses, before anything is written, a file carrying any
// secret value verbatim or as the standard base64 encoding of that value on
// its own, and SecretValues collects every secret a request carries for it
// to refuse. The refusal is a backstop that misses a secret encoded inside a
// larger value or in another encoding, so a block must never render secret
// material at all.
//
// Resource serves one declared resource through the kit's per-resource
// hooks, so the Handler drives each declared resource per call with that
// call's own Desired. Its ensure commits the rendered files and, when the
// recorded identity is of the render's own app, removes in the same commit
// every path that record names and the render no longer has. Only the
// resource's own ensure writes that record, and the app check keeps another
// app's record from removing anything. Its observe
// judges the app's newest notification by the files at that notification's
// revision, and its delete commits their removal.
//
// Observe works under one constraint: Argo CD may sync a later revision than
// the one committed, because it syncs whatever revision it resolves when it
// polls. In a repository several apps share, the notification for a change
// can then name a later commit, and none may name the committed one. Judging
// the newest notification by the files at its revision holds whether or not
// that happens. A live sync on a scratch cluster in which two commits land
// inside one poll interval would show which way Argo CD behaves; it has not
// been observed. Check and Wait keep the exact-revision rule.
//
// GitHubRepository implements Repository over GitHub's REST API and commits
// to the repository's main branch. NewGitHubRepository builds it from a
// request, taking the API base from the github instance through the host
// allowlist and the token from the github secret. A commit whose parent is
// no longer the tip lands nothing and wraps ErrStaleBase, so CommitManifests
// and Revert retry it on a fresh head. gitopstest provides in-memory fakes
// of Repository and Notifications for tests.
package gitops

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
// The package never writes a secret. CommitManifests refuses, before
// anything is written, a file carrying any secret value verbatim or as its
// standard base64 encoding, and SecretValues collects every secret a
// request carries for it to refuse.
//
// Resource serves one declared resource through the kit's per-resource
// hooks, so the Handler drives each declared resource per call with that
// call's own Desired. Its ensure commits the rendered files, its observe
// judges the app's newest notification by the files at that notification's
// revision, and its delete commits their removal.
//
// The package defines Repository but does not yet implement it over GitHub.
// A block supplies its own implementation, and gitopstest provides
// in-memory fakes of Repository and Notifications for tests.
package gitops

// Package gitops commits a block's rendered manifests to a GitOps
// repository, reverts its own commits, and judges a deploy from Argo CD's
// notification, serving one declared resource through the kit's
// per-resource hooks.
package gitops

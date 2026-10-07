// Package awsnames is the single source of the account-unique AWS names every
// mitstack app-infra engine creates (MIT-1694).
//
// The OpenTofu, CloudFormation and Crossplane blocks each build the same names
// from the app name — `mitstack-app-<app>` for the ECR repository, plus
// `-instance` and `-deploy` for the two IAM roles and the instance profile.
// They are account-unique by AWS's own rules, which is the fact that shapes
// everything around them: two engines cannot hold the same app at once, so a
// stack switch is necessarily destroy-then-create, and the destroy has to be
// PROVEN complete before the create starts.
//
// The names live here rather than in each engine because they are exactly the
// kind of thing that drifts. Each engine spells them in a different language —
// an HCL local, a CloudFormation `!Sub`, a Crossplane Format transform — so
// nothing but a shared constant makes a mismatch visible, and a mismatch is
// silent until an app is stranded between two stacks that disagree about which
// resources are the same resources.
//
// The package also gives a subscription's CloudFormation stack name.
package awsnames

import "strings"

// Prefix is the common stem: mitstack-app-<appName>.
func Prefix(appName string) string { return "mitstack-app-" + strings.TrimSpace(appName) }

// ECRRepository is the app's container registry. Deleting it destroys every
// image pushed to it, which is why a switch classifies it as an artifact store
// that is rebuilt rather than carried.
func ECRRepository(appName string) string { return Prefix(appName) }

// InstanceRole is the role the app's EC2 instance assumes.
func InstanceRole(appName string) string { return Prefix(appName) + "-instance" }

// InstanceProfile shares the instance role's name — an AWS instance profile is
// a separate account-unique resource, so it collides independently.
func InstanceProfile(appName string) string { return InstanceRole(appName) }

// DeployRole is the role managed-cd assumes to deploy into the app's account.
func DeployRole(appName string) string { return Prefix(appName) + "-deploy" }

// Colliding lists every account-unique name for the app, in a stable order.
//
// This is the set a stack switch waits on: while any of them exists, the target
// stack cannot create its own copy, and an engine that tried would either fail
// mid-apply or — worse, on the OpenTofu engine, whose self-repair imports
// adopt resources it finds by name — take ownership of another engine's
// infrastructure and destroy it on the next delete.
func Colliding(appName string) []string {
	return []string{
		ECRRepository(appName),
		InstanceRole(appName),
		DeployRole(appName),
	}
}

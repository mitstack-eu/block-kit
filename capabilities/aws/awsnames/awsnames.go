// Package awsnames, an optional helper for AWS, names the account-unique resources of an app's AWS infrastructure once, so every engine that creates them, and managed-cd that assumes the deploy role, agrees on each name.
package awsnames

import "strings"

const (
	stackPrefix     = "mitstack-"
	maxStackNameLen = 128
)

func Prefix(appName string) string { return "mitstack-app-" + strings.TrimSpace(appName) }

func ECRRepository(appName string) string { return Prefix(appName) }

func InstanceRole(appName string) string { return Prefix(appName) + "-instance" }

// InstanceProfile shares the instance role's name, since an instance profile is a separate account-unique resource.
func InstanceProfile(appName string) string { return InstanceRole(appName) }

func DeployRole(appName string) string { return Prefix(appName) + "-deploy" }

// Colliding lists, in a stable order, the names a stack switch waits on before the target stack creates its own.
func Colliding(appName string) []string {
	return []string{
		ECRRepository(appName),
		InstanceRole(appName),
		DeployRole(appName),
	}
}

func StackName(id string) string {
	sanitised := SanitiseCFNName(id)
	if sanitised == "" {
		sanitised = "sub"
	}
	name := stackPrefix + sanitised
	if len(name) > maxStackNameLen {
		name = name[:maxStackNameLen]
	}
	return name
}

func SanitiseCFNName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

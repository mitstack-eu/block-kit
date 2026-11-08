// Package awsnames, an optional helper for AWS, names the account-unique resources of an app's AWS infrastructure once, under a prefix the caller owns, so every engine that creates them and everything that assumes the deploy role agree on each name.
package awsnames

import (
	"errors"
	"strings"
)

const maxStackNameLen = 128

type Names struct{ base string }

func For(prefix, appName string) Names { return Names{base: prefix + strings.TrimSpace(appName)} }

func (n Names) Base() string { return n.base }

func (n Names) ECRRepository() string { return n.base }

func (n Names) InstanceRole() string { return n.base + "-instance" }

// InstanceProfile shares the instance role's name, since an instance profile is a separate account-unique resource.
func (n Names) InstanceProfile() string { return n.InstanceRole() }

func (n Names) DeployRole() string { return n.base + "-deploy" }

// Colliding lists, in a stable order, the names a stack switch waits on before the target stack creates its own.
func (n Names) Colliding() []string {
	return []string{n.ECRRepository(), n.InstanceRole(), n.DeployRole()}
}

// StackName refuses a prefix that does not start with an ASCII letter, since a CloudFormation stack name must.
func StackName(prefix, id string) (string, error) {
	if prefix == "" || !isASCIILetter(prefix[0]) {
		return "", errors.New("awsnames: a stack name prefix must start with an ASCII letter")
	}
	sanitised := SanitiseCFNName(id)
	if sanitised == "" {
		sanitised = "sub"
	}
	name := SanitiseCFNName(prefix) + sanitised
	if len(name) > maxStackNameLen {
		name = name[:maxStackNameLen]
	}
	return name, nil
}

func SanitiseCFNName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 128 && (isASCIILetter(byte(r)) || (r >= '0' && r <= '9')) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

func isASCIILetter(c byte) bool { return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') }

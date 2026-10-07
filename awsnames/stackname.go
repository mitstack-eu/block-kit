package awsnames

import "strings"

const (
	stackPrefix     = "mitstack-"
	maxStackNameLen = 128
)

// StackName is the CloudFormation stack name for id, prefixed to start with a letter and cut to 128 bytes.
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

// SanitiseCFNName replaces each rune outside [A-Za-z0-9] with one '-'.
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

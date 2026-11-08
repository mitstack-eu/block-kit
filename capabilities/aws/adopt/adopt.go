// Package adopt, an optional helper for AWS, decides whether a retried subscribe creates, adopts or refuses a resource, so it converges without taking over another app's resources.
package adopt

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	TagAppID          = "AppId"
	TagSubscriptionID = "SubscriptionId"
	TagManagedBy      = "ManagedBy"
)

const ManagedByValue = "mitstack"

// Identity matches on AppID alone, since a resubscribe brings a new subscription id for the same app and a name proves nothing.
type Identity struct {
	AppID          string
	SubscriptionID string
	Name           string
}

func (id Identity) Validate() error {
	if strings.TrimSpace(id.AppID) == "" {
		return errors.New("adopt: AppID is required — an identity without it matches every app's resources")
	}
	return nil
}

// Tags omits an unknown subscription id rather than writing it empty.
func (id Identity) Tags() map[string]string {
	t := map[string]string{
		TagAppID:     id.AppID,
		TagManagedBy: ManagedByValue,
	}
	if id.SubscriptionID != "" {
		t[TagSubscriptionID] = id.SubscriptionID
	}
	if id.Name != "" {
		t["Name"] = id.Name
	}
	return t
}

// Filter matches the AppId tag's value, never only its key, which every app's resources carry.
func (id Identity) Filter() string {
	return "Name=tag:" + TagAppID + ",Values=" + id.AppID
}

type Action int

// Refuse is the zero value, so a decision never made fails safe rather than adopting.
const (
	Refuse Action = iota
	Create
	Adopt
)

func (a Action) String() string {
	switch a {
	case Create:
		return "Create"
	case Adopt:
		return "Adopt"
	default:
		return "Refuse"
	}
}

// Candidate is a resource found in the account; its ResourceID reports and imports it and is never matched on.
type Candidate struct {
	ResourceID string
	Tags       map[string]string
}

type Decision struct {
	Action    Action
	Candidate Candidate
}

// Decide refuses whenever identity is not exact: another app's or an untagged candidate, one another system manages, or two that claim the app.
func Decide(id Identity, candidates []Candidate) (Decision, error) {
	if err := id.Validate(); err != nil {
		return Decision{Action: Refuse}, err
	}

	var matches []Candidate
	var conflicts []Candidate
	for _, c := range candidates {
		owner := c.Tags[TagAppID]
		switch {
		case owner == id.AppID:
			if mb, ok := c.Tags[TagManagedBy]; ok && mb != ManagedByValue {
				conflicts = append(conflicts, c)
				continue
			}
			matches = append(matches, c)
		default:
			conflicts = append(conflicts, c)
		}
	}

	if len(conflicts) > 0 {
		return Decision{Action: Refuse}, conflictError(id, conflicts)
	}

	switch len(matches) {
	case 0:
		return Decision{Action: Create}, nil
	case 1:
		return Decision{Action: Adopt, Candidate: matches[0]}, nil
	default:
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, m.ResourceID)
		}
		sort.Strings(ids)
		return Decision{Action: Refuse}, fmt.Errorf(
			"adopt: %d resources claim %s=%s (%s) — the invariant is already broken; adopting one would orphan the rest",
			len(matches), TagAppID, id.AppID, strings.Join(ids, ", "))
	}
}

func conflictError(id Identity, conflicts []Candidate) error {
	parts := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		owner := c.Tags[TagAppID]
		if owner == "" {
			owner = "<untagged>"
		}
		if mb, ok := c.Tags[TagManagedBy]; ok && mb != ManagedByValue {
			owner += " managed by " + mb
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", c.ResourceID, owner))
	}
	sort.Strings(parts)
	return fmt.Errorf(
		"adopt: refusing — %s does not exactly match %s=%s. A name or shape that merely looks right is not identity; adopting here would take over a resource that is not this app's",
		strings.Join(parts, ", "), TagAppID, id.AppID)
}

// Plan refuses the whole graph if any member refuses, since a partial adoption splits an app across two owners.
func Plan(id Identity, found map[string][]Candidate) (map[string]Decision, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}

	out := make(map[string]Decision, len(found))
	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		d, err := Decide(id, found[name])
		if err != nil {
			return nil, fmt.Errorf("adopt: %s: %w", name, err)
		}
		out[name] = d
	}
	return out, nil
}

// alreadyExistsPattern holds the AlreadyExists codes of the AWS services and the plain text an IaC engine prints under -no-color.
var alreadyExistsPattern = regexp.MustCompile(
	`(?i)(EntityAlreadyExists|BucketAlreadyOwnedByYou|BucketAlreadyExists|` +
		`RepositoryAlreadyExistsException|AlreadyExistsException|ResourceAlreadyExistsException|` +
		`InvalidGroup\.Duplicate|InvalidKeyPair\.Duplicate|InvalidPermission\.Duplicate|` +
		`already exists)`)

// IsAlreadyExists reports a collision with an existing resource, the signal to import rather than fail.
func IsAlreadyExists(output string) bool {
	if strings.TrimSpace(output) == "" {
		return false
	}
	return alreadyExistsPattern.MatchString(output)
}

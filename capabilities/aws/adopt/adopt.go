// Package adopt, an optional helper for AWS, decides whether a retried subscribe creates, adopts or refuses a resource by the kit's subscription tag, so it converges without taking over a resource that is not its own.
package adopt

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strings"

	"github.com/mitstack-eu/block-kit/observe"
)

type Tag struct {
	Key   string
	Value string
}

// Identity proves ownership by observe.AWSSubscriptionTag. Legacy is the tag an earlier release of the block wrote instead, which still proves ownership of a resource carrying no subscription tag until the block's next write adds it.
type Identity struct {
	Subscription string
	Legacy       Tag
}

func (id Identity) Validate() error {
	if strings.TrimSpace(id.Subscription) == "" {
		return errors.New("adopt: Subscription is required, since an identity without it matches every resource")
	}
	if id.Legacy != (Tag{}) && (strings.TrimSpace(id.Legacy.Key) == "" || strings.TrimSpace(id.Legacy.Value) == "") {
		return errors.New("adopt: a legacy tag needs both a key and a value, since a key alone matches every resource that carries it")
	}
	if id.Legacy.Key == observe.AWSSubscriptionTag {
		return fmt.Errorf("adopt: the legacy tag cannot be %s, which already proves ownership", observe.AWSSubscriptionTag)
	}
	return nil
}

func (id Identity) Tags() map[string]string {
	return map[string]string{observe.AWSSubscriptionTag: id.Subscription}
}

// Lookups lists the tag values a caller searches by, each matching a value and never only a key, and unions the results into the candidates.
func (id Identity) Lookups() []Tag {
	out := []Tag{{Key: observe.AWSSubscriptionTag, Value: id.Subscription}}
	if id.Legacy.Key != "" {
		out = append(out, id.Legacy)
	}
	return out
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

// Decision's Retag reports an adopted resource that only its legacy tag names, so the caller writes Identity.Tags onto it on its next write.
type Decision struct {
	Action    Action
	Candidate Candidate
	Retag     bool
}

// Decide refuses whenever identity is not exact: another subscription's or an untagged candidate, or two that claim the subscription.
func Decide(id Identity, candidates []Candidate) (Decision, error) {
	if err := id.Validate(); err != nil {
		return Decision{Action: Refuse}, err
	}

	var matches, conflicts []Candidate
	seen := map[string]map[string]string{}
	for _, c := range candidates {
		if c.ResourceID == "" {
			return Decision{Action: Refuse}, errors.New("adopt: refusing a candidate with no resource id, which can be neither imported nor reported")
		}
		if tags, ok := seen[c.ResourceID]; ok {
			if !maps.Equal(tags, c.Tags) {
				return Decision{Action: Refuse}, fmt.Errorf("adopt: refusing %s, since two views of it disagree on its tags", c.ResourceID)
			}
			continue
		}
		seen[c.ResourceID] = c.Tags
		if owner, ok := c.Tags[observe.AWSSubscriptionTag]; ok {
			if owner == id.Subscription {
				matches = append(matches, c)
			} else {
				conflicts = append(conflicts, c)
			}
			continue
		}
		if id.Legacy.Key != "" && c.Tags[id.Legacy.Key] == id.Legacy.Value {
			matches = append(matches, c)
			continue
		}
		conflicts = append(conflicts, c)
	}

	if len(conflicts) > 0 {
		return Decision{Action: Refuse}, conflictError(id, conflicts)
	}

	switch len(matches) {
	case 0:
		return Decision{Action: Create}, nil
	case 1:
		_, tagged := matches[0].Tags[observe.AWSSubscriptionTag]
		return Decision{Action: Adopt, Candidate: matches[0], Retag: !tagged}, nil
	default:
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, m.ResourceID)
		}
		sort.Strings(ids)
		return Decision{Action: Refuse}, fmt.Errorf(
			"adopt: %d resources claim subscription %s (%s), so adopting one would orphan the rest",
			len(matches), id.Subscription, strings.Join(ids, ", "))
	}
}

func conflictError(id Identity, conflicts []Candidate) error {
	parts := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		owner, ok := c.Tags[observe.AWSSubscriptionTag]
		switch {
		case ok:
			owner = "subscription " + owner
		case id.Legacy.Key != "" && c.Tags[id.Legacy.Key] != "":
			owner = id.Legacy.Key + "=" + c.Tags[id.Legacy.Key]
		default:
			owner = "<untagged>"
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", c.ResourceID, owner))
	}
	sort.Strings(parts)
	return fmt.Errorf(
		"adopt: refusing, since %s is not subscription %s's. A name or shape that merely looks right is not identity, and adopting it would take over a resource that is not this subscription's",
		strings.Join(parts, ", "), id.Subscription)
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

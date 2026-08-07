// Package adopt is the idempotency contract for a provisioning operator
// (MIT-1468).
//
// The single hardest thing about provisioning is that subscribe is RETRIED. Not
// might be — is. A network blip, a redeploy, a consumer clicking twice, an engine
// retry after a 502: your subscribe will run again over a half-built resource
// set. A naive operator then either duplicates resources or, far worse, adopts
// one that belongs to somebody else.
//
// aws-app-infra learned this in stages, each a live bug:
//
//   - T-501  — a retried subscribe must CONVERGE, not duplicate: import on
//     AlreadyExists.
//   - #478   — AlreadyExists must be recognised in plain `-no-color` engine
//     output, not only in structured errors. The first fix matched only the
//     structured form and missed the text the engine actually emits.
//   - T-585  — reuse the existing box by tag-based import rather than creating a
//     second EC2 beside it.
//   - MIT-1224 — recovery must adopt the drifted SATELLITES too (the SSM
//     edge-config document, the DLM role), or the app self-heals halfway.
//   - MIT-1229 — adoption must be AppId-EXACT. A tag match on the app NAME is
//     not identity. This is the dangerous one: match loosely and one consumer's
//     app takes over another's live resource.
//
// The API is shaped so the dangerous mistake is hard to make. Identity carries
// an immutable id, never a display name; Filter() emits a tag-VALUE filter, never
// a tag-key one; and the zero Action is Refuse, so a code path that forgets to
// decide fails safe.
package adopt

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The canonical identity tag set. Every resource an operator provisions carries
// these, and adoption matches on them.
const (
	// TagAppID is the immutable app id. This is IDENTITY. Matching on anything
	// else — a Name, a slug, a prefix — is MIT-1229.
	TagAppID = "AppId"
	// TagSubscriptionID records which subscription provisioned the resource.
	TagSubscriptionID = "SubscriptionId"
	// TagManagedBy marks the resource as mitstack-managed, so a resource another
	// system owns is never adopted even if its ids happen to line up.
	TagManagedBy = "ManagedBy"
)

// ManagedByValue is the TagManagedBy value mitstack writes.
const ManagedByValue = "mitstack"

// Identity is what a resource must match to be adopted.
type Identity struct {
	// AppID is the immutable app id — the only thing that constitutes identity.
	AppID string
	// SubscriptionID is recorded when known. It is written as a tag but is NOT
	// part of the match: a resubscribe legitimately produces a new subscription
	// id for the same app.
	SubscriptionID string
	// Name is a human-chosen display name. It is carried for tagging and
	// diagnostics and is deliberately NEVER used to match — see MIT-1229.
	Name string
}

// Validate rejects an identity that could not distinguish one app from another.
func (id Identity) Validate() error {
	if strings.TrimSpace(id.AppID) == "" {
		return errors.New("adopt: AppID is required — an identity without it matches every app's resources")
	}
	return nil
}

// Tags returns the tag set to write onto a provisioned resource. An unknown
// subscription id is OMITTED rather than written empty: AWS filters match on
// value, so an empty tag would make every app's resources look alike.
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

// Filter returns an AWS describe filter scoped to this identity.
//
// It is a tag-VALUE filter (`Name=tag:AppId,Values=<id>`) and never a tag-KEY
// filter (`Name=tag-key,Values=AppId`). The difference is the whole of MIT-1229:
// a tag-key filter matches every app's resources, which reads as a working
// discovery right up until it adopts or deletes somebody else's.
func (id Identity) Filter() string {
	return "Name=tag:" + TagAppID + ",Values=" + id.AppID
}

// Action is what to do about a candidate resource.
type Action int

const (
	// Refuse is the ZERO VALUE on purpose: a decision that was never made must
	// fail safe, not silently adopt.
	Refuse Action = iota
	// Create — nothing exists; provision it.
	Create
	// Adopt — an exact-identity match exists; import it rather than duplicating.
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

// Candidate is a resource discovered in the account that might be ours.
type Candidate struct {
	// ResourceID is the provider's own id (i-…, sg-…, an ARN). Used for
	// reporting and for the import call — never for matching.
	ResourceID string
	// Tags as read back from the provider.
	Tags map[string]string
}

// Decision is the outcome for one resource.
type Decision struct {
	Action    Action
	Candidate Candidate
}

// Decide chooses between creating, adopting and refusing.
//
// It refuses whenever identity is not EXACT — an untagged candidate, one owned
// by another app, one another system manages, or two candidates claiming the
// same identity. Refusing is not a failure of the algorithm; it is the correct
// answer, because every alternative silently damages something.
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
		"adopt: refusing — %s does not exactly match %s=%s. A name or shape that merely looks right is not identity (MIT-1229); adopting here would take over a resource that is not this app's",
		strings.Join(parts, ", "), TagAppID, id.AppID)
}

// Plan decides for a whole resource graph at once — the primary resource plus
// every satellite.
//
// If ANY member refuses, the whole plan refuses. A partial adoption is worse
// than none: it half-rewires an app across two owners, which is how MIT-1224's
// app came back self-healed halfway and still broken.
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

// alreadyExistsPattern matches the AlreadyExists family across AWS services AND
// the plain `-no-color` text an IaC engine prints. Both forms are needed: the
// first fix for T-501 matched only the structured error, and the plain form is
// what the engine actually emits (#478).
var alreadyExistsPattern = regexp.MustCompile(
	`(?i)(EntityAlreadyExists|BucketAlreadyOwnedByYou|BucketAlreadyExists|` +
		`RepositoryAlreadyExistsException|AlreadyExistsException|ResourceAlreadyExistsException|` +
		`InvalidGroup\.Duplicate|InvalidKeyPair\.Duplicate|InvalidPermission\.Duplicate|` +
		`already exists)`)

// IsAlreadyExists reports whether output describes a collision with a resource
// that is already there — the signal to IMPORT rather than fail.
func IsAlreadyExists(output string) bool {
	if strings.TrimSpace(output) == "" {
		return false
	}
	return alreadyExistsPattern.MatchString(output)
}

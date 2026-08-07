package adopt_test

import (
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/adopt"
)

// --- Identity ---------------------------------------------------------------

func TestIdentity_TagsCarryTheAppIdAndOwner(t *testing.T) {
	tags := adopt.Identity{AppID: "app-42"}.Tags()

	if tags[adopt.TagAppID] != "app-42" {
		t.Errorf("Tags()[%s] = %q, want app-42", adopt.TagAppID, tags[adopt.TagAppID])
	}
	if tags[adopt.TagManagedBy] != adopt.ManagedByValue {
		t.Errorf("Tags()[%s] = %q, want %q", adopt.TagManagedBy, tags[adopt.TagManagedBy], adopt.ManagedByValue)
	}
}

func TestIdentity_SubscriptionIDIsIncludedWhenKnown(t *testing.T) {
	tags := adopt.Identity{AppID: "app-42", SubscriptionID: "sub-7"}.Tags()
	if tags[adopt.TagSubscriptionID] != "sub-7" {
		t.Errorf("Tags()[%s] = %q, want sub-7", adopt.TagSubscriptionID, tags[adopt.TagSubscriptionID])
	}
}

func TestIdentity_OmitsSubscriptionTagWhenUnknown(t *testing.T) {
	// An empty tag value is not the same as an absent tag: AWS filters match on
	// value, so an empty one would make every app's resource look alike.
	tags := (adopt.Identity{AppID: "app-42"}).Tags()
	if _, ok := tags[adopt.TagSubscriptionID]; ok {
		t.Error("an unknown subscription id must be omitted, not written as an empty tag")
	}
}

func TestIdentity_RejectsAnEmptyAppID(t *testing.T) {
	if err := (adopt.Identity{}).Validate(); err == nil {
		t.Error("an identity with no app id must not validate — it would match every resource")
	}
}

func TestIdentity_FilterMatchesOnValueNotTagKey(t *testing.T) {
	// MIT-1229's core lesson in filter form. A tag-KEY filter ("has an AppId")
	// matches every app's resources; only a tag-VALUE filter is identity.
	f := adopt.Identity{AppID: "app-42"}.Filter()

	if !strings.Contains(f, "tag:"+adopt.TagAppID) || !strings.Contains(f, "app-42") {
		t.Errorf("Filter() = %q, want a tag:%s=app-42 value filter", f, adopt.TagAppID)
	}
	if strings.Contains(f, "tag-key") {
		t.Errorf("Filter() = %q, must never be a tag-KEY filter — that matches every app", f)
	}
}

// --- Decide -----------------------------------------------------------------

func candidate(appID string) adopt.Candidate {
	return adopt.Candidate{ResourceID: "i-abc", Tags: map[string]string{
		adopt.TagAppID:     appID,
		adopt.TagManagedBy: adopt.ManagedByValue,
	}}
}

func TestDecide_AdoptsAnExactAppIdMatch(t *testing.T) {
	d, err := adopt.Decide(adopt.Identity{AppID: "app-42"}, []adopt.Candidate{candidate("app-42")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Action != adopt.Adopt {
		t.Errorf("Action = %v, want Adopt", d.Action)
	}
	if d.Candidate.ResourceID != "i-abc" {
		t.Errorf("Candidate = %+v", d.Candidate)
	}
}

func TestDecide_CreatesWhenNothingExists(t *testing.T) {
	d, err := adopt.Decide(adopt.Identity{AppID: "app-42"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Action != adopt.Create {
		t.Errorf("Action = %v, want Create", d.Action)
	}
}

func TestDecide_RefusesAResourceBelongingToAnotherApp(t *testing.T) {
	// MIT-1229. This is the dangerous one: adopting here takes over another
	// consumer's live resource.
	d, err := adopt.Decide(adopt.Identity{AppID: "app-42"}, []adopt.Candidate{candidate("app-99")})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if d.Action != adopt.Refuse {
		t.Errorf("Action = %v, want Refuse", d.Action)
	}
	if !strings.Contains(err.Error(), "app-99") {
		t.Errorf("refusal must name the conflicting owner, got %v", err)
	}
}

func TestDecide_RefusesACandidateWithNoAppIdTag(t *testing.T) {
	// An untagged resource has no identity. "It looks like ours" is not identity.
	c := adopt.Candidate{ResourceID: "i-untagged", Tags: map[string]string{}}
	d, err := adopt.Decide(adopt.Identity{AppID: "app-42"}, []adopt.Candidate{c})
	if err == nil {
		t.Fatal("expected a refusal for an untagged candidate")
	}
	if d.Action != adopt.Refuse {
		t.Errorf("Action = %v, want Refuse", d.Action)
	}
}

func TestDecide_RefusesWhenTwoCandidatesMatch(t *testing.T) {
	// Two resources carrying the same AppId means the invariant is already
	// broken. Picking one at random would silently orphan the other.
	a, b := candidate("app-42"), candidate("app-42")
	b.ResourceID = "i-def"

	d, err := adopt.Decide(adopt.Identity{AppID: "app-42"}, []adopt.Candidate{a, b})
	if err == nil {
		t.Fatal("expected a refusal for an ambiguous match")
	}
	if d.Action != adopt.Refuse {
		t.Errorf("Action = %v, want Refuse", d.Action)
	}
	if !strings.Contains(err.Error(), "i-abc") || !strings.Contains(err.Error(), "i-def") {
		t.Errorf("refusal must name both candidates so the operator can resolve it, got %v", err)
	}
}

func TestDecide_RefusesWhenTheNameMatchesButTheIdDiffers(t *testing.T) {
	// The exact shape of MIT-1229 and of the MIT-1126 exit-sweep bug: matching
	// on a human-chosen name instead of the immutable id.
	c := adopt.Candidate{ResourceID: "i-abc", Tags: map[string]string{
		"Name":             "myapp",
		adopt.TagAppID:     "app-99",
		adopt.TagManagedBy: adopt.ManagedByValue,
	}}
	d, err := adopt.Decide(adopt.Identity{AppID: "app-42", Name: "myapp"}, []adopt.Candidate{c})
	if err == nil {
		t.Fatal("a matching Name must never override a differing AppId")
	}
	if d.Action != adopt.Refuse {
		t.Errorf("Action = %v, want Refuse", d.Action)
	}
}

func TestDecide_RefusesACandidateManagedByAnotherSystem(t *testing.T) {
	c := candidate("app-42")
	c.Tags[adopt.TagManagedBy] = "some-other-platform"

	d, err := adopt.Decide(adopt.Identity{AppID: "app-42"}, []adopt.Candidate{c})
	if err == nil {
		t.Fatal("expected a refusal for a resource another system manages")
	}
	if d.Action != adopt.Refuse {
		t.Errorf("Action = %v, want Refuse", d.Action)
	}
}

func TestDecide_RefusesOnAnInvalidIdentity(t *testing.T) {
	if _, err := adopt.Decide(adopt.Identity{}, []adopt.Candidate{candidate("app-42")}); err == nil {
		t.Error("an identity with no app id must refuse, not match everything")
	}
}

func TestDecide_DefaultIsRefuseNotAdopt(t *testing.T) {
	// The zero Action value must not be Adopt: a future code path that forgets
	// to set it should fail safe.
	var zero adopt.Action
	if zero == adopt.Adopt {
		t.Error("the zero Action must not be Adopt — an unset decision has to fail safe")
	}
	if zero != adopt.Refuse {
		t.Errorf("the zero Action = %v, want Refuse", zero)
	}
}

// --- AlreadyExists classification -------------------------------------------

func TestIsAlreadyExists_RecognisesStructuredAWSCodes(t *testing.T) {
	for _, msg := range []string{
		"EntityAlreadyExists: Role with name mitstack-app already exists",
		"InvalidGroup.Duplicate: The security group 'sg-x' already exists",
		"BucketAlreadyOwnedByYou: Your previous request to create the named bucket succeeded",
		"RepositoryAlreadyExistsException: The repository named x already exists",
		"AlreadyExistsException: Stack [x] already exists",
	} {
		if !adopt.IsAlreadyExists(msg) {
			t.Errorf("IsAlreadyExists(%q) = false, want true", msg)
		}
	}
}

func TestIsAlreadyExists_RecognisesPlainNoColorEngineOutput(t *testing.T) {
	// #478: the collision handler matched only structured errors, and missed
	// the plain `-no-color` text the engine actually emits.
	plain := `Error: creating IAM Role (mitstack-app): operation error IAM: CreateRole,
	https response error StatusCode: 409, EntityAlreadyExists: Role with name
	mitstack-app already exists.`
	if !adopt.IsAlreadyExists(plain) {
		t.Error("IsAlreadyExists must match plain -no-color engine output (#478)")
	}
}

func TestIsAlreadyExists_IsNotFooledByUnrelatedText(t *testing.T) {
	for _, msg := range []string{
		"AccessDenied: not authorized to perform iam:CreateRole",
		"the resource does not exist",
		"",
	} {
		if adopt.IsAlreadyExists(msg) {
			t.Errorf("IsAlreadyExists(%q) = true, want false", msg)
		}
	}
}

// --- Satellites (MIT-1224) --------------------------------------------------

func TestPlan_AdoptsEveryDriftedSatelliteNotJustThePrimary(t *testing.T) {
	// MIT-1224: recovery that adopts the instance but not the SSM document and
	// DLM role leaves the app self-healed halfway, which reads as "still broken".
	id := adopt.Identity{AppID: "app-42"}
	found := map[string][]adopt.Candidate{
		"instance":     {candidate("app-42")},
		"ssm-document": {candidate("app-42")},
		"dlm-role":     nil, // genuinely absent -> create
	}

	plan, err := adopt.Plan(id, found)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan["instance"].Action != adopt.Adopt || plan["ssm-document"].Action != adopt.Adopt {
		t.Errorf("plan = %+v, want every present satellite adopted", plan)
	}
	if plan["dlm-role"].Action != adopt.Create {
		t.Errorf("plan[dlm-role].Action = %v, want Create", plan["dlm-role"].Action)
	}
}

func TestPlan_OneForeignSatelliteRefusesTheWholePlan(t *testing.T) {
	// A partial adoption is worse than none: it half-rewires an app across two
	// owners. If any member refuses, the whole plan refuses.
	id := adopt.Identity{AppID: "app-42"}
	found := map[string][]adopt.Candidate{
		"instance":     {candidate("app-42")},
		"ssm-document": {candidate("app-99")},
	}

	_, err := adopt.Plan(id, found)
	if err == nil {
		t.Fatal("a foreign satellite must refuse the entire plan, not just its own member")
	}
	if !strings.Contains(err.Error(), "ssm-document") {
		t.Errorf("the refusal must name the offending member, got %v", err)
	}
}

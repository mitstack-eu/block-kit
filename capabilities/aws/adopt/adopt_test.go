package adopt_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/aws/adopt"
	"github.com/mitstack-eu/block-kit/observe"
)

var legacy = adopt.Tag{Key: "Owner", Value: "app-42"}

func owned(id, subscription string) adopt.Candidate {
	return adopt.Candidate{ResourceID: id, Tags: map[string]string{observe.AWSSubscriptionTag: subscription}}
}

func tagged(id string, tags map[string]string) adopt.Candidate {
	return adopt.Candidate{ResourceID: id, Tags: tags}
}

func TestIdentity_TagsAreTheKitsSubscriptionTagAlone(t *testing.T) {
	got := adopt.Identity{Subscription: "sub-7", Legacy: legacy}.Tags()

	if len(got) != 1 || got[observe.AWSSubscriptionTag] != "sub-7" {
		t.Errorf("Tags() = %v, want only %s=sub-7, the tag observe and kittest read", got, observe.AWSSubscriptionTag)
	}
}

func TestIdentity_RefusesAnIdentityThatWouldMatchTooMuch(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   adopt.Identity
	}{
		{"no subscription", adopt.Identity{}},
		{"a blank subscription", adopt.Identity{Subscription: "  "}},
		{"a legacy tag with no value", adopt.Identity{Subscription: "sub-7", Legacy: adopt.Tag{Key: "Owner"}}},
		{"a legacy tag with no key", adopt.Identity{Subscription: "sub-7", Legacy: adopt.Tag{Value: "app-42"}}},
		{"a legacy tag that is the subscription tag", adopt.Identity{Subscription: "sub-7", Legacy: adopt.Tag{Key: observe.AWSSubscriptionTag, Value: "sub-1"}}},
	} {
		if err := tc.id.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want a refusal", tc.name)
		}
	}
	if err := (adopt.Identity{Subscription: "sub-7"}).Validate(); err != nil {
		t.Errorf("a subscription alone: Validate() = %v, want nil", err)
	}
}

func TestIdentity_LooksUpByTheSubscriptionTagThenTheLegacyTag(t *testing.T) {
	std := adopt.Tag{Key: observe.AWSSubscriptionTag, Value: "sub-7"}

	if got := (adopt.Identity{Subscription: "sub-7"}).Lookups(); !slices.Equal(got, []adopt.Tag{std}) {
		t.Errorf("Lookups() = %v, want only %v", got, std)
	}
	if got := (adopt.Identity{Subscription: "sub-7", Legacy: legacy}).Lookups(); !slices.Equal(got, []adopt.Tag{std, legacy}) {
		t.Errorf("Lookups() with a legacy tag = %v, want %v then %v", got, std, legacy)
	}
}

func TestDecide(t *testing.T) {
	plain, migrating := adopt.Identity{Subscription: "sub-7"}, adopt.Identity{Subscription: "sub-7", Legacy: legacy}
	for _, tc := range []struct {
		name       string
		id         adopt.Identity
		candidates []adopt.Candidate
		want       adopt.Action
		wantRetag  bool
		wantInErr  []string
	}{
		{"nothing found is created", plain, nil, adopt.Create, false, nil},
		{"the one resource carrying the subscription is adopted", plain, []adopt.Candidate{owned("i-abc", "sub-7")}, adopt.Adopt, false, nil},
		{"one resource found by both lookups is adopted once", migrating,
			[]adopt.Candidate{tagged("i-abc", map[string]string{observe.AWSSubscriptionTag: "sub-7", "Owner": "app-42"}), tagged("i-abc", map[string]string{observe.AWSSubscriptionTag: "sub-7", "Owner": "app-42"})},
			adopt.Adopt, false, nil},
		{"a resource only the legacy tag names is adopted and retagged", migrating, []adopt.Candidate{tagged("i-old", map[string]string{"Owner": "app-42"})}, adopt.Adopt, true, nil},
		{"without a legacy tag, a resource only a legacy tag names is refused", plain, []adopt.Candidate{tagged("i-old", map[string]string{"Owner": "app-42"})}, adopt.Refuse, false, []string{"i-old", "<untagged>"}},
		{"a resource of another subscription is refused", plain, []adopt.Candidate{owned("i-abc", "sub-9")}, adopt.Refuse, false, []string{"i-abc", "sub-9"}},
		{"another subscription's resource is refused even when the legacy tag matches", migrating,
			[]adopt.Candidate{tagged("i-abc", map[string]string{observe.AWSSubscriptionTag: "sub-9", "Owner": "app-42"})}, adopt.Refuse, false, []string{"sub-9"}},
		{"a legacy tag naming another owner is refused", migrating, []adopt.Candidate{tagged("i-abc", map[string]string{"Owner": "app-99"})}, adopt.Refuse, false, []string{"i-abc"}},
		{"an untagged resource is refused", plain, []adopt.Candidate{tagged("i-bare", map[string]string{})}, adopt.Refuse, false, []string{"<untagged>"}},
		{"a matching Name proves nothing", plain, []adopt.Candidate{tagged("i-abc", map[string]string{"Name": "myapp"})}, adopt.Refuse, false, nil},
		{"two resources claiming the subscription are refused", migrating,
			[]adopt.Candidate{owned("i-abc", "sub-7"), tagged("i-def", map[string]string{"Owner": "app-42"})}, adopt.Refuse, false, []string{"i-abc", "i-def"}},
		{"an invalid identity refuses", adopt.Identity{}, []adopt.Candidate{owned("i-abc", "")}, adopt.Refuse, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := adopt.Decide(tc.id, tc.candidates)

			if d.Action != tc.want || d.Retag != tc.wantRetag {
				t.Fatalf("Decide = %v (retag %v), %v; want %v (retag %v)", d.Action, d.Retag, err, tc.want, tc.wantRetag)
			}
			if (tc.want == adopt.Refuse) != (err != nil) {
				t.Fatalf("Decide error = %v; a refusal, and only a refusal, says why", err)
			}
			for _, s := range tc.wantInErr {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("refusal %q does not name %q", err, s)
				}
			}
			if tc.want == adopt.Adopt && d.Candidate.ResourceID == "" {
				t.Errorf("an adoption names no resource: %+v", d)
			}
		})
	}
}

func TestDecide_DefaultIsRefuseNotAdopt(t *testing.T) {
	var zero adopt.Action
	if zero != adopt.Refuse {
		t.Errorf("the zero Action = %v, want Refuse, so an unset decision fails safe", zero)
	}
}

func TestPlan_AdoptsEveryPresentMemberAndSaysWhichToRetag(t *testing.T) {
	id := adopt.Identity{Subscription: "sub-7", Legacy: legacy}
	found := map[string][]adopt.Candidate{
		"instance":     {owned("i-abc", "sub-7")},
		"ssm-document": {tagged("doc-1", map[string]string{"Owner": "app-42"})},
		"dlm-role":     nil,
	}

	plan, err := adopt.Plan(id, found)

	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan["instance"].Action != adopt.Adopt || plan["instance"].Retag {
		t.Errorf("plan[instance] = %+v, want adopted with nothing to retag", plan["instance"])
	}
	if plan["ssm-document"].Action != adopt.Adopt || !plan["ssm-document"].Retag {
		t.Errorf("plan[ssm-document] = %+v, want adopted and retagged", plan["ssm-document"])
	}
	if plan["dlm-role"].Action != adopt.Create {
		t.Errorf("plan[dlm-role] = %+v, want Create", plan["dlm-role"])
	}
}

func TestPlan_OneForeignMemberRefusesTheWholePlan(t *testing.T) {
	found := map[string][]adopt.Candidate{
		"instance":     {owned("i-abc", "sub-7")},
		"ssm-document": {owned("doc-1", "sub-9")},
	}

	_, err := adopt.Plan(adopt.Identity{Subscription: "sub-7"}, found)

	if err == nil || !strings.Contains(err.Error(), "ssm-document") {
		t.Errorf("Plan error = %v, want a refusal naming ssm-document", err)
	}
}

func TestIsAlreadyExists(t *testing.T) {
	for _, tc := range []struct {
		msg  string
		want bool
	}{
		{"EntityAlreadyExists: Role with name acme-app already exists", true},
		{"InvalidGroup.Duplicate: The security group 'sg-x' already exists", true},
		{"BucketAlreadyOwnedByYou: Your previous request to create the named bucket succeeded", true},
		{"RepositoryAlreadyExistsException: The repository named x already exists", true},
		{"AlreadyExistsException: Stack [x] already exists", true},
		{"Error: creating IAM Role (acme-app): operation error IAM: CreateRole,\n\thttps response error StatusCode: 409, EntityAlreadyExists", true},
		{"AccessDenied: not authorized to perform iam:CreateRole", false},
		{"the resource does not exist", false},
		{"", false},
	} {
		if got := adopt.IsAlreadyExists(tc.msg); got != tc.want {
			t.Errorf("IsAlreadyExists(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}

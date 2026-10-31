package apphostname

import (
	"encoding/json"
	"slices"
	"testing"
)

const testHost = "shop.apps.mitstack.dev"

func TestChangeBatchJSON_QuotesATXTValueAndKeepsEveryAValueAsGiven(t *testing.T) {
	owner := OwnerText("sub_shop")
	changes := []Change{
		{Action: Create, Type: TypeA, Name: testHost, Values: []string{"203.0.113.10", "203.0.113.20"}, TTL: TTL},
		{Action: Delete, Type: TypeTXT, Name: testHost, Values: []string{owner}, TTL: TTL},
	}

	raw, err := changeBatchJSON(changes)

	if err != nil {
		t.Fatalf("changeBatchJSON: %v", err)
	}
	var batch struct {
		Changes []struct {
			Action            string
			ResourceRecordSet struct {
				Name            string
				Type            string
				TTL             int
				ResourceRecords []struct{ Value string }
			}
		}
	}
	if err := json.Unmarshal(raw, &batch); err != nil {
		t.Fatalf("the change batch %s is not JSON: %v", raw, err)
	}
	want := []struct {
		action, recordType string
		values             []struct{ Value string }
	}{
		{Create, TypeA, []struct{ Value string }{{"203.0.113.10"}, {"203.0.113.20"}}},
		{Delete, TypeTXT, []struct{ Value string }{{`"` + owner + `"`}}},
	}
	if len(batch.Changes) != len(want) {
		t.Fatalf("the change batch holds %d changes, want %d: %s", len(batch.Changes), len(want), raw)
	}
	for i, w := range want {
		got := batch.Changes[i]
		set := got.ResourceRecordSet
		if got.Action != w.action || set.Type != w.recordType || set.Name != testHost || set.TTL != TTL || !slices.Equal(set.ResourceRecords, w.values) {
			t.Errorf("change %d = %+v, want %s %s %v at %q with TTL %d", i, got, w.action, w.recordType, w.values, testHost, TTL)
		}
	}
}

func TestIsRecordConflict_RecognisesOnlyAChangeBatchRoute53RejectedOverTheRecordsItFound(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "a create of a record that exists",
			output: "An error occurred (InvalidChangeBatch) when calling the ChangeResourceRecordSets operation: [Tried to create resource record set [name='shop.apps.mitstack.dev.', type='A'] but it already exists]",
			want:   true,
		},
		{
			name:   "a delete of a record that is gone",
			output: "An error occurred (InvalidChangeBatch) when calling the ChangeResourceRecordSets operation: [Tried to delete resource record set [name='shop.apps.mitstack.dev.', type='TXT'] but it was not found]",
			want:   true,
		},
		{
			name:   "a delete whose value is not the record's",
			output: "An error occurred (InvalidChangeBatch) when calling the ChangeResourceRecordSets operation: [Tried to delete resource record set [name='shop.apps.mitstack.dev.', type='A'] but the values provided do not match the current values]",
			want:   true,
		},
		{
			name:   "an access denial",
			output: "An error occurred (AccessDenied) when calling the ChangeResourceRecordSets operation: User is not authorized to perform: route53:ChangeResourceRecordSets",
		},
		{
			name:   "a throttle",
			output: "An error occurred (Throttling) when calling the ChangeResourceRecordSets operation (reached max retries: 2): Rate exceeded",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRecordConflict(tc.output); got != tc.want {
				t.Errorf("isRecordConflict(%q) = %v, want %v", tc.output, got, tc.want)
			}
		})
	}
}

func TestRecordValues_ReadsTheRecordOfTheNameAndTypeAskedFromAListing(t *testing.T) {
	listing := []byte(`{"ResourceRecordSets":[
		{"Name":"shop.apps.mitstack.dev.","Type":"TXT","TTL":60,"ResourceRecords":[{"Value":"\"mitstack-subscription=sub_shop\""}]},
		{"Name":"shopfront.apps.mitstack.dev.","Type":"A","TTL":60,"ResourceRecords":[{"Value":"198.51.100.9"},{"Value":"198.51.100.1"}]}
	]}`)
	for _, tc := range []struct {
		name, host, recordType string
		want                   []string
	}{
		{name: "a TXT value is read without its quotes", host: testHost, recordType: TypeTXT, want: []string{OwnerText("sub_shop")}},
		{name: "a type the name lacks reads empty", host: testHost, recordType: TypeA},
		{name: "a record of another name reads empty", host: "shopfront.apps.mitstack.dev", recordType: TypeTXT},
		{name: "every A value is read as given and in the listing's order", host: "shopfront.apps.mitstack.dev", recordType: TypeA, want: []string{"198.51.100.9", "198.51.100.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := recordValues(listing, tc.host, tc.recordType)

			if err != nil {
				t.Fatalf("recordValues(%s, %s): %v", tc.host, tc.recordType, err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("recordValues(%s, %s) = %q, want %q", tc.host, tc.recordType, got, tc.want)
			}
		})
	}
}

package kittest_test

// declared_resources_recorded_test.go pins CheckDeclaredResources handing a
// named resource what the engine hands it on a later verb: the inputs
// recorded at subscribe, DeclaredResources.ResourceInputs, and the identity
// its ensure stored, DeclaredResources.ResourceIdentity, both put on the
// Desired the check drives. A resource implementing handler.RequestObserver
// or handler.RequestDeleter is observed or deleted through it, handed that
// Desired; one implementing neither keeps getting the Identity alone.
// Recorded inputs or a stored identity with no resource named are a
// request the engine never sends, since DecodeRequest refuses it, so the
// check reports one problem naming the fields and drives nothing.

import (
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/kittest"
)

// recordedInputs are the inputs recorded for instance at subscribe.
var recordedInputs = map[string]json.RawMessage{
	"network": json.RawMessage(`"vpc-0abc"`),
	"size":    json.RawMessage(`{"cpu":"2","memory":"4Gi"}`),
}

// storedIdentity is the identity instance's ensure stored.
var storedIdentity = map[string]string{"hostId": "i-0abc"}

// requestScripted is a scriptedResource that also implements
// handler.RequestObserver and handler.RequestDeleter, keeping the Desired
// each of those calls is handed and writing each to the shared log under
// its own name, so a plain "observe" or "delete" entry proves the
// identity-only call ran instead.
type requestScripted struct {
	*scriptedResource
	observed, deleted []contract.Desired
}

func (r *requestScripted) ObserveWithRequest(ctx context.Context, d contract.Desired) (contract.Observation, error) {
	r.record("observe with request", d.SubscriptionID)
	r.observed = append(r.observed, d)
	return r.FakeResource.Observe(ctx, d.Identity)
}

func (r *requestScripted) DeleteWithRequest(ctx context.Context, d contract.Desired) error {
	r.record("delete with request", d.SubscriptionID)
	r.deleted = append(r.deleted, d)
	return r.FakeResource.Delete(ctx, d.Identity)
}

var (
	_ handler.DeclaredResource = (*requestScripted)(nil)
	_ handler.RequestObserver  = (*requestScripted)(nil)
	_ handler.RequestDeleter   = (*requestScripted)(nil)
)

// recordedInput is the block's input naming instance with its recorded
// inputs and stored identity.
func recordedInput(b *checkedBlock) kittest.DeclaredResources {
	in := b.input()
	in.Resource = "instance"
	in.ResourceInputs = recordedInputs
	in.ResourceIdentity = storedIdentity
	return in
}

// wantRecordedDesired checks d is the check's Desired naming instance and
// carrying its recorded inputs and stored identity.
func wantRecordedDesired(t *testing.T, call string, d contract.Desired) {
	t.Helper()
	want := checkedDesired
	want.Resource = "instance"
	want.ResourceInputs = recordedInputs
	want.ResourceIdentity = storedIdentity
	if !reflect.DeepEqual(d, want) {
		t.Errorf("%s was handed %+v, want the check's Desired naming instance with its recorded inputs and stored identity: %+v", call, d, want)
	}
}

func TestCheckDeclaredResources_HandsTheRecordedInputsAndStoredIdentityToObserveAndDelete(t *testing.T) {
	b := newCheckedBlock()
	instance := &requestScripted{scriptedResource: b.instance}
	in := recordedInput(b)
	in.Resources["instance"] = instance

	if problems := kittest.CheckDeclaredResources(in); len(problems) != 0 {
		t.Errorf("a converging resource handed its recorded inputs must have no problems, got %q", problems)
	}
	want := []string{
		"instance ensure sub-checked",
		"instance observe with request sub-checked",
		"instance ensure sub-checked",
		"instance observe with request sub-checked",
		"instance delete with request sub-checked",
		"instance observe with request sub-checked",
	}
	if got := declaredOnly(*b.log); !reflect.DeepEqual(got, want) {
		t.Errorf("calls =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if len(instance.observed) != 3 || len(instance.deleted) != 1 {
		t.Fatalf("instance's ObserveWithRequest ran %d times and DeleteWithRequest %d, want 3 and 1", len(instance.observed), len(instance.deleted))
	}
	for _, d := range instance.observed {
		wantRecordedDesired(t, "ObserveWithRequest", d)
	}
	wantRecordedDesired(t, "DeleteWithRequest", instance.deleted[0])
}

func TestCheckDeclaredResources_HandsTheRecordedInputsAndStoredIdentityToEnsure(t *testing.T) {
	b := newCheckedBlock()
	var got contract.Desired
	in := recordedInput(b)
	in.Resources["instance"] = &desiredCapture{DeclaredResource: b.instance, got: &got}

	kittest.CheckDeclaredResources(in)
	wantRecordedDesired(t, "Ensure", got)
}

func TestCheckDeclaredResources_ObservesAndDeletesAResourceWithOnlyTheThreeCallsWithTheIdentityAlone(t *testing.T) {
	b := newCheckedBlock()

	if problems := kittest.CheckDeclaredResources(recordedInput(b)); len(problems) != 0 {
		t.Errorf("a converging resource with only the three calls must have no problems, got %q", problems)
	}
	want := []string{
		"instance ensure sub-checked",
		"instance observe sub-checked",
		"instance ensure sub-checked",
		"instance observe sub-checked",
		"instance delete sub-checked",
		"instance observe sub-checked",
	}
	if got := declaredOnly(*b.log); !reflect.DeepEqual(got, want) {
		t.Errorf("calls =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// resourceField matches the field name Resource standing alone, not as the
// start of ResourceInputs or ResourceIdentity.
var resourceField = regexp.MustCompile(`\bResource\b`)

func TestCheckDeclaredResources_ProblemForRecordedInputsOrIdentityWithNoResourceNamedAndNothingDriven(t *testing.T) {
	cases := []struct {
		name       string
		arrange    func(in *kittest.DeclaredResources)
		wantFields []string
	}{
		{
			name:       "inputs on DeclaredResources",
			arrange:    func(in *kittest.DeclaredResources) { in.ResourceInputs = recordedInputs },
			wantFields: []string{"ResourceInputs"},
		},
		{
			name:       "identity on DeclaredResources",
			arrange:    func(in *kittest.DeclaredResources) { in.ResourceIdentity = storedIdentity },
			wantFields: []string{"ResourceIdentity"},
		},
		{
			name:       "identity on the Desired",
			arrange:    func(in *kittest.DeclaredResources) { in.Desired.ResourceIdentity = storedIdentity },
			wantFields: []string{"ResourceIdentity"},
		},
		{
			name:       "inputs on the Desired",
			arrange:    func(in *kittest.DeclaredResources) { in.Desired.ResourceInputs = recordedInputs },
			wantFields: []string{"ResourceInputs"},
		},
		{
			name: "inputs and identity on DeclaredResources",
			arrange: func(in *kittest.DeclaredResources) {
				in.ResourceInputs = recordedInputs
				in.ResourceIdentity = storedIdentity
			},
			wantFields: []string{"ResourceInputs", "ResourceIdentity"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newCheckedBlock()
			in := b.input()
			c.arrange(&in)

			problems := kittest.CheckDeclaredResources(in)
			if len(problems) != 1 {
				t.Fatalf("problems = %q, want exactly one: the engine never sends %s with no resource named",
					problems, strings.Join(c.wantFields, " or "))
			}
			for _, field := range c.wantFields {
				if !strings.Contains(problems[0], field) {
					t.Errorf("problem %q does not name the field %s", problems[0], field)
				}
			}
			if !resourceField.MatchString(problems[0]) {
				t.Errorf("problem %q does not name the field Resource that must name the resource they are for", problems[0])
			}
			if len(*b.log) != 0 {
				t.Errorf("calls = %q, want none: nothing may be driven, supplied resources included", *b.log)
			}
			if !untouched(b.network) || !untouched(b.instance) {
				t.Error("the check drove a declared resource for a request the engine never sends")
			}
		})
	}
}

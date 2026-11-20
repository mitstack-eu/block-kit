package observe_test

// A node report carrying a state member is judged from that member alone, one without it is judged whole as before, and a state that is not an object fails naming the member.

import (
	"context"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/observe"
)

// withState is a report made after the verb whose state member holds state, beside the top-level members of top, which contradict it.
func withState(state any, top map[string]any) map[string]any {
	report := map[string]any{"state": state, "madeAt": verbAt.Add(30 * time.Second).Format(time.RFC3339Nano)}
	maps.Copy(report, top)
	return report
}

// credentials is a report whose Synced Application repository-credentials applied the secret once for each health given.
func credentials(healths ...string) map[string]any {
	applied := []map[string]any{}
	for _, health := range healths {
		secret := appliedObject("", "Secret", "argocd")
		secret["health"] = health
		applied = append(applied, secret)
	}
	return argoReport(nil, application("repository-credentials", "Synced", applied...))
}

func readCredentials(api observe.NodeReportsAPI) (observe.Application, string, error) {
	return observe.NodeReported{API: api}.ArgoCDApplication(context.Background(), contract.Desired{VerbAt: verbAt}, "sub-cluster", "repository-credentials")
}

func TestReportState_ReadersTakeJudgedMembersFromState(t *testing.T) {
	report := withState(credentials("Degraded"), credentials("Healthy"))
	without := maps.Clone(report)
	delete(without, "state")

	inState, inStateWhy, inStateErr := readCredentials(received(t, report))
	whole, wholeWhy, wholeErr := readCredentials(received(t, without))

	if inStateErr != nil || len(inState.Objects) != 1 || inState.Objects[0].Health.Status != "Degraded" {
		t.Errorf("ArgoCDApplication over %v = %+v, %q, %v; want the secret its state reads Degraded", report, inState, inStateWhy, inStateErr)
	}
	if wholeErr != nil || len(whole.Objects) != 1 || whole.Objects[0].Health.Status != "Healthy" {
		t.Errorf("ArgoCDApplication over %v = %+v, %q, %v; want the secret its top level reads Healthy", without, whole, wholeWhy, wholeErr)
	}
}

func TestReportState_EveryReaderIgnoresTopLevelJudgedMembers(t *testing.T) {
	listing := func(complete bool, items ...map[string]string) map[string]any {
		return map[string]any{"subscriptionResources": list(items...), "subscriptionResourcesComplete": complete}
	}
	readWhole := map[string]any{"subscriptionResources": list(), "subscriptionResourceKinds": []string{"Namespace"}}
	unread := observe.Health{Status: observe.HealthUnknown}
	for _, tc := range []struct {
		label      string
		ref        contract.ResourceRef
		state, top map[string]any
		want       contract.State
		health     observe.Health
	}{
		{"a state listing the namespace beside a complete top level without it", namespaceRef(), listing(false, appNamespace), listing(true), contract.Present, unread},
		{"a complete state without the namespace beside a top level listing it", namespaceRef(), listing(true), listing(false, appNamespace), contract.Absent, unread},
		{"a state that read the namespace's kind whole beside a top level listing it", namespaceRef(), readWhole, listing(false, appNamespace), contract.Absent, unread},
		{"a state reading the secret Degraded beside a top level reading it Healthy", appliedRef("secret", "argocd"), credentials("Degraded"), credentials("Healthy"), contract.Present, observe.Health{Status: "Degraded"}},
		{"a state whose Synced Application applied nothing beside a top level listing the secret", appliedRef("secret", "argocd"), credentials(), credentials("Healthy"), contract.Absent, unread},
	} {
		t.Run(tc.label, func(t *testing.T) {
			got, health, err := observe.NodeReported{API: received(t, withState(tc.state, tc.top))}.ObserveHealth(context.Background(), tc.ref)

			wantObservation(t, tc.ref, got, err, tc.want, nil, "")
			if health != tc.health {
				t.Errorf("health = %+v, want %+v", health, tc.health)
			}
		})
	}
}

func TestReportState_RefusesAStateThatIsNotAnObject(t *testing.T) {
	for _, state := range []any{[]any{}, nil, "healthy", 7, true} {
		report := withState(state, credentials())

		_, why, err := readCredentials(received(t, report))

		if err == nil || !strings.Contains(err.Error(), "state member") {
			t.Errorf("ArgoCDApplication over %v = %q, %v; want an error naming the state member, never a read of the top-level argocd", report, why, err)
		}
	}
}

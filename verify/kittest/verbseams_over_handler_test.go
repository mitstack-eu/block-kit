// CheckVerbSemantics over VerbSeamsOverHandler names each FakeResource misbehaviour, driven through a real handler serving a block's manifest.
package kittest_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/verify/kittest"
)

const verbSeamManifest = `apiVersion: mitstack/v1beta
type: block
metadata:
  id: verbseam-block
  name: Verb Seam Block
spec:
  version: 1.0.0
  resources: [aws.vpc, aws.security-group]
  lifecycle:
    endpoint: https://verbseam-block.example/hook
    operations: [subscribe, unsubscribe, upgrade, validate]
    contract: kit/v1
`

func TestVerbSeamsOverHandler(t *testing.T) {
	cases := []struct {
		name      string
		fake      *kittest.FakeResource
		wantNamed string
	}{
		{"a conforming FakeResource passes cleanly through a real handler serving the block's manifest", &kittest.FakeResource{}, ""},
		{"DuplicateOnEnsure fails the idempotent subscribe", &kittest.FakeResource{DuplicateOnEnsure: true}, "must CONVERGE, not duplicate"},
		{"LeakOnDelete fails teardown", &kittest.FakeResource{LeakOnDelete: true}, "survived unsubscribe"},
		{"ErrorWhenAlreadyAbsent fails absence-is-success", &kittest.FakeResource{ErrorWhenAlreadyAbsent: true}, "endless retry"},
		{"ClobberOnUpgrade fails the convergent upgrade", &kittest.FakeResource{Outputs: map[string]string{"url": "https://example.test"}, ClobberOnUpgrade: true}, "destructive upgrade"},
		// DeadReportedPending answers Unknown with no error though ObserveErr is set, so ValidateWhenDead reads pending.
		{"DeadReportedPending fails dead detection", &kittest.FakeResource{DeadReportedPending: true, ObserveErr: errors.New("this would normally be an unrecoverable failure")}, "would wait forever"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := kittest.CheckVerbSemantics(kittest.VerbSeamsOverHandler(t, []byte(verbSeamManifest), tc.fake))

			wantProblem(t, problems, tc.wantNamed)
		})
	}
}

func wantProblem(t *testing.T, problems []string, wantNamed string) {
	t.Helper()
	found := false
	for _, p := range problems {
		found = found || strings.Contains(p, wantNamed)
	}
	if (wantNamed == "" && len(problems) != 0) || (wantNamed != "" && !found) {
		t.Errorf("CheckVerbSemantics = %q, want a problem carrying %q (none when empty)", problems, wantNamed)
	}
}

package contract_test

// CheckManifest gives each row of the shared verdict table its verdict, and accepts an http:// endpoint only when the options allow one.

import (
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
)

func checkRow(t *testing.T, row contract.ManifestVerdict, opts contract.CheckOptions) string {
	t.Helper()
	m, err := contract.ParseManifest(row.Manifest)
	if err != nil {
		t.Fatalf("ParseManifest(%s): %v", row.Name, err)
	}
	if err := contract.CheckManifest(m.Block, platformSnapshot(t).Holds, opts); err != nil {
		return err.Error()
	}
	return ""
}

func verdictRows(t *testing.T) []contract.ManifestVerdict {
	t.Helper()
	rows, err := contract.ManifestVerdicts()
	if err != nil {
		t.Fatalf("ManifestVerdicts: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("the verdict table holds no row, so running it proves nothing")
	}
	return rows
}

func TestCheckManifest_GivesEachRowOfTheSharedTableItsVerdict(t *testing.T) {
	for _, row := range verdictRows(t) {
		t.Run(row.Name, func(t *testing.T) {
			if got := checkRow(t, row, contract.CheckOptions{}); got != row.Refusal {
				t.Errorf("CheckManifest(%s) refused %q; want %q", row.Name, got, row.Refusal)
			}
		})
	}
}

func TestCheckManifest_HoldsSnapshotAndRestoreToTheStateTheManifestDeclares(t *testing.T) {
	var accepted contract.ManifestVerdict
	for _, row := range verdictRows(t) {
		if row.Name == "accepted.yaml" {
			accepted = row
		}
	}
	if accepted.Manifest == nil {
		t.Fatal("the verdict table has no accepted.yaml row")
	}
	const ops = "operations: [subscribe, unsubscribe, upgrade, validate]"
	const unit = "  continuity:\n    - name: app-data\n      kind: postgres\n      stateful: true\n      handover: snapshot\n"
	manifest := func(operations string, withState bool) []byte {
		raw := strings.Replace(string(accepted.Manifest), ops, "operations: ["+operations+"]", 1)
		if withState {
			raw = strings.Replace(raw, "  lifecycle:\n", unit+"  lifecycle:\n", 1)
		}
		return []byte(raw)
	}
	cases := []struct {
		label      string
		operations string
		unit       bool
		want       string
	}{
		{"a snapshot handover with both operations listed", "subscribe, unsubscribe, snapshot, restore", true, ""},
		{"a snapshot handover with restore unlisted", "subscribe, unsubscribe, snapshot", true, "to list restore"},
		{"a snapshot handover with snapshot unlisted", "subscribe, unsubscribe, restore", true, "to list snapshot"},
		{"snapshot and restore listed with no state declared", "subscribe, unsubscribe, snapshot, restore", false, "spec.continuity"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			row := contract.ManifestVerdict{Name: tc.label, Manifest: manifest(tc.operations, tc.unit)}

			got := checkRow(t, row, contract.CheckOptions{})

			if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
				t.Errorf("CheckManifest(%s) refused %q; want a refusal naming %q (empty for accepted)", tc.label, got, tc.want)
			}
		})
	}
}

func TestCheckManifest_AcceptsAnHTTPEndpointOnlyWhenAllowed(t *testing.T) {
	var insecure contract.ManifestVerdict
	for _, row := range verdictRows(t) {
		if row.Name == "endpoint-not-https.yaml" {
			insecure = row
		}
	}
	if insecure.Manifest == nil {
		t.Fatal("the verdict table has no endpoint-not-https.yaml row")
	}
	allow := contract.CheckOptions{AllowInsecureEndpoint: true}

	if got := checkRow(t, insecure, allow); got != "" {
		t.Errorf("CheckManifest with AllowInsecureEndpoint refused the http:// endpoint: %s", got)
	}
	ftp := insecure
	ftp.Manifest = []byte(strings.Replace(string(insecure.Manifest), "http://", "ftp://", 1))
	if got, want := checkRow(t, ftp, allow), "spec.lifecycle.endpoint must be an http:// or https:// URL"; got != want {
		t.Errorf("CheckManifest with AllowInsecureEndpoint refused an ftp:// endpoint with %q; want %q", got, want)
	}
}

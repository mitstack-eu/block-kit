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

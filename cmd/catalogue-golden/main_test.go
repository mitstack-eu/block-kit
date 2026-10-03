package main

// main_test.go pins the catalogue-golden command: it prints the resource-type
// catalogue as this release ships it, in the form the contract package's
// comparison reads from contract/goldens/catalogue/resourcetypes.json. The
// seam is Run(args, stdout, stderr), so the command is testable without a
// process.

import (
	"bytes"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

func TestRun_PrintsTheCurrentCatalogueInGoldenForm(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("Run(nil) = %d, want 0; stderr: %s", code, stderr.String())
	}
	want := contract.MarshalCatalogue(contract.CurrentCatalogue())
	if !bytes.Equal(stdout.Bytes(), want) {
		t.Errorf("Run(nil) printed\n%s\nwant\n%s", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("Run(nil) wrote %q to stderr, want nothing", stderr.String())
	}
}

func TestRun_PrintsACatalogueParseCatalogueReadsBack(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("Run(nil) = %d, want 0; stderr: %s", code, stderr.String())
	}
	got, err := contract.ParseCatalogue(stdout.Bytes())
	if err != nil {
		t.Fatalf("ParseCatalogue(Run output): %v", err)
	}
	if got.Major != contract.CatalogueMajor {
		t.Errorf("printed major %d, want CatalogueMajor %d", got.Major, contract.CatalogueMajor)
	}
	if len(got.Types) != len(contract.ResourceTypes()) {
		t.Errorf("printed %d types, want the %d of ResourceTypes()", len(got.Types), len(contract.ResourceTypes()))
	}
}

func TestRun_RefusesAnyArgumentWithAUsageError(t *testing.T) {
	for _, args := range [][]string{{"extra"}, {"-schema", "x.json"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 2 {
			t.Errorf("Run(%q) = %d, want 2", args, code)
		}
		if stdout.Len() != 0 {
			t.Errorf("Run(%q) printed %q to stdout, want nothing", args, stdout.String())
		}
		if stderr.Len() == 0 {
			t.Errorf("Run(%q) wrote nothing to stderr, want a usage message", args)
		}
	}
}

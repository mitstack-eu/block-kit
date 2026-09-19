package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/mitstack-eu/operator-kit/provenance"
)

// runVerify implements `blockprov verify` — the same provenance.Verify code
// path the platform calls, exposed so a record can be checked by hand.
func runVerify(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	recordPath := fs.String("record", "", "path to record.json")
	jwks := fs.String("jwks", "", "path or URL to the signing JWKS")
	repository := fs.String("repository", "", "the expected publishing repository")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}
	if *recordPath == "" || *jwks == "" || *repository == "" {
		fmt.Fprintln(stdout, "blockprov verify: --record, --jwks and --repository are all required")
		return 2
	}

	recordRaw, err := os.ReadFile(*recordPath)
	if err != nil {
		fmt.Fprintf(stdout, "blockprov verify: reading record: %v\n", err)
		return 1
	}
	var rec provenance.Record
	if err := json.Unmarshal(recordRaw, &rec); err != nil {
		fmt.Fprintf(stdout, "blockprov verify: record is not valid JSON: %v\n", err)
		return 1
	}

	jwksRaw, err := readPathOrURL(*jwks)
	if err != nil {
		fmt.Fprintf(stdout, "blockprov verify: reading JWKS: %v\n", err)
		return 1
	}
	resolve, err := provenance.ParseJWKS(jwksRaw)
	if err != nil {
		fmt.Fprintf(stdout, "blockprov verify: %v\n", err)
		return 1
	}

	if err := provenance.Verify(rec, resolve, provenance.Expect{Repository: *repository}); err != nil {
		fmt.Fprintf(stdout, "blockprov verify: record rejected: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "blockprov verify: record accepted")
	return 0
}

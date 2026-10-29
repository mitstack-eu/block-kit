package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mitstack-eu/block-kit/verify/provenance"
)

type blockverifyReport struct {
	SuiteVersion  string `json:"suiteVersion"`
	Verdict       string `json:"verdict"`
	ResultsDigest string `json:"resultsDigest"`
}

func runBuild(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	manifestPath := fs.String("manifest", "", "path to the block's manifest.yaml")
	reportPath := fs.String("report", "", "path to a blockverify --json report")
	blockVerifyPath := fs.String("block-verify", "", "path to an OpenTofu block's block-verify -test.v output, in place of --report")
	blockID := fs.String("block-id", "", "the block's id")
	blockVersion := fs.String("block-version", "", "the block's version")
	imageRef := fs.String("image-ref", "", "the full pushed image reference")
	imageDigest := fs.String("image-digest", "", "the pushed image digest, sha256:...")
	kitVersion := fs.String("kit-version", "", "the kit version the block's go.mod requires, or its operator image was built with")
	repository := fs.String("repository", "", "the publishing repository, owner/name")
	commit := fs.String("commit", "", "the source commit sha")
	runURL := fs.String("run-url", "", "the workflow run's URL")
	tokenURL := fs.String("token-url", "", "GitHub's ACTIONS_ID_TOKEN_REQUEST_URL")
	tokenRequestToken := fs.String("token-request-token", "", "GitHub's ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	out := fs.String("out", "", "path to write the signed record to")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}

	required := []struct{ name, value string }{
		{"manifest", *manifestPath}, {"block-id", *blockID}, {"block-version", *blockVersion},
		{"image-ref", *imageRef}, {"image-digest", *imageDigest},
		{"kit-version", *kitVersion}, {"repository", *repository},
		{"commit", *commit}, {"run-url", *runURL},
		{"token-url", *tokenURL}, {"token-request-token", *tokenRequestToken},
		{"out", *out},
	}
	for _, r := range required {
		if r.value == "" {
			fmt.Fprintf(stdout, "blockprov build: --%s is required\n", r.name)
			return 2
		}
	}
	if (*reportPath == "") == (*blockVerifyPath == "") {
		fmt.Fprintln(stdout, "blockprov build: exactly one of --report and --block-verify is required")
		return 2
	}

	manifestBytes, err := os.ReadFile(*manifestPath)
	if err != nil {
		fmt.Fprintf(stdout, "blockprov build: reading manifest: %v\n", err)
		return 1
	}
	var report blockverifyReport
	if *blockVerifyPath != "" {
		output, err := os.ReadFile(*blockVerifyPath)
		if err != nil {
			fmt.Fprintf(stdout, "blockprov build: reading block-verify output: %v\n", err)
			return 1
		}
		report = blockVerifyReport(output, *kitVersion)
	} else {
		reportBytes, err := os.ReadFile(*reportPath)
		if err != nil {
			fmt.Fprintf(stdout, "blockprov build: reading report: %v\n", err)
			return 1
		}
		if err := json.Unmarshal(reportBytes, &report); err != nil {
			fmt.Fprintf(stdout, "blockprov build: report is not valid JSON: %v\n", err)
			return 1
		}
	}
	// The last gate before a token is requested, in case a stale or hand-edited failing result reaches this command.
	if report.Verdict != "pass" {
		fmt.Fprintf(stdout, "blockprov build: refusing to sign — the conformance verdict was %q, not \"pass\"\n", report.Verdict)
		return 1
	}

	sum := sha256.Sum256(manifestBytes)
	payload := provenance.Payload{
		SchemaVersion:  provenance.SchemaVersion,
		BlockID:        *blockID,
		BlockVersion:   *blockVersion,
		ImageRef:       *imageRef,
		ImageDigest:    *imageDigest,
		Manifest:       base64.StdEncoding.EncodeToString(manifestBytes),
		ManifestSha256: "sha256:" + hex.EncodeToString(sum[:]),
		KitVersion:     *kitVersion,
		Conformance: provenance.Conformance{
			SuiteVersion:  report.SuiteVersion,
			Verdict:       report.Verdict,
			ResultsDigest: report.ResultsDigest,
			RunURL:        *runURL,
		},
		SourceRepository: *repository,
		SourceCommit:     *commit,
		IssuedAt:         time.Now().UTC(),
	}

	rec, err := provenance.Build(payload, func(audience string) (string, error) {
		return mintOIDCToken(*tokenURL, *tokenRequestToken, audience)
	})
	if err != nil {
		fmt.Fprintf(stdout, "blockprov build: %v\n", err)
		return 1
	}

	recordBytes, err := json.Marshal(rec)
	if err != nil {
		fmt.Fprintf(stdout, "blockprov build: encoding record: %v\n", err)
		return 1
	}
	if err := os.WriteFile(*out, recordBytes, 0o644); err != nil {
		fmt.Fprintf(stdout, "blockprov build: writing record: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "blockprov build: wrote %s\n", *out)
	return 0
}

// blockVerifyReport reads block-verify's go test -test.v output as a pass only when a test passed, none failed and the run closed on PASS.
func blockVerifyReport(output []byte, kitVersion string) blockverifyReport {
	text := string(output)
	verdict := "fail"
	if strings.Contains(text, "--- PASS: ") && !strings.Contains(text, "--- FAIL: ") && strings.HasSuffix(strings.TrimSpace(text), "\nPASS") {
		verdict = "pass"
	}
	sum := sha256.Sum256(output)
	return blockverifyReport{SuiteVersion: "block-verify@" + kitVersion, Verdict: verdict, ResultsDigest: "sha256:" + hex.EncodeToString(sum[:])}
}

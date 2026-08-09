package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// suiteVersion is the version of THIS suite's contract (report shape, suite
// set, run order, digest algorithms) — bump it in the same change that
// changes any of those, mirroring scripts/block-verify.sh's SUITE_VERSION.
const suiteVersion = "1.0.0"

// Report is the standalone-mode conformance report blockverify emits — see
// the package doc for the full field-by-field contract.
type Report struct {
	SuiteVersion   string        `json:"suiteVersion"`
	KitVersion     string        `json:"kitVersion"`
	Mode           string        `json:"mode"`
	ManifestDigest string        `json:"manifestDigest"`
	Verdict        string        `json:"verdict"`
	ResultsDigest  string        `json:"resultsDigest"`
	Suites         []SuiteResult `json:"suites"`
}

// SuiteResult is one entry of Report.Suites, in run order.
type SuiteResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "pass" | "fail" | "skipped"
	Detail string `json:"detail"`
}

const usageMessage = `blockverify: standalone conformance runner for a mitstack block living in an
external repository.

Usage:
  blockverify [--json] --standalone <dir>

This command supports ONLY standalone mode (a single external target,
scanned in isolation). It refuses to run without --standalone and a target
directory. For repo-mode verification against this monorepo's own examples/
tree, use scripts/block-verify.sh instead — that is the tool for a block
living inside this repository; this command is its external-repo twin.
`

// Run executes the blockverify CLI and returns its process exit code — see
// the package doc for the full 0/1/2 contract. All output goes through
// stdout, never the real process streams directly, so the whole command is
// testable without touching os.Stdout.
func Run(args []string, stdout io.Writer) int {
	jsonOut, standalone, target := parseArgs(args)

	if !standalone || target == "" {
		fmt.Fprint(stdout, usageMessage)
		return 2
	}

	report := runStandalone(target)

	if jsonOut {
		raw, err := json.Marshal(report)
		if err != nil {
			// Report is a plain struct of strings/slices; Marshal cannot
			// fail on it. Guarded anyway so Run never panics on it.
			fmt.Fprintf(stdout, "blockverify: internal error marshaling report: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, string(raw))
	} else {
		printHuman(stdout, report)
	}

	if report.Verdict != "pass" {
		return 1
	}
	return 0
}

// parseArgs recognises --json and --standalone as flags; any other argument
// is the target directory (the last one wins, mirroring
// scripts/block-verify.sh's own arg loop).
func parseArgs(args []string) (jsonOut, standalone bool, target string) {
	for _, a := range args {
		switch a {
		case "--json":
			jsonOut = true
		case "--standalone":
			standalone = true
		default:
			target = a
		}
	}
	return jsonOut, standalone, target
}

// runStandalone runs every suite against target, in the fixed contract
// order, and assembles the report.
func runStandalone(target string) Report {
	suites := []SuiteResult{
		trustPlumbingSuite(target),
		contractSuite(target),
		secretSafetySuite(target),
		teardownSuite(target),
		deliverableSuite(target),
		capabilityParitySuite(),
	}

	verdict := "pass"
	for _, s := range suites {
		// Only "fail" flips the verdict — "skipped" is an honest "not
		// applicable here", never a partial pass.
		if s.Status == "fail" {
			verdict = "fail"
			break
		}
	}

	return Report{
		SuiteVersion:   suiteVersion,
		KitVersion:     kitVersionFloor,
		Mode:           "standalone",
		ManifestDigest: manifestDigest(target),
		Verdict:        verdict,
		ResultsDigest:  resultsDigest(suites),
		Suites:         suites,
	}
}

// manifestDigest is sha256 of <target>/manifest.yaml's raw bytes, formatted
// "sha256:<hex>" — empty if the file is absent (trust-plumbing is what
// reports that as a failure; this field simply has nothing to digest).
func manifestDigest(target string) string {
	raw, err := os.ReadFile(filepath.Join(target, "manifest.yaml"))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// suiteDigestEntry is the {name,status} shape resultsDigest is computed over —
// detail EXCLUDED, and field order matters (name then status): the result
// must be byte-identical to `jq -c '[.suites[] | {name, status}]' | sha256sum`
// so a downstream consumer can recompute and verify it independently.
type suiteDigestEntry struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func resultsDigest(suites []SuiteResult) string {
	entries := make([]suiteDigestEntry, 0, len(suites))
	for _, s := range suites {
		entries = append(entries, suiteDigestEntry{Name: s.Name, Status: s.Status})
	}
	raw, _ := json.Marshal(entries)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// printHuman renders the same report as a suite-by-suite readable summary,
// on the model of scripts/block-verify.sh's non-JSON output.
func printHuman(w io.Writer, r Report) {
	fmt.Fprintf(w, "blockverify %s (%s mode)\n", r.SuiteVersion, r.Mode)
	for _, s := range r.Suites {
		mark := "✗" // ✗
		switch s.Status {
		case "pass":
			mark = "✓" // ✓
		case "skipped":
			mark = "○" // ○
		}
		fmt.Fprintf(w, "  %s %-22s %s\n", mark, s.Name, s.Detail)
	}
	fmt.Fprintln(w)
	if r.Verdict == "pass" {
		fmt.Fprintf(w, "VERDICT: PASS — %d suite(s), kit %s, %s mode\n", len(r.Suites), r.KitVersion, r.Mode)
	} else {
		fmt.Fprintln(w, "VERDICT: FAIL — a partial pass is a FAIL. There is no 'passed with exceptions'.")
	}
}

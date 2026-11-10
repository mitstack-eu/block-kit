// Command blockverify, part of mitstack's own tooling, runs the conformance suites over one block's directory, in --standalone mode only.
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

// suiteVersion must change with the report shape, suite set, run order or digest algorithms.
const suiteVersion = "2.1.0"

// Report is the standalone-mode conformance report blockverify emits.
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
  blockverify [--json] --standalone [--opentofu-only] [--base-manifest <path>] [--changed-files <path>] [--api-url <url>] <dir>

--opentofu-only judges an OpenTofu-only block, a module with no Go code of
its own whose image serves it through the opentofu-operator binary.
trust-plumbing then needs a .tf or .tofu file at the root and fails a go.mod
or .go file anywhere in the tree, and the suites that judge a Go operator are
skipped. deliverable and manifest judge the block as they judge any other.

--base-manifest names the base branch's manifest.yaml. When the file exists,
a spec.version that differs from the base's needs spec.releaseClass and
spec.releaseNote. A path that does not exist is a block new on this branch,
and no version is compared.

--changed-files names the list of files the pull request changes,
NUL-separated as git diff -z --name-only prints it. When spec.version equals
the base's, a changed file other than Markdown, a file under .github/ or
LICENSE fails the manifest suite, because the image it changes publishes
only under a new version.

--api-url names the platform, such as https://api.mitstack.dev. The manifest
suite then reads the type snapshot it serves and fails when the block's
type-snapshot.json differs from it in a field or an output of a type the
manifest names, or when the snapshot cannot be read.

This command supports ONLY standalone mode: one block's directory, scanned
in isolation. It refuses to run without --standalone and a target directory.
From the block's own directory, run:

  blockverify --standalone .
`

// Run executes the blockverify CLI and returns its exit code, writing only to stdout.
func Run(args []string, stdout io.Writer) int {
	a, ok := parseArgs(args)
	if !ok || !a.standalone || a.target == "" {
		fmt.Fprint(stdout, usageMessage)
		return 2
	}

	operatorSuites := goBlockSuites
	if a.opentofuOnly {
		operatorSuites = opentofuOnlySuites
	}
	report := runStandalone(a, operatorSuites)

	if a.jsonOut {
		raw, err := json.Marshal(report)
		if err != nil {
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

type cliArgs struct {
	jsonOut, standalone bool
	opentofuOnly        bool
	target              string
	baseManifest        string
	changedFiles        string
	apiURL              string
}

// parseArgs lets the last non-flag argument win as the target, and reports ok false when --base-manifest, --changed-files or --api-url has no value, or --api-url an empty one, so an unset input cannot skip the comparison.
func parseArgs(args []string) (a cliArgs, ok bool) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			a.jsonOut = true
		case "--standalone":
			a.standalone = true
		case "--opentofu-only":
			a.opentofuOnly = true
		case "--base-manifest":
			if i+1 == len(args) {
				return a, false
			}
			i++
			a.baseManifest = args[i]
		case "--changed-files":
			if i+1 == len(args) {
				return a, false
			}
			i++
			a.changedFiles = args[i]
		case "--api-url":
			if i+1 == len(args) || args[i+1] == "" {
				return a, false
			}
			i++
			a.apiURL = args[i]
		default:
			a.target = args[i]
		}
	}
	return a, true
}

// goBlockSuites answers trust-plumbing and each Go operator suite for a block whose operator is its own Go code.
func goBlockSuites(target string) []SuiteResult {
	return []SuiteResult{
		trustPlumbingSuite(target),
		contractSuite(target),
		secretSafetySuite(target),
		guardedClientSuite(target),
		teardownSuite(target),
		resourcesSuite(target),
	}
}

// runStandalone runs the suites in the fixed contract order, the first six as operatorSuites answers them.
func runStandalone(a cliArgs, operatorSuites func(target string) []SuiteResult) Report {
	target := a.target
	suites := append(operatorSuites(target), deliverableSuite(target), manifestSuite(target, a.baseManifest, a.changedFiles, a.apiURL))

	verdict := "pass"
	for _, s := range suites {
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

func manifestDigest(target string) string {
	raw, err := os.ReadFile(filepath.Join(target, "manifest.yaml"))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// suiteDigestEntry omits detail and keeps name before status, so the digest equals `jq -c '[.suites[] | {name, status}]' | sha256sum`.
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

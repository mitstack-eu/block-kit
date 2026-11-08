package main

// blockverify --opentofu-only answers every suite for an OpenTofu module with no Go code of its own, and holds its spec.version to the base branch's as it holds a Go block's.

import (
	"testing"
)

// opentofuBlock is an OpenTofu-only block as its repository holds it: a module, the image it builds, its manifest and its type snapshot.
func opentofuBlock(opts ...option) dirFunc {
	return fixture(func(t *testing.T) map[string]string {
		files := map[string]string{
			manifestFile: manifestHead,
			"main.tf":    "resource \"aws_s3_bucket\" \"data\" {\n  bucket_prefix = \"data-\"\n}\n",
			"Dockerfile": "FROM opentofu-operator\nCOPY . /module/\n",
			"README.md":  "# An OpenTofu-only block\n",
		}
		withTypeSnapshot(t, files)
		return files
	}, opts...)
}

var (
	opentofuStandalone  = []string{"--json", "--standalone", "--opentofu-only", "$DIR"}
	opentofuPullRequest = []string{"--json", "--standalone", "--opentofu-only", "--base-manifest", "$BASE", "--changed-files", "$CHANGED", "$DIR"}
)

func TestRun_OpenTofuOnly(t *testing.T) {
	released := withVersion(t, "1.1.0") + "  releaseClass: feature\n  releaseNote: Adds the data bucket.\n"
	skipped := suiteWant{"skipped", []string{"--opentofu-only"}}
	plumbing := func(detail ...string) map[string]suiteWant {
		return map[string]suiteWant{"trust-plumbing": {"fail", detail}}
	}
	from100 := baseManifest(withVersion(t, "1.0.0"))
	cases := []runCase{
		{name: "an OpenTofu-only block passes, each suite answering and none failing for want of Go code", dir: opentofuBlock(), args: opentofuStandalone, exit: 0, verdict: "pass",
			suites: map[string]suiteWant{
				"trust-plumbing": {"pass", []string{"kit/v1", "main.tf"}},
				"contract":       skipped, "secret-safety": skipped, "guarded-client": skipped, "teardown": skipped, "resources": skipped,
				"deliverable": {status: "pass"},
				"manifest":    {status: "pass"},
			}},
		{name: "--opentofu-only after the target judges the target", dir: opentofuBlock(), args: []string{"--json", "--standalone", "$DIR", "--opentofu-only"}, exit: 0, verdict: "pass"},
		{name: "a module in a file of the .tofu extension passes trust-plumbing, naming it", dir: opentofuBlock(without("main.tf"), with("main.tofu", "locals {}\n")), args: opentofuStandalone,
			exit: 0, verdict: "pass", suites: map[string]suiteWant{"trust-plumbing": {"pass", []string{"main.tofu"}}}},
		{name: "a pull request changing the module with the base's version fails, naming its file and the kept version", dir: opentofuBlock(), base: from100,
			changed: changedFiles("main.tf"), args: opentofuPullRequest, exit: 1, verdict: "fail",
			suites: map[string]suiteWant{"trust-plumbing": {status: "pass"}, "manifest": {"fail", []string{"main.tf", "spec.version 1.0.0", "version bump"}}}},
		{name: "a pull request changing the module with a raised, released version passes", dir: opentofuBlock(with(manifestFile, released)), base: from100,
			changed: changedFiles("main.tf"), args: opentofuPullRequest, exit: 0, verdict: "pass",
			suites: map[string]suiteWant{"manifest": {"pass", []string{"1.0.0 to 1.1.0"}}}},
		{name: "a pull request changing only README.md and .github/workflows/ci.yml with the base's version passes", dir: opentofuBlock(), base: from100,
			changed: changedFiles("README.md", ".github/workflows/ci.yml"), args: opentofuPullRequest, exit: 0, verdict: "pass"},
		{name: "a first pull request whose base has no manifest reads the block as new and passes", dir: opentofuBlock(), base: missingBase,
			changed: changedFiles("main.tf", manifestFile), args: opentofuPullRequest, exit: 0, verdict: "pass",
			suites: map[string]suiteWant{"manifest": {"pass", []string{"does not exist, so the block is new"}}}},
		{name: "a tree holding a Go module file fails trust-plumbing, naming it", dir: opentofuBlock(with("go.mod", kitGoMod(t))), args: opentofuStandalone, exit: 1, verdict: "fail",
			suites: plumbing("go.mod", "--opentofu-only")},
		{name: "a tree holding a nested Go file fails trust-plumbing, naming it", dir: opentofuBlock(with("tools/gen.go", "package tools\n")), args: opentofuStandalone, exit: 1, verdict: "fail",
			suites: plumbing("tools/gen.go", "--opentofu-only")},
		{name: "a tree with no OpenTofu file at its root fails trust-plumbing, naming both extensions", dir: opentofuBlock(without("main.tf"), with("modules/bucket/main.tf", "locals {}\n")),
			args: opentofuStandalone, exit: 1, verdict: "fail", suites: plumbing(".tf", ".tofu")},
		{name: "a tree with no manifest fails trust-plumbing, naming the file", dir: opentofuBlock(without(manifestFile)), args: opentofuStandalone, exit: 1, verdict: "fail",
			suites: plumbing(manifestFile)},
		{name: "another lifecycle contract fails trust-plumbing, naming it", dir: opentofuBlock(with(manifestFile, replaced(t, manifestHead, "contract: kit/v1", "contract: kit/v2"))),
			args: opentofuStandalone, exit: 1, verdict: "fail", suites: plumbing("kit/v2")},
		{name: "the same tree without the flag is judged as a Go block and fails trust-plumbing", dir: opentofuBlock(), args: jsonStandalone, exit: 1, verdict: "fail",
			suites: plumbing("go.mod")},
		{name: "the usage names --opentofu-only", dir: emptyDir, args: []string{}, exit: 2, stdout: []string{"--opentofu-only"}},
	}

	runCases(t, cases)
}

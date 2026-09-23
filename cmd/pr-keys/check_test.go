package main

// check_test.go pins the pure core of the pr-keys gate: check(title string,
// commits []commit) error decides whether a pull request's range delivers a
// board key. The rule, ported from this repository's own boardkeys package
// rather than re-implemented as a second regex:
//
//   - at least one commit in the range must deliver a key through
//     boardkeys.Message (a leading key list in its subject, or a whole-line
//     Jira: trailer in its body) — this is what a merge commit or a rebase
//     carries into the range;
//   - AND the pull request's own title must deliver a key through
//     boardkeys.Subject — this is what a squash carries, whether GitHub uses
//     the PR title (several commits) or a lone commit's own subject (one
//     commit), which the commits half already covers.
//
// A branch name folded into a merge commit's default subject
// ("Merge pull request #N from owner/branch") is not delivery intent: the
// key sits after the leading list, so boardkeys.Subject reads nothing there.
//
// check returns nil on pass, and on failure an error that names what is
// missing — "commit"/"commits", "title", or both — never a bare failure a
// caller has to re-diagnose.

import (
	"strings"
	"testing"
)

func TestCheck_KeyedLeadingSubjectAndKeyedTitle_Passes(t *testing.T) {
	commits := []commit{{Subject: "MIT-12: fix the thing"}}
	if err := check("MIT-12: fix the thing", commits); err != nil {
		t.Errorf("check() = %v, want nil", err)
	}
}

func TestCheck_JiraTrailerCommitAndKeyedTitle_Passes(t *testing.T) {
	commits := []commit{{Subject: "fix the thing", Body: "Jira: MIT-12"}}
	if err := check("MIT-12: fix the thing", commits); err != nil {
		t.Errorf("check() = %v, want nil", err)
	}
}

func TestCheck_MultiKeyLeadingList_Passes(t *testing.T) {
	commits := []commit{{Subject: "MIT-12, MIT-13: fix two things"}}
	if err := check("MIT-12, MIT-13: fix two things", commits); err != nil {
		t.Errorf("check() = %v, want nil", err)
	}
}

// TestCheck_KitPR6Shape_FailsAndNamesCommits pins a merge-commit range
// whose branch commits carry no key, whose GitHub-default merge subject names
// a key only inside the source branch name, and whose title does carry the
// key. A branch name is not delivery intent, so the commits half fails even
// though a key substring appears in the range.
func TestCheck_KitPR6Shape_FailsAndNamesCommits(t *testing.T) {
	commits := []commit{
		{Subject: "delete gets instance config"},
		{Subject: "fix review comments"},
		{Subject: "Merge pull request #6 from mitstack-eu/fix/MIT-3149-delete-gets-instance-config"},
	}
	err := check("MIT-3149: delete gets instance config", commits)
	if err == nil {
		t.Fatal("check() = nil, want an error — none of these commits deliver a key through boardkeys.Message")
	}
	if !strings.Contains(err.Error(), "commit") {
		t.Errorf("check() error = %q, want it to name the commits as missing", err.Error())
	}
	if strings.Contains(err.Error(), "title") {
		t.Errorf("check() error = %q, the title does carry the key — it must not be named as missing", err.Error())
	}
}

func TestCheck_KeyedCommitsKeylessTitle_FailsAndNamesTitle(t *testing.T) {
	commits := []commit{{Subject: "MIT-12: fix the thing"}}
	err := check("fix the thing", commits)
	if err == nil {
		t.Fatal("check() = nil, want an error — the title carries no key")
	}
	if !strings.Contains(err.Error(), "title") {
		t.Errorf("check() error = %q, want it to name the title as missing", err.Error())
	}
	if strings.Contains(err.Error(), "commit") {
		t.Errorf("check() error = %q, the commits do carry the key — they must not be named as missing", err.Error())
	}
}

func TestCheck_BothMissing_FailsAndNamesBoth(t *testing.T) {
	commits := []commit{{Subject: "fix the thing"}}
	err := check("fix the thing", commits)
	if err == nil {
		t.Fatal("check() = nil, want an error — neither the commits nor the title carry a key")
	}
	if !strings.Contains(err.Error(), "commit") {
		t.Errorf("check() error = %q, want it to name the commits as missing", err.Error())
	}
	if !strings.Contains(err.Error(), "title") {
		t.Errorf("check() error = %q, want it to name the title as missing", err.Error())
	}
}

func TestCheck_KeyLaterInSubject_DoesNotCount(t *testing.T) {
	commits := []commit{{Subject: "fix MIT-12 thing"}}
	err := check("MIT-12: fix the thing", commits)
	if err == nil {
		t.Fatal("check() = nil, want an error — a key that is not leading is prose, not delivery")
	}
	if !strings.Contains(err.Error(), "commit") {
		t.Errorf("check() error = %q, want it to name the commits as missing", err.Error())
	}
}

func TestCheck_KeyInOrdinaryBodyLine_DoesNotCount(t *testing.T) {
	commits := []commit{{Subject: "fix the thing", Body: "See MIT-12 for context"}}
	err := check("MIT-12: fix the thing", commits)
	if err == nil {
		t.Fatal("check() = nil, want an error — an ordinary body line is not a Jira: trailer")
	}
	if !strings.Contains(err.Error(), "commit") {
		t.Errorf("check() error = %q, want it to name the commits as missing", err.Error())
	}
}

func TestCheck_EmptyRange_NeverPassesVacuously(t *testing.T) {
	err := check("MIT-12: fix the thing", nil)
	if err == nil {
		t.Fatal("check() = nil, want an error — an empty commit range must never pass on the title alone")
	}
	if !strings.Contains(err.Error(), "commit") {
		t.Errorf("check() error = %q, want it to name the commits as missing", err.Error())
	}
}

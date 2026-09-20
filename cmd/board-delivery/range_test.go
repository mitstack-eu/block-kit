package main

// range_test.go pins resolveRange, a verbatim port of the workspace's
// scripts/workspace-delivery-range.sh: a pure function of three candidate
// shas that decides which commits a delivery covers, with no git access and
// no network. Its precedence is:
//
//  1. the anchor sha (read elsewhere from a board-synced git tag this
//     command advances only after a successful board write — self-healing,
//     since a failed run's commits stay inside the next successful run's
//     range);
//  2. else the push event's before sha — exactly what this push delivered;
//  3. else the single tip commit, expressed as <current>~1..<current>,
//     NEVER a bare sha.
//
// A candidate is usable only when it is non-empty, is not the all-zero sha
// (GitHub's "no such commit" sentinel on a branch's first push), and is not
// equal to current.
//
// resolveRange does not exist yet, so every reference below fails to
// compile until code-author adds it. That is the intended RED signal.

import (
	"strings"
	"testing"
)

const testZeroSHA = "0000000000000000000000000000000000000000"

func TestResolveRange_PrefersAnchorWhenUsable(t *testing.T) {
	got := resolveRange("anchorSHA", "beforeSHA", "curSHA")
	if want := "anchorSHA..curSHA"; got != want {
		t.Errorf("resolveRange(anchor, before, cur) = %q, want %q", got, want)
	}
}

func TestResolveRange_FallsBackToBeforeWhenAnchorEmpty(t *testing.T) {
	got := resolveRange("", "beforeSHA", "curSHA")
	if want := "beforeSHA..curSHA"; got != want {
		t.Errorf("resolveRange(\"\", before, cur) = %q, want %q", got, want)
	}
}

func TestResolveRange_FallsBackToBeforeWhenAnchorIsZeroSHA(t *testing.T) {
	got := resolveRange(testZeroSHA, "beforeSHA", "curSHA")
	if want := "beforeSHA..curSHA"; got != want {
		t.Errorf("resolveRange(zeroSHA, before, cur) = %q, want %q", got, want)
	}
}

func TestResolveRange_FallsBackToBeforeWhenAnchorEqualsCurrent(t *testing.T) {
	got := resolveRange("curSHA", "beforeSHA", "curSHA")
	if want := "beforeSHA..curSHA"; got != want {
		t.Errorf("resolveRange(cur, before, cur) = %q, want %q", got, want)
	}
}

func TestResolveRange_RejectingAnchorFallsThroughToBeforeNotToTip(t *testing.T) {
	// An unusable anchor must fall through to before, never straight to the
	// tip fallback while a usable before candidate is available.
	for _, anchor := range []string{"", testZeroSHA, "curSHA"} {
		got := resolveRange(anchor, "beforeSHA", "curSHA")
		if want := "beforeSHA..curSHA"; got != want {
			t.Errorf("resolveRange(%q, before, cur) = %q, want %q (fall through to before)", anchor, got, want)
		}
	}
}

func TestResolveRange_FallsBackToTipWhenAnchorAndBeforeBothEmpty(t *testing.T) {
	got := resolveRange("", "", "curSHA")
	if want := "curSHA~1..curSHA"; got != want {
		t.Errorf("resolveRange(\"\", \"\", cur) = %q, want %q", got, want)
	}
}

func TestResolveRange_FallsBackToTipWhenAnchorAndBeforeBothZeroSHA(t *testing.T) {
	got := resolveRange(testZeroSHA, testZeroSHA, "curSHA")
	if want := "curSHA~1..curSHA"; got != want {
		t.Errorf("resolveRange(zeroSHA, zeroSHA, cur) = %q, want %q", got, want)
	}
}

func TestResolveRange_FallsBackToTipWhenAnchorAndBeforeBothEqualCurrent(t *testing.T) {
	got := resolveRange("curSHA", "curSHA", "curSHA")
	if want := "curSHA~1..curSHA"; got != want {
		t.Errorf("resolveRange(cur, cur, cur) = %q, want %q", got, want)
	}
}

// TestResolveRange_NeverReturnsABareSHA is the load-bearing safety property:
// an unbounded `git log <sha>` walks every ancestor, which would transition
// the board's entire history in one run. Every degenerate input, including
// every rejection combination, must still produce a bounded "A..B" range.
func TestResolveRange_NeverReturnsABareSHA(t *testing.T) {
	const cur = "curSHA"
	cases := []struct{ anchor, before string }{
		{"", ""},
		{testZeroSHA, testZeroSHA},
		{cur, cur},
		{"", testZeroSHA},
		{testZeroSHA, ""},
		{"anchorSHA", "beforeSHA"},
		{"", "beforeSHA"},
		{testZeroSHA, "beforeSHA"},
		{cur, "beforeSHA"},
		{"anchorSHA", ""},
		{"anchorSHA", testZeroSHA},
		{"anchorSHA", cur},
	}
	for _, c := range cases {
		got := resolveRange(c.anchor, c.before, cur)
		if !strings.Contains(got, "..") {
			t.Errorf("resolveRange(%q, %q, %q) = %q, want a bounded range containing \"..\"", c.anchor, c.before, cur, got)
		}
		if got == cur {
			t.Errorf("resolveRange(%q, %q, %q) = %q, a bare sha — this would walk the entire ancestor history", c.anchor, c.before, cur, got)
		}
	}
}

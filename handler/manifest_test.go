package handler

import (
	"strings"
	"testing"
)

// hooksLinesFixture builds a manifest fragment anchored on spec.lifecycle,
// with hooksBlock supplying everything from the "hooks:" line onward, and
// returns the document split into lines along with the index of the
// "hooks:" line — exactly the two arguments parseHooksValue takes.
func hooksLinesFixture(t *testing.T, hooksBlock string) (lines []string, hooksLine int) {
	t.Helper()
	doc := "spec:\n  layer: infra\n  lifecycle:\n" + hooksBlock
	lines = strings.Split(doc, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "hooks:" {
			return lines, i
		}
	}
	t.Fatal("hooksLinesFixture: no \"hooks:\" line in the fixture")
	return nil, -1
}

// TestParseHooksValue_CommentBetweenItemsDeclaresBoth pins that a comment
// line between two items of a hooks: block sequence is skipped, not treated
// as the end of the sequence. A comment is valid inside a YAML block
// sequence, so both items on either side of it must still be read.
func TestParseHooksValue_CommentBetweenItemsDeclaresBoth(t *testing.T) {
	lines, hooksLine := hooksLinesFixture(t,
		"    hooks:\n"+
			"      - beforeEnsure\n"+
			"      # a comment between two items\n"+
			"      - afterDelete\n")

	got, err := parseHooksValue(lines, hooksLine)
	if err != nil {
		t.Fatalf("parseHooksValue: unexpected error: %v", err)
	}
	want := []string{"beforeEnsure", "afterDelete"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("parseHooksValue = %v, want %v — a comment between two items must not end the sequence early", got, want)
	}
}

// TestParseHooksValue_TabIndentedSequenceFailsOnErrorPath pins that a
// tab-indented hooks: sequence is refused through the same error path a
// value in no supported form takes, naming the manifest, rather than being
// read as a sequence with zero items. A tab is not valid YAML indentation.
func TestParseHooksValue_TabIndentedSequenceFailsOnErrorPath(t *testing.T) {
	lines, hooksLine := hooksLinesFixture(t,
		"    hooks:\n"+
			"\t- beforeEnsure\n"+
			"\t- afterDelete\n")

	got, err := parseHooksValue(lines, hooksLine)
	if err == nil {
		t.Fatalf("parseHooksValue: expected an error for a tab-indented sequence, got hooks %v and no error", got)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "manifest") {
		t.Errorf("error %q does not name the manifest", err.Error())
	}
	if len(got) != 0 {
		t.Errorf("parseHooksValue returned hooks %v alongside an error — an unreadable sequence must declare nothing", got)
	}
}

// TestParseHooksValue_ColumnZeroDecoyEndsSequence pins today's correct
// behaviour for the case the tab fix must not disturb: a line that is
// genuinely indented at column zero — not a comment, not a
// mismeasured tab — really does end the block sequence, and any "- name"
// line after it belongs to whatever follows, not to the hooks: list.
func TestParseHooksValue_ColumnZeroDecoyEndsSequence(t *testing.T) {
	lines, hooksLine := hooksLinesFixture(t,
		"    hooks:\n"+
			"      - beforeEnsure\n"+
			"decoy: true\n"+
			"      - afterDelete\n")

	got, err := parseHooksValue(lines, hooksLine)
	if err != nil {
		t.Fatalf("parseHooksValue: unexpected error: %v", err)
	}
	want := []string{"beforeEnsure"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("parseHooksValue = %v, want %v — a genuine column-zero line must still end the sequence", got, want)
	}
}

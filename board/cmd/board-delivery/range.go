package main

// range.go decides which commits a delivery covers. resolveRange is a
// verbatim port of scripts/workspace-delivery-range.sh's precedence: pure,
// no git and no network.

// zeroSHA is GitHub's "no such commit" sentinel on a branch's first push —
// a value, never a real commit.
const zeroSHA = "0000000000000000000000000000000000000000"

// resolveRange picks the bounded range a delivery covers: the anchor sha
// (read elsewhere from the board-synced tag) when usable, else the push
// event's before sha, else the single tip commit expressed as
// "<current>~1..<current>" — NEVER a bare sha, since an unbounded
// `git log <sha>` walks every ancestor.
func resolveRange(anchor, before, current string) string {
	if isUsableCandidate(anchor, current) {
		return anchor + ".." + current
	}
	if isUsableCandidate(before, current) {
		return before + ".." + current
	}
	return current + "~1.." + current
}

// isUsableCandidate reports whether sha is a real, distinct commit: non-empty,
// not the all-zero sentinel, and not equal to current.
func isUsableCandidate(sha, current string) bool {
	return sha != "" && sha != zeroSHA && sha != current
}

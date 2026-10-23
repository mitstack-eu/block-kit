package main

const zeroSHA = "0000000000000000000000000000000000000000"

// resolveRange prefers the anchor, then the push's before sha, then the tip alone, and never returns a bare sha, since an unbounded git log walks every ancestor.
func resolveRange(anchor, before, current string) string {
	if isUsableCandidate(anchor, current) {
		return anchor + ".." + current
	}
	if isUsableCandidate(before, current) {
		return before + ".." + current
	}
	return current + "~1.." + current
}

func isUsableCandidate(sha, current string) bool {
	return sha != "" && sha != zeroSHA && sha != current
}

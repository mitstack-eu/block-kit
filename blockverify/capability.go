package main

// capabilityParitySuite is ALWAYS skipped: capability-parity compares
// several providers of one capability, which is never meaningful for a
// single standalone target. It still always appears in the report, with a
// non-empty Detail — a suite that silently disappeared from the report
// would be indistinguishable from a bug.
func capabilityParitySuite() SuiteResult {
	return SuiteResult{
		Name:   "capability-parity",
		Status: "skipped",
		Detail: "capability-parity compares multiple providers of one capability — not meaningful for a single standalone target",
	}
}

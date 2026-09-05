package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// kitModulePath is the shared operator-kit module every conformant block
// must depend on (MIT-1466).
const kitModulePath = "github.com/mitstack-eu/operator-kit"

// kitVersionFloor is the minimum kit version every block must pin — the
// third copy of the ENFORCED floor scripts/check-operator-kit-pin.sh gates
// on and api/internal/scaffold's KitVersionFloor mirrors. This module
// cannot import that package (the kit must compile standalone, outside the
// monorepo), so a Go test holds this constant textually equal to
// scaffold.KitVersionFloor instead, and the two copies cannot drift apart
// silently. Bump it in the same commit that ships a deliberate floor raise,
// never as a side effect of a new kit tag landing.
const kitVersionFloor = "v0.1.0"

// kitShimFile is the thin binding every block must carry, byte-identical in
// spirit (though not diffed here — that check only makes sense across
// SEVERAL operators in this repo) to the kit's authentication/events seam.
const kitShimFile = "kitshim.go"

// kitOwnedFuncs are the symbols the kit owns — see
// scripts/check-operator-kit-pin.sh's kit_owned_funcs. A block that declares
// its own is diverging from the single implementation, whatever else its
// go.mod says.
var kitOwnedFuncs = []string{
	"verifyReplay",
	"verifyTimestampSkew",
	"verifyEd25519Sig",
	"publicKeyFromJWKS",
	"fetchJWKSKey",
	"MintOBO",
	"VerifyOBO",
	"appendRecentLog",
	"clearRecentLogs",
	"recentLogSnapshot",
	"EmitProgress",
	"EmitCompleted",
	"EmitFailed",
	"scrubEventDetail",
}

// trustPlumbingSuite reimplements scripts/check-operator-kit-pin.sh's checks
// against target ALONE, plus a standalone-only manifest.yaml presence check
// (repo mode has no equivalent need — a standalone target has no other
// suite positioned to catch a missing manifest). It is deliberately
// TEXTUAL/static, so it still produces a meaningful failure on a target
// whose module does not compile (e.g. because it redeclares a kit-owned
// symbol, which is itself a build error).
func trustPlumbingSuite(target string) SuiteResult {
	const name = "trust-plumbing"

	if detail, ok := checkKitDependency(target); !ok {
		return SuiteResult{name, "fail", detail}
	}
	if _, err := os.Stat(filepath.Join(target, kitShimFile)); err != nil {
		return SuiteResult{name, "fail", fmt.Sprintf("%s has no %s binding it to the kit", target, kitShimFile)}
	}
	if detail, ok := checkNoOwnedSymbolRedeclared(target); !ok {
		return SuiteResult{name, "fail", detail}
	}
	if _, err := os.Stat(filepath.Join(target, "manifest.yaml")); err != nil {
		return SuiteResult{name, "fail", fmt.Sprintf("%s is missing manifest.yaml", target)}
	}

	return SuiteResult{name, "pass", fmt.Sprintf(
		"depends on %s at or above the floor %s, %s present, no kit-owned symbol redeclared, manifest.yaml present",
		kitModulePath, kitVersionFloor, kitShimFile)}
}

var kitVersionRe = regexp.MustCompile(regexp.QuoteMeta(kitModulePath) + ` v(\d+\.\d+\.\d+)`)

// checkKitDependency requires target's go.mod to require kitModulePath at or
// above kitVersionFloor.
func checkKitDependency(target string) (detail string, ok bool) {
	raw, err := os.ReadFile(filepath.Join(target, "go.mod"))
	if err != nil {
		return fmt.Sprintf("%s has no go.mod to depend on the shared operator-kit module", target), false
	}
	content := string(raw)
	if !strings.Contains(content, kitModulePath) {
		return fmt.Sprintf("%s/go.mod does not depend on the shared operator-kit module (want require %s)", target, kitModulePath), false
	}

	m := kitVersionRe.FindStringSubmatch(content)
	if m != nil {
		have := "v" + m[1]
		if have != kitVersionFloor && versionLess(have, kitVersionFloor) {
			return fmt.Sprintf("%s pins kit %s, below the floor %s", target, have, kitVersionFloor), false
		}
	}
	return "", true
}

func versionLess(a, b string) bool {
	pa, pb := parseVersion(a), parseVersion(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func parseVersion(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	for i := 0; i < len(parts) && i < 3; i++ {
		n, _ := strconv.Atoi(parts[i])
		out[i] = n
	}
	return out
}

// checkNoOwnedSymbolRedeclared scans every non-test, non-shim .go file under
// target for a top-level declaration of a kit-owned symbol.
func checkNoOwnedSymbolRedeclared(target string) (detail string, ok bool) {
	type hit struct{ fn, file string }
	var hits []hit

	_ = filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == kitShimFile {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		content := string(raw)
		rel, err := filepath.Rel(target, path)
		if err != nil {
			rel = path
		}
		for _, fn := range kitOwnedFuncs {
			if funcDeclRe(fn).MatchString(content) {
				hits = append(hits, hit{fn: fn, file: rel})
			}
		}
		return nil
	})

	if len(hits) == 0 {
		return "", true
	}

	parts := make([]string, 0, len(hits))
	for _, h := range hits {
		parts = append(parts, fmt.Sprintf("%s() in %s", h.fn, h.file))
	}
	return fmt.Sprintf(
		"%s redeclares kit-owned symbol(s): %s — delete it and use the kit; a local copy silently diverges from the contract",
		target, strings.Join(parts, ", "),
	), false
}

func funcDeclRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^func (\([^)]*\) )?` + regexp.QuoteMeta(name) + `\(`)
}

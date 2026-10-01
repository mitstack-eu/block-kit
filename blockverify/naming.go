package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/mitstack-eu/operator-kit/handler"
)

// appNamePlaceholder is the text a derived-name template writes in place of
// the app name.
const appNamePlaceholder = "{appName}"

// namingSuite checks the app-name limit target's manifest declares at
// spec.naming.appName. Each derived name leaves its limit minus the length
// of its fixed text (the template with {appName} removed) for the app name,
// and maxLength must equal the smallest of those. A template with no
// {appName}, a template whose fixed text alone reaches its limit, and a
// maxLength with no derivedNames behind it all fail. A manifest that
// declares no spec.naming.appName is skipped.
func namingSuite(target string) SuiteResult {
	const name = "naming"

	path := filepath.Join(target, manifestFile)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return SuiteResult{name, "skipped", fmt.Sprintf(
			"%s has no %s (trust-plumbing reports that), so there is no app-name limit to check", target, manifestFile)}
	}
	if err != nil {
		return SuiteResult{name, "fail", fmt.Sprintf("could not read %s: %v", path, err)}
	}

	naming, err := handler.ManifestAppNameNaming(raw)
	if err != nil {
		return SuiteResult{name, "fail", path + ": " + err.Error()}
	}
	if naming == nil {
		return SuiteResult{name, "skipped",
			"manifest.yaml declares no spec.naming.appName, so there is no app-name limit to check"}
	}
	if len(naming.DerivedNames) == 0 {
		return SuiteResult{name, "fail", fmt.Sprintf(
			"%s: spec.naming.appName declares maxLength %d but no derivedNames, so no derived name backs it", path, naming.MaxLength)}
	}

	var problems []string
	computed, setBy := 0, ""
	for _, d := range naming.DerivedNames {
		if !strings.Contains(d.Template, appNamePlaceholder) {
			problems = append(problems, fmt.Sprintf("derived name %q has no %s placeholder", d.Template, appNamePlaceholder))
			continue
		}
		fixed := len(strings.ReplaceAll(d.Template, appNamePlaceholder, ""))
		if fixed >= d.Limit {
			problems = append(problems, fmt.Sprintf(
				"derived name %q has %d characters of fixed text, which reaches its limit of %d and leaves no room for the app name",
				d.Template, fixed, d.Limit))
			continue
		}
		if room := d.Limit - fixed; setBy == "" || room < computed {
			computed, setBy = room, d.Template
		}
	}
	if len(problems) > 0 {
		return SuiteResult{name, "fail", path + ": spec.naming.appName: " + strings.Join(problems, "; ")}
	}

	if naming.MaxLength != computed {
		return SuiteResult{name, "fail", fmt.Sprintf(
			"%s: spec.naming.appName declares maxLength %d, but the derived names allow an app name of at most %d, set by %q",
			path, naming.MaxLength, computed, setBy)}
	}
	return SuiteResult{name, "pass", fmt.Sprintf(
		"spec.naming.appName maxLength %d matches the derived names, set by %q", computed, setBy)}
}

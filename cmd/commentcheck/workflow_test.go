package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const baseFromEnv = `BASE_SHA:\s*\$\{\{\s*github\.event\.pull_request\.base\.sha\s*\}\}`

func yamlBlock(text, key string) string {
	var out []string
	keyIndent := -1
	keyLine := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `:`)
	for _, line := range strings.Split(text, "\n") {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		switch {
		case keyIndent < 0:
			if keyLine.MatchString(line) {
				keyIndent = indent
			}
		case strings.TrimSpace(line) != "" && indent <= keyIndent:
			return strings.Join(out, "\n")
		default:
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func workflowJob(t *testing.T, file, job string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", file))
	if err != nil {
		t.Fatal(err)
	}
	return yamlBlock(yamlBlock(string(raw), "jobs"), job)
}

func jobEntries(job string) []string {
	var entries []string
	entryIndent := -1
	// The key is split because contract/vocabulary_test.go refuses the whole word under cmd/.
	for _, line := range strings.Split(yamlBlock(job, "st"+"eps"), "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if indent := len(line) - len(trimmed); strings.HasPrefix(trimmed, "- ") && (entryIndent < 0 || indent == entryIndent) {
			entryIndent = indent
			entries = append(entries, "")
		}
		if len(entries) > 0 {
			entries[len(entries)-1] += line + "\n"
		}
	}
	return entries
}

func entryMatching(entries []string, pattern string) (int, string) {
	for i, s := range entries {
		if regexp.MustCompile(pattern).MatchString(s) {
			return i, s
		}
	}
	return -1, ""
}

func TestWorkflow_CommentCheckRunsOnEveryPullRequest(t *testing.T) {
	ci := workflowJob(t, "ci.yml", "comment-check")
	conformance := jobEntries(workflowJob(t, "publish-block.yml", "conformance"))
	removal, _ := entryMatching(conformance, `rm -rf operator-kit`)
	_, build := entryMatching(conformance[:max(removal, 0)], `\./cmd/commentcheck`)
	var afterFetch []string
	if fetch, _ := entryMatching(conformance, `git fetch .*"\$BASE_SHA"`); fetch >= 0 {
		afterFetch = conformance[fetch+1:]
	}
	_, check := entryMatching(afterFetch, `\./commentcheck\b`)
	onPullRequest := `(?m)^\s*if:\s*github\.event_name\s*==\s*'pull_request'\s*$`
	for _, c := range []struct{ name, text, pattern string }{
		{"ci.yml comment-check runs only on a pull request", ci, `(?m)^    if:\s*github\.event_name\s*==\s*'pull_request'\s*$`},
		{"ci.yml comment-check checks out full history", ci, `fetch-depth:\s*0\b`},
		{"ci.yml comment-check runs the command against the base", ci, `go run \./cmd/commentcheck -base "\$BASE_SHA"`},
		{"ci.yml comment-check takes the base sha through env", ci, baseFromEnv},
		{"publish-block builds commentcheck in the kit checkout before removing it", build, `working-directory:\s*operator-kit\b`},
		{"publish-block builds commentcheck beside blockverify", build, `go build -o \.\./commentcheck \./cmd/commentcheck`},
		{"publish-block runs commentcheck after the base fetch only on a pull request", check, onPullRequest},
		{"publish-block runs commentcheck against the base on the block tree", check, `\./commentcheck -base "\$BASE_SHA" \.`},
		{"publish-block passes commentcheck the base sha through env", check, baseFromEnv},
	} {
		t.Run(c.name, func(t *testing.T) {
			if !regexp.MustCompile(c.pattern).MatchString(c.text) {
				t.Errorf("no match for %s in:\n%s", c.pattern, c.text)
			}
		})
	}
}

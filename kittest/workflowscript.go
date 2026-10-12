package kittest

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The kit is stdlib-only, so ReadWorkflowScript reads a narrow subset of YAML by
// indentation and refuses any other shape rather than guess at it.

// WorkflowScript is the shell body and environment of one step in a rendered GitHub Actions workflow.
type WorkflowScript struct {
	Run string
	Env map[string]string
}

// ScriptRun is what a test supplies to RunWorkflowScript.
type ScriptRun struct {
	// Fakes maps a command name to the bash script that stands in for it on PATH.
	Fakes map[string]string
	// Tools names real commands, resolved on the test's own PATH, that the body may also call.
	Tools []string
	// Env is added to the step's env, and wins over it.
	Env map[string]string
	// Dir is the working directory; empty means a fresh temporary one.
	Dir string
}

// ScriptResult is how a step body ended.
type ScriptResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

type yamlLine struct {
	num    int
	indent int
	text   string
}

func (l yamlLine) blank() bool   { return strings.TrimSpace(l.text) == "" }
func (l yamlLine) comment() bool { return strings.HasPrefix(strings.TrimSpace(l.text), "#") }

type yamlEntry struct {
	key, value string
	line       yamlLine
	children   []yamlLine
}

// ReadWorkflowScript returns the run body and env of the step named step in job, which is a key under jobs:.
func ReadWorkflowScript(workflow []byte, job, step string) (WorkflowScript, error) {
	var lines []yamlLine
	for i, text := range strings.Split(strings.TrimSuffix(string(workflow), "\n"), "\n") {
		if strings.HasPrefix(text, "\t") {
			return WorkflowScript{}, fmt.Errorf("line %d is indented by a tab, which YAML forbids", i+1)
		}
		lines = append(lines, yamlLine{num: i + 1, indent: len(text) - len(strings.TrimLeft(text, " ")), text: text})
	}

	top, err := mappingEntries(lines)
	if err != nil {
		return WorkflowScript{}, err
	}
	jobsEntry, ok := findEntry(top, "jobs")
	if !ok {
		return WorkflowScript{}, errors.New("the workflow has no top-level jobs: key")
	}
	jobs, err := mappingEntries(jobsEntry.children)
	if err != nil {
		return WorkflowScript{}, err
	}
	jobEntry, ok := findEntry(jobs, job)
	if !ok {
		return WorkflowScript{}, fmt.Errorf("job %q not found; jobs: %s", job, strings.Join(entryKeys(jobs), ", "))
	}
	jobFields, err := mappingEntries(jobEntry.children)
	if err != nil {
		return WorkflowScript{}, err
	}
	stepsEntry, ok := findEntry(jobFields, "steps")
	if !ok {
		return WorkflowScript{}, fmt.Errorf("job %q has no steps: key", job)
	}
	items, err := sequenceItems(stepsEntry.children)
	if err != nil {
		return WorkflowScript{}, err
	}

	var names []string
	var matches [][]yamlEntry
	for _, item := range items {
		fields, err := mappingEntries(item)
		if err != nil {
			return WorkflowScript{}, err
		}
		nameEntry, ok := findEntry(fields, "name")
		if !ok {
			continue
		}
		name, err := scalar(nameEntry)
		if err != nil {
			return WorkflowScript{}, err
		}
		names = append(names, name)
		if name == step {
			matches = append(matches, fields)
		}
	}
	switch len(matches) {
	case 0:
		return WorkflowScript{}, fmt.Errorf("step %q not found in job %q; steps: %s", step, job, strings.Join(names, ", "))
	case 1:
	default:
		return WorkflowScript{}, fmt.Errorf("job %q has %d steps named %q", job, len(matches), step)
	}

	fields := matches[0]
	if shellEntry, ok := findEntry(fields, "shell"); ok {
		shell, err := scalar(shellEntry)
		if err != nil {
			return WorkflowScript{}, err
		}
		if shell != "bash" {
			return WorkflowScript{}, fmt.Errorf("line %d: step %q runs under shell %q, and the runner runs bash only", shellEntry.line.num, step, shell)
		}
	}
	runEntry, ok := findEntry(fields, "run")
	if !ok {
		return WorkflowScript{}, fmt.Errorf("step %q in job %q has no run: body", step, job)
	}
	run, err := scalar(runEntry)
	if err != nil {
		return WorkflowScript{}, err
	}
	env := map[string]string{}
	if envEntry, ok := findEntry(fields, "env"); ok {
		if v := stripComment(envEntry.value); v != "" {
			return WorkflowScript{}, fmt.Errorf("line %d: env must be a block mapping, not the flow or inline value %q", envEntry.line.num, v)
		}
		vars, err := mappingEntries(envEntry.children)
		if err != nil {
			return WorkflowScript{}, err
		}
		for _, v := range vars {
			if env[v.key], err = scalar(v); err != nil {
				return WorkflowScript{}, err
			}
		}
	}
	return WorkflowScript{Run: run, Env: env}, nil
}

// mappingEntries splits lines into the entries of the block mapping they hold, each with the lines nested under it.
func mappingEntries(lines []yamlLine) ([]yamlEntry, error) {
	indent := -1
	var entries []yamlEntry
	for _, l := range lines {
		if l.blank() || l.comment() {
			if len(entries) > 0 {
				entries[len(entries)-1].children = append(entries[len(entries)-1].children, l)
			}
			continue
		}
		if indent < 0 {
			indent = l.indent
		}
		if l.indent > indent || (l.indent == indent && isSequenceLine(l) && len(entries) > 0) {
			entries[len(entries)-1].children = append(entries[len(entries)-1].children, l)
			continue
		}
		if l.indent < indent {
			return nil, fmt.Errorf("line %d is less indented than the mapping it is in", l.num)
		}
		body := l.text[l.indent:]
		if strings.HasPrefix(body, "\t") {
			return nil, fmt.Errorf("line %d indents its key with a tab, which YAML forbids", l.num)
		}
		key, value, ok := splitKey(body)
		if !ok {
			return nil, fmt.Errorf("line %d is not a key: value pair: %q", l.num, strings.TrimSpace(body))
		}
		if _, dup := findEntry(entries, key); dup {
			return nil, fmt.Errorf("line %d: key %s is given twice in one mapping", l.num, key)
		}
		entries = append(entries, yamlEntry{key: key, value: value, line: l})
	}
	return entries, nil
}

// sequenceItems splits lines into the items of the block sequence they hold, with each dash blanked so an item reads as a mapping.
func sequenceItems(lines []yamlLine) ([][]yamlLine, error) {
	indent := -1
	var items [][]yamlLine
	for _, l := range lines {
		if l.blank() || l.comment() {
			if len(items) > 0 {
				items[len(items)-1] = append(items[len(items)-1], l)
			}
			continue
		}
		if indent < 0 {
			indent = l.indent
		}
		if l.indent == indent && isSequenceLine(l) {
			l.text = l.text[:l.indent] + " " + l.text[l.indent+1:]
			items = append(items, []yamlLine{})
		} else if l.indent <= indent {
			return nil, fmt.Errorf("line %d is not a sequence item: %q", l.num, strings.TrimSpace(l.text))
		}
		l.indent = len(l.text) - len(strings.TrimLeft(l.text, " "))
		items[len(items)-1] = append(items[len(items)-1], l)
	}
	return items, nil
}

func isSequenceLine(l yamlLine) bool {
	body := l.text[l.indent:]
	return body == "-" || strings.HasPrefix(body, "- ")
}

func splitKey(body string) (key, value string, ok bool) {
	for i := 0; i < len(body); i++ {
		if body[i] == ':' && (i+1 == len(body) || body[i+1] == ' ') {
			return unquoteKey(strings.TrimSpace(body[:i])), strings.TrimSpace(body[i+1:]), true
		}
	}
	return "", "", false
}

func unquoteKey(key string) string {
	if v, err := quoted(key); err == nil {
		return v
	}
	return key
}

func findEntry(entries []yamlEntry, key string) (yamlEntry, bool) {
	for _, e := range entries {
		if e.key == key {
			return e, true
		}
	}
	return yamlEntry{}, false
}

func entryKeys(entries []yamlEntry) []string {
	keys := make([]string, len(entries))
	for i, e := range entries {
		keys[i] = e.key
	}
	return keys
}

func stripComment(value string) string {
	if strings.HasPrefix(value, "#") {
		return ""
	}
	if i := strings.Index(value, " #"); i >= 0 {
		value = value[:i]
	}
	return strings.TrimSpace(value)
}

// scalar reads an entry's value as a single-line plain or quoted scalar or as a literal block.
func scalar(e yamlEntry) (string, error) {
	nested := false
	for _, c := range e.children {
		if !c.blank() && !c.comment() {
			nested = true
		}
	}
	v := e.value
	switch {
	case strings.HasPrefix(v, "|"):
		return literal(e)
	case strings.HasPrefix(v, ">"):
		return "", fmt.Errorf("line %d: %s is a folded scalar, which this reader does not support", e.line.num, e.key)
	case strings.HasPrefix(v, "*"):
		return "", fmt.Errorf("line %d: %s is an alias, which this reader does not resolve", e.line.num, e.key)
	case v != "" && strings.ContainsRune("{[&!%@`", rune(v[0])):
		return "", fmt.Errorf("line %d: %s is a flow, anchored or tagged value, which this reader does not support", e.line.num, e.key)
	case nested:
		return "", fmt.Errorf("line %d: %s is a multi-line or nested value, which this reader does not support", e.line.num, e.key)
	case strings.HasPrefix(v, `"`) || strings.HasPrefix(v, "'"):
		s, err := quoted(v)
		if err != nil {
			return "", fmt.Errorf("line %d: %s: %w", e.line.num, e.key, err)
		}
		return s, nil
	}
	plain := stripComment(v)
	if plain == "~" || strings.EqualFold(plain, "null") {
		return "", fmt.Errorf("line %d: %s is a YAML null, which this reader does not read as a value", e.line.num, e.key)
	}
	return plain, nil
}

// quoted reads a single-line quoted scalar, followed by nothing but an optional comment.
func quoted(v string) (string, error) {
	if len(v) < 2 {
		return "", errors.New("unterminated quoted scalar")
	}
	end := -1
	if v[0] == '\'' {
		for i := 1; i < len(v); i++ {
			if v[i] == '\'' {
				if i+1 < len(v) && v[i+1] == '\'' {
					i++
					continue
				}
				end = i
				break
			}
		}
	} else {
		for i := 1; i < len(v); i++ {
			if v[i] == '\\' {
				i++
				continue
			}
			if v[i] == '"' {
				end = i
				break
			}
		}
	}
	if end < 0 || stripComment(v[end+1:]) != "" {
		return "", fmt.Errorf("quoted scalar %s does not close on its line", v)
	}
	if v[0] == '\'' {
		return strings.ReplaceAll(v[1:end], "''", "'"), nil
	}
	s, err := strconv.Unquote(v[:end+1])
	if err != nil {
		return "", fmt.Errorf("double-quoted scalar %s uses an escape this reader does not support", v[:end+1])
	}
	return s, nil
}

// literal reads a | block scalar, with its clip, strip or keep chomping.
func literal(e yamlEntry) (string, error) {
	header := stripComment(e.value)
	chomp := strings.TrimPrefix(header, "|")
	if chomp != "" && chomp != "-" && chomp != "+" {
		return "", fmt.Errorf("line %d: %s carries an indentation indicator, which this reader does not support", e.line.num, e.key)
	}
	indent := -1
	var body []string
	for i, c := range e.children {
		if c.blank() {
			body = append(body, "")
			continue
		}
		if indent < 0 {
			indent = c.indent
		}
		if c.indent < indent {
			for _, rest := range e.children[i:] {
				if !rest.blank() && !rest.comment() {
					return "", fmt.Errorf("line %d is less indented than the %s body above it", rest.num, e.key)
				}
			}
			break
		}
		body = append(body, c.text[indent:])
	}
	content := strings.Join(body, "\n")
	switch chomp {
	case "-":
		return strings.TrimRight(content, "\n"), nil
	case "+":
		return content + "\n", nil
	}
	if strings.TrimRight(content, "\n") == "" {
		return "", nil
	}
	return strings.TrimRight(content, "\n") + "\n", nil
}

// RunWorkflowScript runs the step body under bash -euo pipefail, with a PATH holding only the fakes and tools the test names and no ambient environment.
// The body runs as written: a ${{ }} expression in it is not evaluated, so a test substitutes one before the run.
func RunWorkflowScript(t testing.TB, step WorkflowScript, in ScriptRun) ScriptResult {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("RunWorkflowScript: no bash on PATH: %v", err)
	}
	bin := t.TempDir()
	for name, script := range in.Fakes {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!"+bash+"\n"+script+"\n"), 0o755); err != nil {
			t.Fatalf("RunWorkflowScript: writing fake %s: %v", name, err)
		}
	}
	for _, name := range in.Tools {
		resolved, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("RunWorkflowScript: tool %s is not on the test's PATH: %v", name, err)
		}
		if err := os.Symlink(resolved, filepath.Join(bin, name)); err != nil {
			t.Fatalf("RunWorkflowScript: linking tool %s: %v", name, err)
		}
	}
	scratch := t.TempDir()
	script := filepath.Join(scratch, "step.sh")
	if err := os.WriteFile(script, []byte(step.Run), 0o600); err != nil {
		t.Fatalf("RunWorkflowScript: writing the step body: %v", err)
	}
	dir := in.Dir
	if dir == "" {
		dir = t.TempDir()
	}

	vars := map[string]string{"PATH": bin, "HOME": scratch}
	for k, v := range step.Env {
		vars[k] = v
	}
	for k, v := range in.Env {
		vars[k] = v
	}
	env := make([]string, 0, len(vars))
	for k, v := range vars {
		env = append(env, k+"="+v)
	}

	cmd := exec.Command(bash, "--noprofile", "--norc", "-euo", "pipefail", script)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	res := ScriptResult{}
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("RunWorkflowScript: starting bash: %v", err)
		}
		res.ExitCode = exit.ExitCode()
	}
	res.Stdout, res.Stderr = stdout.String(), stderr.String()
	return res
}

package main

// git.go wraps the one git operation Run needs: reading every commit's
// subject and body in a base..head range. Mirrors
// cmd/board-delivery/git.go's idiom, minus the anchor-tag machinery this
// command has no use for — pr-keys only ever reads a range, and never
// writes to the repository.

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// commitFieldSep separates a commit's subject from its body in git log's
// custom format. commitRecordSep separates one commit's record from the
// next. Neither byte appears in ordinary commit text.
const (
	commitFieldSep  = "\x1f"
	commitRecordSep = "\x1e"
)

// gitOutput runs git in repo and returns its trimmed combined output.
func gitOutput(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// commitsInRange reads every commit's subject and body in rng.
func commitsInRange(repo, rng string) ([]commit, error) {
	format := "%s" + commitFieldSep + "%b" + commitRecordSep
	out, err := gitOutput(repo, "log", "--format="+format, rng, "--")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var commits []commit
	for _, rec := range strings.Split(out, commitRecordSep) {
		rec = strings.TrimPrefix(rec, "\n")
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, commitFieldSep, 2)
		c := commit{Subject: parts[0]}
		if len(parts) == 2 {
			c.Body = parts[1]
		}
		commits = append(commits, c)
	}
	return commits, nil
}

package main

// git.go wraps the handful of git operations Run needs: reading the
// board-synced anchor tag, reading every commit's subject and body in a
// resolved range, and advancing and pushing the anchor tag after a
// successful board write.

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// anchorTag is the local git tag that anchors range resolution: the sha of
// the last commit whose keys reached the board.
const anchorTag = "board-synced"

// commitFieldSep separates a commit's subject from its body in git log's
// custom format. commitRecordSep separates one commit's record from the
// next. Neither byte appears in ordinary commit text.
const (
	commitFieldSep  = "\x1f"
	commitRecordSep = "\x1e"
)

// gitOutput runs git in repo and returns its trimmed combined output. Named
// distinctly from the test fixture's own runGit helper, which takes a
// *testing.T and lives in the same package.
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

// readAnchorTag returns the sha the anchor tag points at in repo, or ""
// when the tag does not exist — an absent tag is a legitimate first-run
// state, never an error.
func readAnchorTag(repo string) string {
	sha, err := gitOutput(repo, "rev-parse", "-q", "--verify", "refs/tags/"+anchorTag+"^{commit}")
	if err != nil {
		return ""
	}
	return sha
}

// commit is one commit's subject and body, the shape boardkeys.Message reads.
type commit struct {
	Subject string
	Body    string
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

// advanceAnchorTag force-moves the local anchor tag to sha and pushes it to
// remote. Called only after a successful board write, since the tag's
// meaning is "the last commit whose keys reached the board" — advancing it
// on a failed write would strand those keys' commits outside every future
// run's range.
func advanceAnchorTag(repo, remote, sha string) error {
	if _, err := gitOutput(repo, "tag", "-f", anchorTag, sha); err != nil {
		return err
	}
	if _, err := gitOutput(repo, "push", "-f", remote, "refs/tags/"+anchorTag); err != nil {
		return err
	}
	return nil
}

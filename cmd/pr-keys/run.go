package main

// run.go is the CLI seam: Run(args, stdout) does the whole command's work
// and returns its process exit code, so the command is testable without a
// real process. main.go's only job is wiring Run to the real process
// streams. Follows cmd/board-delivery/run.go's idiom.

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// Run reads the commits in --base..--head and checks whether that range and
// --title together deliver a board key. It returns 2 on a missing or bad
// flag, 1 when the check fails (its error, and how to fix it, printed to
// stdout), and 0 on pass. Every message goes to stdout — Run never writes to
// the real process streams directly, so a caller can capture and assert on
// it.
func Run(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("pr-keys", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	repo := fs.String("repo", ".", "the git repository to read commits from")
	base := fs.String("base", "", "the pull request's base sha")
	head := fs.String("head", "", "the pull request's head sha")
	title := fs.String("title", "", "the pull request's title (default: the PR_TITLE environment variable)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}
	if *base == "" {
		fmt.Fprintln(stdout, "pr-keys: --base is required")
		return 2
	}
	if *head == "" {
		fmt.Fprintln(stdout, "pr-keys: --head is required")
		return 2
	}
	if *title == "" {
		*title = os.Getenv("PR_TITLE")
	}

	rng := *base + ".." + *head
	commits, err := commitsInRange(*repo, rng)
	if err != nil {
		fmt.Fprintf(stdout, "pr-keys: reading commits in range %s: %v\n", rng, err)
		return 1
	}

	if err := check(*title, commits); err != nil {
		fmt.Fprintln(stdout, err)
		fmt.Fprintln(stdout, "pr-keys: put the key in a leading \"MIT-###:\" list in a commit subject (or a Jira: trailer line) and in the pull request's title")
		return 1
	}

	fmt.Fprintf(stdout, "pr-keys: %s and the title both carry a board key\n", rng)
	return 0
}

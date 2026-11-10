// Command checkpolls fails when a change adds a line that sleeps or polls on a fixed interval, judging only the lines the working tree adds over -base.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const usage = `Usage: checkpolls -base REV

checkpolls compares the working tree with REV and fails when a line it adds
holds a fixed sleep or a polling interval:

  Go, in .go and .go.tmpl files: a time.Sleep, time.Tick or time.NewTicker call
  TypeScript and JavaScript: a setInterval call or a refetchInterval option
  shell, in any file but prose: sleep with a number or a variable, run as a
    command in a script, a workflow's run block or a quoted string

A wait proceeds on the signal its upstream sends. Where the upstream sends
none, an operator verb answers pending and the engine asks again later,
backing off between asks, and other code calls the waiter its upstream's
client provides, so a change adds no timer of its own.

Only added lines are judged, so a poll the base already holds passes until a
change rewrites its line. A line that opens with a comment marker, // or a
shell #, runs nothing and is not judged. A quoted string that opens with sleep
and a number is judged as a command, so word a message another way.

It cannot see a timer reset inside a loop, a time.After in a select, a new
runEvery loop in the engine or a cron schedule. Review judges those against
the rule.

Exit 0 when no added line sleeps or polls, 1 when one does, 2 on a usage or
git error.

`

const advice = "A wait proceeds on the signal its upstream sends. Where it sends none, an operator verb answers pending and the engine asks again later, " +
	"and other code calls the waiter its upstream's client provides, as block-kit's docs/maintainers.md explains."

func main() {
	os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr))
}

func Run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("checkpolls", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprint(stderr, usage)
		flags.PrintDefaults()
	}
	base := flags.String("base", "", "the commit the working tree is compared with")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "checkpolls: takes no path, since it judges the whole change: %s\n", strings.Join(flags.Args(), " "))
		return 2
	}
	if *base == "" {
		fmt.Fprintln(stderr, "checkpolls: -base is required")
		return 2
	}
	lines, err := addedLines(*base)
	if err != nil {
		fmt.Fprintln(stderr, "checkpolls:", err)
		return 2
	}
	return report(stdout, lines)
}

func report(stdout io.Writer, lines []addedLine) int {
	files := map[string]bool{}
	found := 0
	for _, l := range lines {
		files[l.path] = true
		if f, ok := match(l); ok {
			fmt.Fprintf(stdout, "%s:%d: adds %s: %s\n", l.path, l.number, f.label, strings.TrimSpace(l.text))
			found++
		}
	}
	if found > 0 {
		fmt.Fprintf(stdout, "checkpolls: found %d added %s holding a fixed sleep or a polling interval. %s\n", found, plural(found, "line"), advice)
		return 1
	}
	fmt.Fprintf(stdout, "checkpolls: judged %d added %s in %d %s, and none sleeps or polls on a timer\n",
		len(lines), plural(len(lines), "line"), len(files), plural(len(files), "file"))
	return 0
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

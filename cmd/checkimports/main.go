// Command checkimports fails when a file of a Go module, or of a module nested under it, imports the core module or a module whose own manifest.yaml says type: block or type: capability.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const usage = `Usage: checkimports [DIR]

checkimports judges the Go module at DIR, or at the current directory, and
every module nested under it. It fails when one of their files imports a
package of:

  the core module github.com/mitstack/api
  a module whose own manifest.yaml says type: block or type: capability

A kit or an operator never builds on the core or on a block. An operator
reaches mitstack over its HTTP API and reads another block's outcome from the
outputs of a requirement its manifest declares, and code two blocks share
belongs in a kit.

A module's type is the value of the top-level type key in the manifest.yaml
at its root, read past a byte-order mark, quotes and a trailing comment. That
line places a repository in its layer, so no list of blocks is kept here. A
module with no manifest.yaml, such as a kit, is neither a block nor a
capability.

Every Go file of each module is judged, tests included. The walk skips the
directories the go command skips: testdata, vendor, and any whose name starts
with . or _. A file the default build constraints leave out is checked against
the core module. Its other imports are checked against a block's module only
when go list loaded that package for another file, and the pass line counts
the rest.

Exit 0 when no import is refused, 1 when one is, and 2 on a usage error, when
go list fails or cannot load a package of a module or one of its tests, when
an import's module cannot be found, or when a manifest's type line holds no
plain value, such as a list or an anchor.

`

const (
	coreAdvice  = "A kit or an operator never builds on the core: delete the import and reach mitstack over its HTTP API."
	blockAdvice = "A kit or an operator never builds on a block: delete the import, read another block's outcome from the outputs of a requirement your manifest declares, and move code two blocks share into a kit."
)

func main() {
	os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr))
}

func Run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("checkimports", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprint(stderr, usage)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 1 {
		fmt.Fprintf(stderr, "checkimports: judges one module and the modules nested under it, so it takes one DIR at most: %s\n", strings.Join(flags.Args(), " "))
		return 2
	}
	root := "."
	if flags.NArg() == 1 {
		root = flags.Arg(0)
	}
	dirs, err := findModules(root)
	if err != nil {
		fmt.Fprintln(stderr, "checkimports: finding the modules under", root+":", err)
		return 2
	}
	code := 0
	for _, dir := range dirs {
		code = max(code, judgeModule(root, dir, stdout, stderr))
	}
	return code
}

func judgeModule(root, dir string, stdout, stderr io.Writer) int {
	prefix, err := filepath.Rel(root, dir)
	if err != nil {
		fmt.Fprintln(stderr, "checkimports:", err)
		return 2
	}
	mod, err := listModule(dir)
	if err != nil {
		fmt.Fprintln(stderr, "checkimports:", err)
		return 2
	}
	result, err := judge(mod, prefix)
	if err != nil {
		fmt.Fprintln(stderr, "checkimports:", err)
		return 2
	}
	return report(stdout, stderr, mod.path, result)
}

func report(stdout, stderr io.Writer, modulePath string, r judgement) int {
	for _, u := range r.unresolved {
		fmt.Fprintf(stderr, "%s:%d: cannot find the module of %s: %s. Run go mod download in the module, or require the module in its go.mod.\n", u.file, u.line, u.importPath, u.reason)
	}
	for _, f := range r.refused {
		if f.module == coreModule {
			fmt.Fprintf(stdout, "%s:%d: imports %s, a package of the core module %s. %s\n", f.file, f.line, f.importPath, coreModule, coreAdvice)
			continue
		}
		fmt.Fprintf(stdout, "%s:%d: imports %s from %s, whose manifest.yaml says type: %s. %s\n", f.file, f.line, f.importPath, f.module, f.moduleType, blockAdvice)
	}
	if len(r.refused) > 0 {
		files := map[string]bool{}
		for _, f := range r.refused {
			files[f.file] = true
		}
		fmt.Fprintf(stdout, "checkimports: found %d refused %s in %d %s of %s.\n", len(r.refused), plural(len(r.refused), "import"), len(files), plural(len(files), "file"), modulePath)
	}
	switch {
	case len(r.unresolved) > 0:
		return 2
	case len(r.refused) > 0:
		return 1
	}
	fmt.Fprintf(stdout, "checkimports: judged %d %s in %d %s of %s, and none reaches the core module or a block's module",
		r.imports, plural(r.imports, "import"), r.files, plural(r.files, "file"), modulePath)
	if r.unjudged > 0 {
		pronoun := "it"
		if r.unjudged > 1 {
			pronoun = "them"
		}
		fmt.Fprintf(stdout, "; %d %s in files the default build leaves out could only be checked against the core module, since go list loaded no package for %s",
			r.unjudged, plural(r.unjudged, "import"), pronoun)
	}
	fmt.Fprintln(stdout)
	return 0
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

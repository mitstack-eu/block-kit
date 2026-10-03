// Command catalogue-golden prints the resource-type catalogue this kit
// release ships, as contract.MarshalCatalogue writes it: each type's ID and
// kind, its inputs with their required marks and its outputs with their
// secret marks.
//
// Usage:
//
//	go run ./cmd/catalogue-golden > contract/goldens/catalogue/resourcetypes.json
//
// It exists so the catalogue golden, which the contract tests read as the
// previous release's catalogue when they look for a breaking change, is
// generated from the catalogue rather than written by hand.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/mitstack-eu/operator-kit/contract"
)

func main() {
	os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr))
}

// Run prints the current catalogue to stdout and returns 0. It takes no
// argument: given any, it writes a usage line to stderr and returns 2. It
// returns 1 when stdout cannot be written.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: catalogue-golden > contract/goldens/catalogue/resourcetypes.json (it takes no argument)")
		return 2
	}
	if _, err := stdout.Write(contract.MarshalCatalogue(contract.CurrentCatalogue())); err != nil {
		fmt.Fprintf(stderr, "catalogue-golden: %v\n", err)
		return 1
	}
	return 0
}

// Command core-importer is a block that imports the platform's core module, which the import check refuses.
package main

import (
	"fmt"

	"github.com/mitstack/api/contract"
)

func main() {
	fmt.Println(contract.Verb)
}

// Command block-importer is a block that imports another block's module, which the import check refuses.
package main

import (
	"fmt"

	"example.com/other-block/outputs"
)

func main() {
	fmt.Println(outputs.Address)
}

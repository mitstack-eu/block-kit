package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Printf("%x\n", digest([]byte(os.Args[0])))
	openpgpCheck()
}

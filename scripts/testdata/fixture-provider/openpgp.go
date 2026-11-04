//go:build openpgp

package main

import (
	"fmt"
	"strings"

	"golang.org/x/crypto/openpgp"
)

func openpgpCheck() {
	_, err := openpgp.ReadArmoredKeyRing(strings.NewReader(""))
	fmt.Println(err)
}

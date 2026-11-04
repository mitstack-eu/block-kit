//go:build !nocrypto

package main

import "golang.org/x/crypto/blake2b"

func digest(b []byte) [32]byte { return blake2b.Sum256(b) }

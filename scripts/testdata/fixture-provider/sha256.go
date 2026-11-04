//go:build nocrypto

package main

import "crypto/sha256"

func digest(b []byte) [32]byte { return sha256.Sum256(b) }

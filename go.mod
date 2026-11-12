// block-kit is the mitstack block-operator contract, kept as one
// implementation every operator imports rather than as copies kept in sync
// by hand. It requires nothing beyond the standard library, with one
// exception, golang.org/x/crypto and the golang.org/x/sys it builds on:
// hand-written cryptography is a security risk, and this library is the Go
// team's own.
module github.com/mitstack-eu/block-kit

go 1.26.0

require golang.org/x/crypto v0.59.0

require golang.org/x/sys v0.50.0 // indirect

// operator-kit — the mitstack block-operator contract, as ONE implementation
// (MIT-1466). Before this module, `auth.go`/`ed25519verify.go`/`obo.go`/
// `logs.go`/`events.go` were kept correct by being BYTE-IDENTICAL copies in
// every operator, enforced by scripts/check-operator-auth-parity.sh. That gate
// did real work — no operator's trust check ever silently diverged — but it
// could not reach an author outside this repo, could not carry logic (only
// files), and made every contract fix cost 13 edits.
//
// Module path note: this is `github.com/mitstack-eu/mitstack/...` (the real
// repository path) rather than the `github.com/mitstack/examples/...` path the
// operators themselves use, so that the import path is genuinely resolvable the
// day the repository is public. Until then in-repo consumers reach it with a
// `replace` directive, which is also what keeps CI free of any network fetch or
// private-module credential.
module github.com/mitstack-eu/operator-kit

go 1.25

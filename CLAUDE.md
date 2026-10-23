# CLAUDE.md — block-kit

Module path: `github.com/mitstack-eu/block-kit`.

The kit is stdlib-only by design. No third-party dependency is ever added,
however convenient one looks for a single call site.

Test with `go vet ./... && go test ./...`. The board module under `board/`,
the tooling for mitstack's work tracker, has its own `go.mod`, and you test it
with `go -C board vet ./... && go -C board test ./...`. Its `go.mod` pins the
kit at a commit of main, so bump that pin by hand when the board module needs
newer kit code.

The first tag, v0.4.0, is cut by hand. After it, every push to main that
changes the tree outside `board/` cuts the next patch tag, and a
`kit-release: minor` line in a commit message raises the minor level instead
(`scripts/release-block-kit.sh`).
A breaking change takes the minor raise. No commit carries
`kit-release: major`, because that raise cuts v1.0.0, which is kept for the
commercial launch.

Commit subjects carry the `MIT-###:` key of the ticket they deliver.

A change under `contract/` is a wire-contract change: the platform must pick
it up by bumping its pin on this module, not by copying the file.

# CLAUDE.md — block-kit

Module path: `github.com/mitstack-eu/block-kit`.

The kit is stdlib-only by design. No third-party dependency is ever added,
however convenient one looks for a single call site.

Test with `go vet ./... && go test ./...`.

The example block under `examples/hetzner-firewall` is a module of its own,
built against this checkout. Test it with
`go -C examples/hetzner-firewall test ./...` and
`go run ./verify/blockverify --standalone examples/hetzner-firewall`. The
README shows its `manifest.yaml` and `block.go` whole, and a test in
`examples/` fails when the README and those files differ.

The root holds only the packages every block imports, `contract`, `handler`,
`observe` and `webhook`. An optional helper tied to no provider goes in
`helpers/`, and one for a single provider in `capabilities/<provider>/`.

The first tag, v0.4.0, is cut by hand. After it, every push to main that
changes the tree cuts the next patch tag, and a
`kit-release: minor` line in a commit message raises the minor level instead
(`scripts/release-block-kit.sh`).
A breaking change takes the minor raise. No commit carries
`kit-release: major`, because that raise cuts v1.0.0, which is kept for the
commercial launch.

Commit subjects carry the `MIT-###:` key of the ticket they deliver.

A change under `contract/` is a wire-contract change: the platform must pick
it up by bumping its pin on this module, not by copying the file.

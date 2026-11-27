# CLAUDE.md — block-kit

Module path: `github.com/mitstack-eu/block-kit`.

The kit is stdlib-only by design. No third-party dependency is ever added,
however convenient one looks for a single call site. There are two
exceptions.

- `golang.org/x/crypto`, which `helpers/age` seals with, and the
  `golang.org/x/sys` it builds on. Hand-written cryptography is a security
  risk, and this library is the Go team's own.
- The AWS SDK for Go, in the nested module `capabilities/s3store` alone. It is
  the S3-protocol client the kit uses for S3-compatible stores such as
  Scaleway, and it stays out of the main module.

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
A breaking change takes the minor raise. While the newest tag is a v0
version, the script refuses a `kit-release: major` line, and the pull
request's `test` check fails on it. v1.0.0 is cut by hand at the commercial
launch.

Commit subjects carry the `MIT-###:` key of the ticket they deliver.

A change under `contract/` is a wire-contract change: the platform must pick
it up by bumping its pin on this module, not by copying the file.

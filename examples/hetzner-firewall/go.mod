module github.com/mitstack-eu/block-kit/examples/hetzner-firewall

go 1.25

require github.com/mitstack-eu/block-kit v0.4.0

// This line builds the example against the kit beside it; a copy deletes it and runs go mod tidy.
replace github.com/mitstack-eu/block-kit => ../..

// rescuessh is the Robot rescue system's SSH session, a module of its own so that block-kit's main module stays stdlib-only.
module github.com/mitstack-eu/block-kit/capabilities/hetzner/robot/rescuessh

go 1.26.0

require golang.org/x/crypto v0.34.0

require golang.org/x/sys v0.49.0 // indirect

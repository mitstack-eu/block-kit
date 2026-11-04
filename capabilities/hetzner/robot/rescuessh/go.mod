// rescuessh is the Robot rescue system's SSH session, a module of its own so that block-kit's main module stays stdlib-only.
module github.com/mitstack-eu/block-kit/capabilities/hetzner/robot/rescuessh

go 1.26.0

require golang.org/x/crypto v0.58.1-0.20261009181718-2f1f834330b2

require golang.org/x/sys v0.49.0 // indirect

// Command blockprov, part of mitstack's own tooling, is the CLI a block's publish workflow calls to build, verify and push its provenance record, read it back and post it to the platform.
package main

import (
	"fmt"
	"io"
)

const usageMessage = `blockprov: builds, pushes and posts a block's publisher-signed provenance record.

Usage:
  blockprov build   --manifest <path> \
                    (--report <blockverify --json report> | --block-verify <block-verify -test.v output>) \
                    --block-id <id> --block-version <v> \
                    --image-ref <ref> --image-digest sha256:... \
                    --kit-version <v> --repository <owner/name> --commit <sha> \
                    --run-url <url> --token-url <url> --token-request-token <tok> \
                    --out <record.json>
  blockprov verify  --record <record.json> --jwks <path-or-url> --repository <owner/name>
  blockprov push    --record <record.json> --registry <host> --repo <name> \
                    --image-digest sha256:... --username <user> \
                    (--password <pass> | --password-env <ENV_VAR>)
  blockprov pull    --registry <host> --repo <name> --image-digest sha256:... \
                    --username <user> --password-env <ENV_VAR> --out <record.json> \
                    [--timeout <duration>]
  blockprov post    --record <record.json> --platform <https://address> \
                    [--summary <file>] [--timeout <duration>]
`

// Run executes the blockprov CLI and returns its exit code, 0 on success, 1 on a failure and 2 on a usage error.
func Run(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, usageMessage)
		return 2
	}

	sub, rest := args[0], args[1:]
	switch sub {
	case "build":
		return runBuild(rest, stdout)
	case "verify":
		return runVerify(rest, stdout)
	case "push":
		return runPush(rest, stdout)
	case "pull":
		return runPull(rest, stdout)
	case "post":
		return runPost(rest, stdout)
	default:
		fmt.Fprintf(stdout, "blockprov: unknown subcommand %q\n\n%s", sub, usageMessage)
		return 2
	}
}

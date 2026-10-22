package main

import (
	"fmt"
	"io"
)

const usageMessage = `blockprov: builds and pushes a block's publisher-signed provenance record.

Usage:
  blockprov build   --manifest <path> --report <blockverify --json report> \
                    --block-id <id> --block-version <v> \
                    --image-ref <ref> --image-digest sha256:... \
                    --kit-version <v> --repository <owner/name> --commit <sha> \
                    --run-url <url> --token-url <url> --token-request-token <tok> \
                    --out <record.json>
  blockprov verify  --record <record.json> --jwks <path-or-url> --repository <owner/name>
  blockprov push    --record <record.json> --registry <host> --repo <name> \
                    --image-digest sha256:... --username <user> \
                    (--password <pass> | --password-env <ENV_VAR>)
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
	default:
		fmt.Fprintf(stdout, "blockprov: unknown subcommand %q\n\n%s", sub, usageMessage)
		return 2
	}
}

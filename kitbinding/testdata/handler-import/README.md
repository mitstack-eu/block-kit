# The kit/v1 handler-import fixture set

Each `.txtar` file is one block's Go source tree. Its comment section opens
with a `verdict:` line, which is what every judge of the kit/v1 handler
import must answer for that tree. A `names:` line, where present, is the
file the answer is about: the importing file on a pass, and the file the
scan could not read on a tooling failure.

| Verdict | Meaning |
|---|---|
| `pass` | Some non-test file a default build compiles imports the handler package. |
| `absent` | No such file exists, however often the path is mentioned. |
| `tooling` | The scan could not run, so it must report a tooling failure. |

The trees hold Go sources only. Each harness adds the `go.mod` and the
`manifest.yaml` its own gate needs around them.

The platform keeps a byte-identical copy at
`scripts/test/fixtures/kit-handler-import/`. Its suite
`scripts/kit-handler-files_test.sh` compares that copy with this directory
in the operator-kit version `api/go.mod` pins, so the two sets cannot drift
apart past a pin bump.

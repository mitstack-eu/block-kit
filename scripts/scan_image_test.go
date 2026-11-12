package scripts_test

// scan-image.sh runs Trivy from an empty directory with no TRIVY_ variable and its fixed flags, passes a clean image, refuses one Trivy fails on, and hands Trivy's findings and the image's providers to the exception.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	stubTrivy = `#!/usr/bin/env bash
if [ "$1" = convert ]; then exit 0; fi
{ pwd; ls -A; env | grep '^TRIVY_'; } > "$STUB_LOG/trivy-env"
printf '%s\n' "$@" > "$STUB_LOG/trivy-args"
while [ $# -gt 0 ]; do [ "$1" = --output ] && out=$2; shift; done
[ -z "$STUB_REPORT" ] || cp "$STUB_REPORT" "$out"
exit "$STUB_TRIVY_EXIT"
`
	stubDocker = `#!/usr/bin/env bash
echo "docker $1" >> "$STUB_LOG/docker"
echo "docker $*" >> "$STUB_LOG/docker-calls"
[ "$1" != "${STUB_FAIL:-}" ] || exit 1
case "$1" in
  create) echo c0ffee ;;
  cp) cp -R "$STUB_IMAGE/${2#*:}" "$3" ;;
  save) echo "saved image" > "$3" ;;
esac
for arg; do [ "${flag:-}" != --iidfile ] || echo sha256:feed > "$arg"; flag=$arg; done
`
	scanned = "block:test"
)

// scanImage runs scan-image.sh on scanned with stubs on PATH, a stub Trivy in the pinned tool's cache, and a sha256sum that accepts it.
func scanImage(t *testing.T, trivyExit, report, image, url string, env ...string) (out string, exit int, log string) {
	t.Helper()
	cache, bin, log := t.TempDir(), t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(cache, "block-kit", "trivy-0.76.0", "trivy"), stubTrivy)
	writeFile(t, filepath.Join(bin, "docker"), stubDocker)
	writeFile(t, filepath.Join(bin, "sha256sum"), "#!/usr/bin/env bash\ncat >/dev/null\n")
	writeFile(t, filepath.Join(bin, "uname"), "#!/usr/bin/env bash\necho Linux x86_64\n")
	env = append([]string{"PATH=" + bin + ":" + os.Getenv("PATH"), "XDG_CACHE_HOME=" + cache, "STUB_LOG=" + log,
		"STUB_TRIVY_EXIT=" + trivyExit, "STUB_REPORT=" + report, "STUB_IMAGE=" + image}, env...)
	db, err := filepath.Abs(filepath.Join("testdata", "vulndb"))
	if err != nil {
		t.Fatal(err)
	}
	env = append(env, "PROVIDER_REGISTRY_URL="+url, "PROVIDER_SOURCE_URL="+url, "GOVULNCHECK_DB_URL=file://"+db)
	out, exit = runBash(t, env, "scan-image.sh", scanned)
	return out, exit, log
}

func stubbedDocker(t *testing.T) (env []string, log string) {
	t.Helper()
	bin, log := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(bin, "docker"), stubDocker)
	return []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "STUB_LOG=" + log}, log
}

func dockerCalls(t *testing.T, log string) string {
	return strings.TrimSpace(readLog(t, filepath.Join(log, "docker-calls")))
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(raw)
}

func TestScanImage_RunsTrivyFromAnEmptyDirectoryWithNoVariableAndItsFixedFlags(t *testing.T) {
	out, exit, log := scanImage(t, "0", "", t.TempDir(), "", "TRIVY_SEVERITY=CRITICAL", "TRIVY_IGNOREFILE=/tmp/ignore")

	wantOutput(t, out, exit, 0, []string{"scan-image: Trivy found nothing with a fixed version in " + scanned}, "")
	if lines := strings.Split(strings.TrimSpace(readLog(t, filepath.Join(log, "trivy-env"))), "\n"); len(lines) != 1 {
		t.Errorf("Trivy's directory, its entries and TRIVY_ variables = %q; want the directory alone, empty and with no variable", lines)
	}
	args := strings.Split(strings.TrimSpace(readLog(t, filepath.Join(log, "trivy-args"))), "\n")
	if i := slices.Index(args, "--output"); i >= 0 && i+1 < len(args) {
		args = slices.Delete(args, i+1, i+2)
	}
	want := []string{"image", "--image-src", "docker", "--exit-code", "3", "--ignore-unfixed", "--no-progress",
		"--db-repository", "mirror.gcr.io/aquasec/trivy-db:2,public.ecr.aws/aquasecurity/trivy-db:2,ghcr.io/aquasecurity/trivy-db:2",
		"--java-db-repository", "mirror.gcr.io/aquasec/trivy-java-db:1,public.ecr.aws/aquasecurity/trivy-java-db:1,ghcr.io/aquasecurity/trivy-java-db:1",
		"--format", "json", "--output", scanned}
	if !slices.Equal(args, want) {
		t.Errorf("Trivy args, its report path left out =\n%s\nwant\n%s", strings.Join(args, "\n"), strings.Join(want, "\n"))
	}
	if docker := readLog(t, filepath.Join(log, "docker")); docker != "" {
		t.Errorf("a clean image ran %q; want no docker call", docker)
	}
}

func TestScanImage_RefusesAnImageTrivyFailedOn(t *testing.T) {
	out, exit, log := scanImage(t, "1", "", t.TempDir(), "")

	wantOutput(t, out, exit, 1, []string{"scan-image: Trivy exited 1 scanning " + scanned + ", so the image is refused"}, "")
	if docker := readLog(t, filepath.Join(log, "docker")); docker != "" {
		t.Errorf("a failed scan ran %q; want no docker call", docker)
	}
}

func TestScanImage_HandsTrivysFindingsAndTheImagesProvidersToTheException(t *testing.T) {
	plain := fixtureBinary(t, fixtureModule, "")
	url := serve(t, fixtureNewest("1.0.0", "v0.58.0"))
	netFinding := finding{"vuln", fixtureTarget, "CVE-2026-78669", "golang.org/x/net", "v0.59.0", "0.60.0"}
	cases := []struct {
		name     string
		findings []finding
		exit     int
		want     string
	}{
		{"GO-2026-5932 in a provider binary copied out of the image is accepted", []finding{advisoryIn(fixtureTarget)}, 0,
			"accepted GO-2026-5932 in " + fixtureTarget + ": its Go function table names "},
		{"any other finding is refused", []finding{advisoryIn(fixtureTarget), netFinding}, 1,
			"refused CVE-2026-78669 in " + fixtureTarget},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, exit, log := scanImage(t, "3", trivyReport(t, c.findings...), imageRoot(t, map[string]string{fixtureTarget: plain}), url)

			wantOutput(t, out, exit, c.exit, []string{c.want}, "")
			if docker := readLog(t, filepath.Join(log, "docker")); docker != "docker create\ndocker cp\ndocker rm\n" {
				t.Errorf("docker calls = %q; want create, cp and rm", docker)
			}
		})
	}
}

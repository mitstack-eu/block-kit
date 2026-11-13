//go:build toolsimage

package tools_test

// The tools image TOOLS_IMAGE names holds every file at its pin with each tool in a layer of its own, and copy-tool.sh copies each out of a published one through Docker and over the registry's HTTP API, where it reads that one layer, and the image runs its sign-in companion and its probe.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/tools"
	"github.com/mitstack-eu/block-kit/helpers/tools/toolstest"
)

func everyFile() []tools.File {
	return append([]tools.File{tools.Age, tools.AgeKeygen, tools.NSAFramework, tools.NSAConfigInputs, tools.K3sInstaller, tools.SignInCA,
		{Path: tools.CopyScriptPath, SHA256: sha(tools.CopyScript)}}, tools.KubeBenchConfig...)
}

func toolOf(f tools.File) string { return strings.Split(f.Path, "/")[1] }

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestToolsImage_HoldsEveryFileAtItsPinInALayerOfItsOwn(t *testing.T) {
	image := os.Getenv("TOOLS_IMAGE")
	id := docker(t, "create", image, "/none")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", id).Run() })
	held := map[string]bool{}

	for _, f := range everyFile() {
		dest := filepath.Join(t.TempDir(), "file")
		docker(t, "cp", id+":"+f.Path, dest)
		raw, err := os.ReadFile(dest)
		if sum := sha256.Sum256(raw); err != nil || hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Errorf("%s in %s has sha256 %x (%v); want the pin %s", f.Path, image, sum, err, f.SHA256)
		}
		held[toolOf(f)] = true
	}
	for _, built := range []string{tools.SignInPath, tools.ProbePath} {
		held[toolOf(tools.File{Path: built})] = true
	}

	if layers := docker(t, "image", "inspect", "-f", "{{len .RootFS.Layers}}", image); layers != strconv.Itoa(len(held)) {
		t.Errorf("%s has %s layers; want %d, one for each tool", image, layers, len(held))
	}
}

func TestToolsImage_CopyToolCopiesEveryFileThroughDockerAndOverTheRegistryAPI(t *testing.T) {
	image := os.Getenv("TOOLS_IMAGE")
	if !regexp.MustCompile(`@sha256:[0-9a-f]{64}$`).MatchString(image) {
		t.Fatalf("TOOLS_IMAGE = %q; want a published image named by digest", image)
	}
	overAPI := toolstest.Setting + "=https://" + strings.SplitN(image, "/", 2)[0]
	layerOf, read := map[string]string{}, regexp.MustCompile(`reading layer (sha256:[0-9a-f]{64})`)

	for _, f := range everyFile() {
		f.Image = image
		_, viaDocker := runScript(t, nil, f.Args(filepath.Join(t.TempDir(), "docker"))...)
		out, viaAPI := runScript(t, []string{overAPI}, f.Args(filepath.Join(t.TempDir(), "api"))...)
		layers := read.FindAllStringSubmatch(out, -1)
		if viaDocker != 0 || viaAPI != 0 || len(layers) != 1 {
			t.Fatalf("copying %s exits %d through Docker and %d over the API, reading %d layers; want 0, 0 and one\n%s", f.Path, viaDocker, viaAPI, len(layers), out)
		}
		if seen := layerOf[toolOf(f)]; seen != "" && seen != layers[0][1] {
			t.Errorf("%s comes from layer %s, and its tool's other files from %s", f.Path, layers[0][1], seen)
		}
		layerOf[toolOf(f)] = layers[0][1]
	}

	distinct := map[string]bool{}
	for _, layer := range layerOf {
		distinct[layer] = true
	}
	if len(distinct) != len(layerOf) {
		t.Errorf("tools share layers: %v; want each tool in a layer of its own", layerOf)
	}
}

func TestToolsImage_RunsTheSignInCompanion(t *testing.T) {
	image := os.Getenv("TOOLS_IMAGE")

	out := docker(t, "run", "--rm", "--network", "none", "--entrypoint", tools.SignInPath, image, "-version")

	if !strings.HasPrefix(out, "mitstack-signin ") {
		t.Errorf("%s -version in %s printed %q; want it to name itself", tools.SignInPath, image, out)
	}
}

func TestToolsImage_RunsTheProbe(t *testing.T) {
	image := os.Getenv("TOOLS_IMAGE")

	out, err := exec.Command("docker", "run", "--rm", "--network", "none", "--entrypoint", tools.ProbePath, image, "http://127.0.0.1:9/").CombinedOutput()

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), "connection refused") {
		t.Errorf("%s in %s exited %v, printing %q; want 1 and the refused connection named", tools.ProbePath, image, err, out)
	}
}

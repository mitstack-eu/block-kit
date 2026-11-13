// Package tools, an optional helper, names the kit's tools image and each file it holds, and ships copy-tool.sh, which copies one file of an image named by digest onto a machine.
package tools

import (
	"embed"
	"strings"
)

// Image is the tools image a release published, by tag and digest, which the release writes into the commit it tags, so main names none.
const Image = "ghcr.io/mitstack-eu/block-kit-tools:v0.72.0@sha256:be0b20130cd7dfd8b313f5687f15950b5783f7f7dfe119818be8cb29762148ab"

const CopyScriptPath = "/copy-tool/copy-tool.sh"

// SignInPath is the sign-in companion in the tools image, which a Docker host runs from the image as a container.
const SignInPath = "/mitstack-signin/mitstack-signin"

//go:embed tools.sha256 scripts/copy-tool.sh
var embedded embed.FS

var CopyScript = read("scripts/copy-tool.sh")

// File is one file of an image named by digest, at its path in that image, with the sha256 copy-tool.sh checks it against.
type File struct {
	Image, Path, SHA256 string
}

func (f File) Args(dest string) []string { return []string{f.Image, f.Path, f.SHA256, dest} }

var (
	Age             = pinned("/age/age")
	AgeKeygen       = pinned("/age/age-keygen")
	NSAFramework    = pinned("/kubescape/nsa")
	NSAConfigInputs = pinned("/kubescape/default_config_inputs")
	K3sInstaller    = pinned("/k3s/install.sh")
	KubeBenchConfig = pinnedUnder("/kube-bench/cfg/")
	SignInCA        = pinned("/mitstack-signin/ca-certificates.crt")
)

func pinned(path string) File {
	for _, f := range files() {
		if f.Path == path {
			return f
		}
	}
	panic("tools.sha256 pins no " + path)
}

func pinnedUnder(dir string) []File {
	var under []File
	for _, f := range files() {
		if strings.HasPrefix(f.Path, dir) {
			under = append(under, f)
		}
	}
	return under
}

func files() []File {
	var all []File
	for _, line := range strings.Split(strings.TrimSpace(read("tools.sha256")), "\n") {
		sum, path, _ := strings.Cut(line, "  ")
		all = append(all, File{Image: Image, Path: "/" + path, SHA256: sum})
	}
	return all
}

func read(name string) string {
	b, err := embedded.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return string(b)
}

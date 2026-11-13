package tools_test

// Each file the tools image holds is named by its path in the image and its sha256 pin, beside the image a release published, and copy-tool.sh fits a node's user data.

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"regexp"
	"slices"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/tools"
)

func TestFiles_EachNamesItsPathInTheImageAndItsSha256Pin(t *testing.T) {
	cases := []struct {
		name string
		file tools.File
		path string
	}{
		{"age", tools.Age, "/age/age"},
		{"age-keygen", tools.AgeKeygen, "/age/age-keygen"},
		{"the NSA framework", tools.NSAFramework, "/kubescape/nsa"},
		{"the NSA framework's inputs", tools.NSAConfigInputs, "/kubescape/default_config_inputs"},
		{"the k3s installer", tools.K3sInstaller, "/k3s/install.sh"},
		{"kube-bench's config", tools.KubeBenchConfig[0], "/kube-bench/cfg/config.yaml"},
		{"kube-bench's k3s-cis-1.11 policies", tools.KubeBenchConfig[6], "/kube-bench/cfg/k3s-cis-1.11/policies.yaml"},
		{"the sign-in companion's CA bundle", tools.SignInCA, "/mitstack-signin/ca-certificates.crt"},
		{"the health probe", tools.Probe, "/mitstack-probe/mitstack-probe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.file

			if f.Path != tc.path || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(f.SHA256) || f.Image != tools.Image {
				t.Errorf("%s = %+v; want path %s, a sha256 pin and the image %q", tc.name, f, tc.path, tools.Image)
			}
		})
	}
}

func TestArgs_AreCopyToolsArgumentsForTheFile(t *testing.T) {
	f := tools.File{Image: "ghcr.io/o/i:v1@sha256:" + sha("a"), Path: "/age/age", SHA256: sha("b")}

	got := f.Args("/usr/local/bin/age")

	want := []string{f.Image, "/age/age", sha("b"), "/usr/local/bin/age"}
	if !slices.Equal(got, want) {
		t.Errorf("Args = %q; want %q", got, want)
	}
}

func TestImage_IsUnsetOnMainOrNamesTheToolsPackageByTagAndDigest(t *testing.T) {
	ref := regexp.MustCompile(`^(|ghcr\.io/mitstack-eu/block-kit-tools:v\d+\.\d+\.\d+@sha256:[0-9a-f]{64})$`)

	if !ref.MatchString(tools.Image) {
		t.Errorf("Image = %q; want empty on main, or the tools package by tag and digest at a release", tools.Image)
	}
}

func TestCopyScript_FitsANodesUserDataGzippedAndBase64EncodedInUnder4000Bytes(t *testing.T) {
	var gz bytes.Buffer
	w, _ := gzip.NewWriterLevel(&gz, gzip.BestCompression)
	_, _ = w.Write([]byte(tools.CopyScript))
	_ = w.Close()

	if n := base64.StdEncoding.EncodedLen(gz.Len()); n >= 4000 {
		t.Errorf("copy-tool.sh is %d bytes gzipped and base64-encoded; want under 4000, the room a k3s node's user data leaves", n)
	}
}

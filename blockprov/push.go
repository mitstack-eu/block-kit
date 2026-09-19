package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/mitstack-eu/operator-kit/provenance"
)

// emptyOCIConfig is the standard empty config blob an artifact-only OCI
// manifest points its config descriptor at, on the model of
// application/vnd.oci.empty.v1+json.
var emptyOCIConfig = []byte("{}")

// runPush implements `blockprov push`: writes a built record into a
// registry beside its image over the plain OCI distribution API.
func runPush(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	recordPath := fs.String("record", "", "path to the record.json to push")
	registry := fs.String("registry", "", "registry host — bare (assumed https) or scheme+host")
	repo := fs.String("repo", "", "repository name inside the registry")
	imageDigest := fs.String("image-digest", "", "the image digest the record describes, sha256:...")
	username := fs.String("username", "", "registry basic auth username")
	password := fs.String("password", "", "registry basic auth password — mutually exclusive with --password-env")
	passwordEnv := fs.String("password-env", "", "environment variable holding the registry basic auth password — mutually exclusive with --password, and the way to keep a live credential out of argv")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}
	if *recordPath == "" || *registry == "" || *repo == "" || *imageDigest == "" || *username == "" {
		fmt.Fprintln(stdout, "blockprov push: --record, --registry, --repo, --image-digest and --username are all required")
		return 2
	}
	if (*password == "") == (*passwordEnv == "") {
		fmt.Fprintln(stdout, "blockprov push: exactly one of --password or --password-env is required")
		return 2
	}

	resolvedPassword := *password
	if *passwordEnv != "" {
		resolvedPassword = os.Getenv(*passwordEnv)
		if resolvedPassword == "" {
			fmt.Fprintf(stdout, "blockprov push: environment variable %q named by --password-env is empty or unset\n", *passwordEnv)
			return 2
		}
	}

	recordBytes, err := os.ReadFile(*recordPath)
	if err != nil {
		fmt.Fprintf(stdout, "blockprov push: reading record: %v\n", err)
		return 1
	}

	client := &ociClient{
		base:     normalizeRegistry(*registry),
		repo:     *repo,
		username: *username,
		password: resolvedPassword,
		http:     http.DefaultClient,
	}

	recordDigest, err := client.uploadBlob(recordBytes)
	if err != nil {
		fmt.Fprintf(stdout, "blockprov push: %v\n", err)
		return 1
	}
	configDigest, err := client.uploadBlob(emptyOCIConfig)
	if err != nil {
		fmt.Fprintf(stdout, "blockprov push: %v\n", err)
		return 1
	}

	// No "subject" descriptor: a referrer would not survive the platform's
	// untagged-image lifecycle policy, so this manifest is a plain TAGGED
	// artifact instead — see package provenance's doc comment.
	manifest := map[string]interface{}{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"artifactType":  provenance.ArtifactType,
		"config": map[string]interface{}{
			"mediaType": "application/vnd.oci.empty.v1+json",
			"digest":    configDigest,
			"size":      len(emptyOCIConfig),
		},
		"layers": []map[string]interface{}{
			{
				"mediaType": provenance.ArtifactType,
				"digest":    recordDigest,
				"size":      len(recordBytes),
			},
		},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		fmt.Fprintf(stdout, "blockprov push: encoding manifest: %v\n", err)
		return 1
	}

	tag := "sha256-" + strings.TrimPrefix(*imageDigest, "sha256:") + ".prov"
	if err := client.putManifest(tag, manifestBytes); err != nil {
		fmt.Fprintf(stdout, "blockprov push: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "blockprov push: pushed %s/%s:%s\n", client.base, *repo, tag)
	return 0
}

// normalizeRegistry turns a bare host into an https URL, and leaves a
// scheme+host as given — the shape that lets a test point --registry at a
// plain-http httptest.NewServer.
func normalizeRegistry(registry string) string {
	if strings.HasPrefix(registry, "http://") || strings.HasPrefix(registry, "https://") {
		return registry
	}
	return "https://" + registry
}

// ociClient speaks just enough of the OCI distribution API for `push`: the
// two-step blob upload and a manifest PUT, all over HTTP Basic auth.
type ociClient struct {
	base     string
	repo     string
	username string
	password string
	http     *http.Client
}

// drainAndClose reads body to completion before closing it, so the
// transport can reuse the underlying connection for the next request
// instead of tearing it down.
func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, body)
	body.Close()
}

func (c *ociClient) do(method, rawURL string, body []byte, contentType string) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, rawURL, reader)
	if err != nil {
		return nil, fmt.Errorf("building %s %s: %w", method, rawURL, err)
	}
	req.SetBasicAuth(c.username, c.password)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.http.Do(req)
}

// uploadBlob pushes data through the two-step OCI blob upload: POST
// .../blobs/uploads/ to start (a 202 with a Location header naming the
// upload session), then PUT that location with ?digest=<sha256> to
// complete it. It returns the blob's own digest.
func (c *ociClient) uploadBlob(data []byte) (digest string, err error) {
	sum := sha256.Sum256(data)
	digest = "sha256:" + hex.EncodeToString(sum[:])

	startURL := c.base + "/v2/" + c.repo + "/blobs/uploads/"
	startResp, err := c.do(http.MethodPost, startURL, nil, "")
	if err != nil {
		return "", fmt.Errorf("starting blob upload: %w", err)
	}
	drainAndClose(startResp.Body)
	if startResp.StatusCode/100 != 2 {
		return "", fmt.Errorf("starting blob upload: registry returned %d", startResp.StatusCode)
	}
	location := startResp.Header.Get("Location")
	if location == "" {
		return "", fmt.Errorf("starting blob upload: registry returned no Location header")
	}

	putURL, err := resolveAgainst(c.base, location)
	if err != nil {
		return "", fmt.Errorf("resolving blob upload location: %w", err)
	}
	putURL, err = withQueryParam(putURL, "digest", digest)
	if err != nil {
		return "", fmt.Errorf("adding digest to blob upload location: %w", err)
	}

	putResp, err := c.do(http.MethodPut, putURL, data, "application/octet-stream")
	if err != nil {
		return "", fmt.Errorf("completing blob upload: %w", err)
	}
	defer drainAndClose(putResp.Body)
	if putResp.StatusCode/100 != 2 {
		return "", fmt.Errorf("completing blob upload: registry returned %d", putResp.StatusCode)
	}
	return digest, nil
}

// putManifest PUTs manifest under tag.
func (c *ociClient) putManifest(tag string, manifest []byte) error {
	manifestURL := c.base + "/v2/" + c.repo + "/manifests/" + tag
	resp, err := c.do(http.MethodPut, manifestURL, manifest, "application/vnd.oci.image.manifest.v1+json")
	if err != nil {
		return fmt.Errorf("pushing manifest: %w", err)
	}
	defer drainAndClose(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("pushing manifest: registry returned %d", resp.StatusCode)
	}
	return nil
}

// resolveAgainst resolves ref (absolute or registry-relative, per the OCI
// distribution spec's own allowance for either) against base.
func resolveAgainst(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	return b.ResolveReference(r).String(), nil
}

// withQueryParam adds key=value to rawURL's query string, preserving
// whatever is already there.
func withQueryParam(rawURL, key, value string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

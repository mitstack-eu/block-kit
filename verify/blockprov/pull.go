package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/mitstack-eu/block-kit/verify/provenance"
)

const (
	// pulledMaxBytes bounds a manifest and a record read back, each of which is tens of kilobytes.
	pulledMaxBytes = 1 << 20
	// defaultPullTimeout bounds each of the two reads, since the publish job sets no limit of its own.
	defaultPullTimeout = time.Minute
)

var sha256DigestForm = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func runPull(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	registry := fs.String("registry", "", "registry host — bare (assumed https) or scheme+host")
	repo := fs.String("repo", "", "repository name inside the registry")
	imageDigest := fs.String("image-digest", "", "the image digest the record describes, sha256:...")
	username := fs.String("username", "", "registry basic auth username")
	passwordEnv := fs.String("password-env", "", "environment variable holding the registry basic auth password")
	outPath := fs.String("out", "", "path the record is written to")
	timeout := fs.Duration("timeout", defaultPullTimeout, "how long to wait for each of the registry's two answers")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}
	if *registry == "" || *repo == "" || *imageDigest == "" || *username == "" || *passwordEnv == "" || *outPath == "" {
		fmt.Fprintln(stdout, "blockprov pull: --registry, --repo, --image-digest, --username, --password-env and --out are all required")
		return 2
	}
	password := os.Getenv(*passwordEnv)
	if password == "" {
		fmt.Fprintf(stdout, "blockprov pull: environment variable %q named by --password-env is empty or unset\n", *passwordEnv)
		return 2
	}

	client := &ociClient{
		base:     normalizeRegistry(*registry),
		repo:     *repo,
		username: *username,
		password: password,
		http:     &http.Client{Timeout: *timeout},
	}
	tag := recordTag(*imageDigest)
	record, err := client.record(tag)
	if err != nil {
		fmt.Fprintf(stdout, "blockprov pull: %v\n", err)
		return 1
	}
	if err := os.WriteFile(*outPath, record, 0o644); err != nil {
		fmt.Fprintf(stdout, "blockprov pull: writing record: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "blockprov pull: read %s/%s:%s\n", client.base, *repo, tag)
	return 0
}

// record reads back the record tagged tag, refusing an artifact that is no provenance record and bytes its digest does not name.
func (c *ociClient) record(tag string) ([]byte, error) {
	raw, err := c.get(c.base+"/v2/"+c.repo+"/manifests/"+tag, ociManifestType)
	if err != nil {
		return nil, fmt.Errorf("reading the manifest tagged %s: %w", tag, err)
	}
	var manifest struct {
		ArtifactType string `json:"artifactType"`
		Layers       []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("decoding the manifest tagged %s: %w", tag, err)
	}
	if manifest.ArtifactType != provenance.ArtifactType || len(manifest.Layers) != 1 {
		return nil, fmt.Errorf("%s is an artifact of type %q with %d layers, where a provenance record is one layer of type %s",
			tag, manifest.ArtifactType, len(manifest.Layers), provenance.ArtifactType)
	}

	digest := manifest.Layers[0].Digest
	if !sha256DigestForm.MatchString(digest) {
		return nil, fmt.Errorf("%s names its record %q, which is no sha256 digest", tag, digest)
	}
	record, err := c.get(c.base+"/v2/"+c.repo+"/blobs/"+digest, "")
	if err != nil {
		return nil, fmt.Errorf("reading the record %s: %w", digest, err)
	}
	if got := sha256Digest(record); got != digest {
		return nil, fmt.Errorf("the registry answered %d bytes for the record %s, and they hash to %s", len(record), digest, got)
	}
	return record, nil
}

// get reads rawURL whole, with accept as its Accept header where one is given.
func (c *ociClient) get(rawURL, accept string) ([]byte, error) {
	req, err := c.request(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer drainAndClose(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("registry returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, pulledMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading the answer: %w", err)
	}
	if len(body) > pulledMaxBytes {
		return nil, fmt.Errorf("the answer is over %d bytes", pulledMaxBytes)
	}
	return body, nil
}

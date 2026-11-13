package main

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/verify/provenance"
)

func runBlockprov(t *testing.T, args ...string) (stdout string, exitCode int) {
	t.Helper()
	var buf bytes.Buffer
	exitCode = Run(args, &buf)
	return buf.String(), exitCode
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

// signTestToken duplicates provenance_test.go's helper because a test in package main cannot import provenance_test.
func signTestToken(t *testing.T, priv *rsa.PrivateKey, kid string, claims map[string]interface{}) string {
	t.Helper()
	header := map[string]interface{}{"alg": "RS256", "kid": kid, "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshalling header: %v", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshalling claims: %v", err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	hashed := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, hashed[:])
	if err != nil {
		t.Fatalf("signing: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func mintServer(t *testing.T, repository, sha string) (url string, resolve provenance.KeyResolver) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating test RSA key: %v", err)
	}
	const kid = "test-blockprov-mint-kid"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aud := r.URL.Query().Get("audience")
		if aud == "" {
			http.Error(w, "missing audience", http.StatusBadRequest)
			return
		}
		now := time.Now().UTC()
		token := signTestToken(t, priv, kid, map[string]interface{}{
			"iss":        provenance.Issuer,
			"aud":        aud,
			"repository": repository,
			"sha":        sha,
			"iat":        now.Unix(),
			"exp":        now.Add(10 * time.Minute).Unix(),
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"value": token})
	}))
	t.Cleanup(srv.Close)

	return srv.URL, func(k string) (*rsa.PublicKey, bool) {
		if k == kid {
			return &priv.PublicKey, true
		}
		return nil, false
	}
}

const (
	testRepository = "mitstack-eu/sample-block"
	testCommit     = "0123456789abcdef0123456789abcdef01234567"
)

func buildArgs(t *testing.T, dir, tokenURL, outPath string) []string {
	t.Helper()
	manifestPath := writeFile(t, dir, "manifest.yaml", "apiVersion: mitstack/v1alpha\nkind: Block\nmetadata:\n  id: sample-block\n")
	return []string{
		"build",
		"--manifest", manifestPath,
		"--block-id", "sample-block",
		"--block-version", "1.0.0",
		"--image-ref", "123456789012.dkr.ecr.eu-central-1.amazonaws.com/mitstack-sample-block@sha256:" + strings.Repeat("a", 64),
		"--image-digest", "sha256:" + strings.Repeat("a", 64),
		"--kit-version", "v0.1.0",
		"--repository", testRepository,
		"--commit", testCommit,
		"--run-url", "https://github.com/mitstack-eu/sample-block/actions/runs/1",
		"--token-url", tokenURL,
		"--token-request-token", "test-request-token",
		"--out", outPath,
	}
}

const (
	passReport      = `{"suiteVersion":"1.0.0","kitVersion":"v0.1.0","mode":"standalone","manifestDigest":"sha256:aa","verdict":"pass","resultsDigest":"sha256:bb","suites":[]}`
	failReport      = `{"suiteVersion":"1.0.0","kitVersion":"v0.1.0","mode":"standalone","manifestDigest":"sha256:aa","verdict":"fail","resultsDigest":"sha256:bb","suites":[]}`
	passBlockVerify = "=== RUN   TestDeclaredResources\n--- PASS: TestDeclaredResources (0.02s)\nPASS\n"
	failBlockVerify = "=== RUN   TestDeclaredResources\n    blockcheck_test.go:20: aws_s3_bucket is declared but the module never creates it\n--- FAIL: TestDeclaredResources (0.02s)\nFAIL\n"
)

func TestRun_Build_AFailedResultExitsNonZeroAndWritesNoFile(t *testing.T) {
	for _, tc := range [][3]string{
		{"--report", "report.json", failReport},
		{"--block-verify", "block-verify.txt", failBlockVerify},
		{"--block-verify", "empty.txt", ""},
		{"--block-verify", "all-skipped.txt", "=== RUN   TestDeclaredResources\n--- SKIP: TestDeclaredResources (0.00s)\nPASS\n"},
		{"--block-verify", "a-failure-then-pass.txt", "--- FAIL: TestNoSecretLeak (0.01s)\n--- PASS: TestDeclaredResources (0.02s)\nPASS\n"},
		{"--block-verify", "no-closing-pass.txt", "--- PASS: TestDeclaredResources (0.02s)\n"},
	} {
		t.Run(tc[1], func(t *testing.T) {
			dir := t.TempDir()
			outPath := filepath.Join(dir, "record.json")
			tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("blockprov build minted a token for a result whose verdict is not pass")
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer tokenSrv.Close()
			args := append(buildArgs(t, dir, tokenSrv.URL, outPath), tc[0], writeFile(t, dir, tc[1], tc[2]))

			stdout, exit := runBlockprov(t, args...)

			if exit != 1 {
				t.Fatalf("exit = %d for a failed %s result, want 1\nstdout:\n%s", exit, tc[0], stdout)
			}
			if _, err := os.Stat(outPath); !os.IsNotExist(err) {
				t.Errorf("os.Stat(record) = %v, want no record written for a result that is not a pass", err)
			}
		})
	}
}

func TestRun_Build_APassingResultIsTheConformanceOfARecordProvenanceVerifyAccepts(t *testing.T) {
	sum := sha256.Sum256([]byte(passBlockVerify))
	for _, tc := range []struct {
		flag, file, content string
		want                provenance.Conformance
	}{
		{"--report", "report.json", passReport, provenance.Conformance{SuiteVersion: "1.0.0", Verdict: "pass", ResultsDigest: "sha256:bb"}},
		{"--block-verify", "block-verify.txt", passBlockVerify, provenance.Conformance{SuiteVersion: "block-verify@v0.1.0", Verdict: "pass", ResultsDigest: "sha256:" + hex.EncodeToString(sum[:])}},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			dir := t.TempDir()
			outPath := filepath.Join(dir, "record.json")
			tokenURL, resolve := mintServer(t, testRepository, testCommit)
			args := append(buildArgs(t, dir, tokenURL, outPath), tc.flag, writeFile(t, dir, tc.file, tc.content))

			stdout, exit := runBlockprov(t, args...)

			if exit != 0 {
				t.Fatalf("exit = %d for a passing %s result, want 0\nstdout:\n%s", exit, tc.flag, stdout)
			}
			raw, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatalf("reading written record: %v", err)
			}
			var rec provenance.Record
			if err := json.Unmarshal(raw, &rec); err != nil {
				t.Fatalf("written record is not valid JSON: %v", err)
			}
			if err := provenance.Verify(rec, resolve, provenance.Expect{Repository: testRepository}); err != nil {
				t.Errorf("provenance.Verify on the written record: %v", err)
			}
			tc.want.RunURL = "https://github.com/mitstack-eu/sample-block/actions/runs/1"
			if got := rec.Payload.Conformance; got != tc.want || rec.Payload.SourceRepository != testRepository {
				t.Errorf("conformance = %+v, sourceRepository %q; want %+v and %q", got, rec.Payload.SourceRepository, tc.want, testRepository)
			}
		})
	}
}

// verify goes through provenance.Decode, because json.Unmarshal drops an attacker-added field and the record would be accepted.
func TestRun_Verify(t *testing.T) {
	for _, tc := range []struct {
		name, record, wantLine string
		wantExit               int
	}{
		{"the committed golden is accepted", "record.golden.json", "blockprov verify: record accepted", 0},
		{"another identity is rejected naming the mismatch", "invalid-other-identity.json", "blockprov verify: record rejected: provenance: token identity does not match the expected repository", 1},
		{"an unknown payload field is rejected", "invalid-unknown-field.json", "blockprov verify: record is not valid: provenance: record contains a field its shape does not declare", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, exit := runBlockprov(t, "verify", "--record", "../provenance/goldens/"+tc.record,
				"--jwks", "../provenance/goldens/record.golden.jwks.json", "--repository", "mitstack-eu/postgres-db")

			if exit != tc.wantExit || !strings.HasPrefix(stdout, tc.wantLine) {
				t.Errorf("verify %s = exit %d, stdout %q; want exit %d starting %q", tc.record, exit, stdout, tc.wantExit, tc.wantLine)
			}
		})
	}
}

type ociServer struct {
	*httptest.Server

	mu             sync.Mutex
	forceFail      bool
	blobs          map[string][]byte
	manifestBody   []byte
	manifestTag    string
	lastAuthHeader string
}

func newOCIServer(t *testing.T) *ociServer {
	t.Helper()
	s := &ociServer{blobs: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.lastAuthHeader = r.Header.Get("Authorization")
		fail := s.forceFail
		s.mu.Unlock()

		if fail {
			http.Error(w, "forced failure", http.StatusInternalServerError)
			return
		}

		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/blobs/uploads/"):
			w.Header().Set("Location", r.URL.Path+"session1")
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/blobs/uploads/"):
			body, _ := io.ReadAll(r.Body)
			digest := r.URL.Query().Get("digest")
			s.mu.Lock()
			s.blobs[digest] = body
			s.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/"):
			body, _ := io.ReadAll(r.Body)
			parts := strings.Split(strings.TrimRight(r.URL.Path, "/"), "/")
			s.mu.Lock()
			s.manifestBody = body
			s.manifestTag = parts[len(parts)-1]
			s.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/manifests/"):
			s.mu.Lock()
			body, tag := s.manifestBody, s.manifestTag
			s.mu.Unlock()
			if body == nil || path.Base(r.URL.Path) != tag {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(body)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/blobs/"):
			s.mu.Lock()
			blob, ok := s.blobs[path.Base(r.URL.Path)]
			s.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(blob)
		default:
			http.NotFound(w, r)
		}
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Server.Close)
	return s
}

func (s *ociServer) blobCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.blobs)
}

func TestRun_Push_UploadsBlobsAndTagsTheManifestWithNoSubject(t *testing.T) {
	srv := newOCIServer(t)
	dir := t.TempDir()
	recordPath := writeFile(t, dir, "record.json", `{"payload":{"blockId":"sample-block"},"token":"tok"}`)
	imageDigest := "sha256:" + strings.Repeat("d", 64)

	stdout, exit := runBlockprov(t, "push",
		"--record", recordPath,
		"--registry", srv.URL,
		"--repo", "mitstack-sample-block",
		"--image-digest", imageDigest,
		"--username", "AWS",
		"--password", "ecr-password",
	)
	if exit != 0 {
		t.Fatalf("want exit 0, got %d\nstdout:\n%s", exit, stdout)
	}

	wantTag := "sha256-" + strings.TrimPrefix(imageDigest, "sha256:") + ".prov"
	if srv.manifestTag != wantTag {
		t.Errorf("manifest was tagged %q, want %q — an untagged referrer is garbage-collected by the ECR lifecycle policy after seven days", srv.manifestTag, wantTag)
	}

	var manifest map[string]interface{}
	if err := json.Unmarshal(srv.manifestBody, &manifest); err != nil {
		t.Fatalf("manifest body is not JSON: %v", err)
	}
	if _, present := manifest["subject"]; present {
		t.Error("pushed manifest must carry NO subject descriptor")
	}
	if manifest["artifactType"] != provenance.ArtifactType {
		t.Errorf("artifactType = %v, want %q", manifest["artifactType"], provenance.ArtifactType)
	}

	if srv.blobCount() == 0 {
		t.Error("push must upload at least the record as a blob")
	}

	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("AWS:ecr-password"))
	if srv.lastAuthHeader != wantAuth {
		t.Errorf("Authorization header = %q, want HTTP Basic for AWS:ecr-password", srv.lastAuthHeader)
	}
}

func TestRun_Push_FailsLoudlyOnNon2xxFromRegistry(t *testing.T) {
	srv := newOCIServer(t)
	srv.forceFail = true
	dir := t.TempDir()
	recordPath := writeFile(t, dir, "record.json", `{"payload":{"blockId":"sample-block"},"token":"tok"}`)

	stdout, exit := runBlockprov(t, "push",
		"--record", recordPath,
		"--registry", srv.URL,
		"--repo", "mitstack-sample-block",
		"--image-digest", "sha256:"+strings.Repeat("e", 64),
		"--username", "AWS",
		"--password", "ecr-password",
	)
	if exit == 0 {
		t.Fatalf("want non-zero exit when the registry returns a non-2xx status, got 0\nstdout:\n%s", stdout)
	}
}

const (
	sampleRecord = `{"payload":{"blockId":"sample-block"},"token":"tok"}`
	sampleRepo   = "mitstack-postgres-db"
)

func pushSampleRecord(t *testing.T, srv *ociServer, imageDigest string) {
	t.Helper()
	recordPath := writeFile(t, t.TempDir(), "record.json", sampleRecord)
	stdout, exit := runBlockprov(t, "push", "--record", recordPath, "--registry", srv.URL, "--repo", sampleRepo,
		"--image-digest", imageDigest, "--username", "AWS", "--password", "ecr-password")
	if exit != 0 {
		t.Fatalf("pushing the sample record: exit %d\n%s", exit, stdout)
	}
}

func pullSampleRecord(t *testing.T, registry, out string, flags ...string) (stdout string, exitCode int) {
	t.Helper()
	t.Setenv("ECR_PASSWORD", "ecr-password")
	return runBlockprov(t, append([]string{"pull", "--registry", registry, "--repo", sampleRepo, "--image-digest", testDigest,
		"--username", "AWS", "--password-env", "ECR_PASSWORD", "--out", out}, flags...)...)
}

func silentServer(t *testing.T) (address string) {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv.URL
}

func TestRun_Pull_ReadsBackTheRecordPushedBesideTheImage(t *testing.T) {
	srv := newOCIServer(t)
	pushSampleRecord(t, srv, testDigest)
	srv.lastAuthHeader = ""
	out := filepath.Join(t.TempDir(), "record.json")

	stdout, exit := pullSampleRecord(t, srv.URL, out)

	got, _ := os.ReadFile(out)
	if exit != 0 || string(got) != sampleRecord {
		t.Fatalf("pull = exit %d, record %q; want 0 and the pushed record %q\nstdout:\n%s", exit, got, sampleRecord, stdout)
	}
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("AWS:ecr-password")); srv.lastAuthHeader != want {
		t.Errorf("Authorization header = %q, want HTTP Basic for AWS:ecr-password", srv.lastAuthHeader)
	}
}

func TestRun_Pull_WritesNoRecordItCannotReadBackWhole(t *testing.T) {
	sum := sha256.Sum256([]byte(sampleRecord))
	recordDigest := "sha256:" + hex.EncodeToString(sum[:])
	for _, tc := range []struct {
		name    string
		spoil   func(*ociServer)
		wantOut string
	}{
		{"no record is tagged beside the image, so the failure names the tag", func(s *ociServer) { s.manifestBody = nil }, testRecordTag},
		{"the tag holds an artifact of another type, so the failure names that type", func(s *ociServer) {
			s.manifestBody = bytes.ReplaceAll(s.manifestBody, []byte(provenance.ArtifactType), []byte("application/vnd.example.other"))
		}, "application/vnd.example.other"},
		{"the registry answers other bytes than the record's digest names, so the failure names the digest", func(s *ociServer) { s.blobs[recordDigest] = []byte("another record") }, recordDigest},
		{"the manifest names its layer by something that is no sha256 digest, so no such path is read", func(s *ociServer) {
			s.manifestBody = bytes.ReplaceAll(s.manifestBody, []byte(recordDigest), []byte("../manifests/latest"))
		}, `"../manifests/latest", which is no sha256 digest`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newOCIServer(t)
			pushSampleRecord(t, srv, testDigest)
			tc.spoil(srv)
			out := filepath.Join(t.TempDir(), "record.json")

			stdout, exit := pullSampleRecord(t, srv.URL, out)

			if exit != 1 || !strings.Contains(stdout, tc.wantOut) {
				t.Errorf("pull = exit %d, stdout %q; want exit 1 naming %q", exit, stdout, tc.wantOut)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Errorf("os.Stat(record) = %v, want no record written", err)
			}
		})
	}
}

func TestRun_Pull_ARegistryThatNeverAnswersFailsAtTheTimeoutNamingTheTag(t *testing.T) {
	out := filepath.Join(t.TempDir(), "record.json")

	stdout, exit := pullSampleRecord(t, silentServer(t), out, "--timeout", "50ms")

	if exit != 1 || !strings.Contains(stdout, testRecordTag) {
		t.Errorf("pull = exit %d, stdout %q; want exit 1 naming the tag %s it could not read", exit, stdout, testRecordTag)
	}
}

type platformRequest struct {
	method, path string
	header       http.Header
	body         string
}

func fakePlatform(t *testing.T, status int, header http.Header, answer string) (address string, requests func() []platformRequest) {
	t.Helper()
	var mu sync.Mutex
	var sent []platformRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, platformRequest{r.Method, r.URL.Path, r.Header.Clone(), string(body)})
		mu.Unlock()
		for name, values := range header {
			w.Header()[name] = values
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []platformRequest {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(sent)
	}
}

func TestRun_Post_AnAnswerThatDoesNotSayStoredFailsAfterOneRequest(t *testing.T) {
	elsewhere, followed := fakePlatform(t, http.StatusCreated, nil, `{"action":"stored"}`)
	for _, tc := range []struct {
		name     string
		status   int
		header   http.Header
		answer   string
		wantLine string
	}{
		{"a 200 that is no admission, as a proxy's page is", http.StatusOK, nil, "<html>welcome</html>",
			"blockprov post: %s answered HTTP 200 without saying the release record is stored: <html>welcome</html>\n"},
		{"a failure whose body is no JSON prints the body on one line", http.StatusBadGateway, nil, "bad\ngateway\n",
			"blockprov post: %s answered HTTP 502 and stored no release record: bad gateway\n"},
		{"a redirect is not followed, so the record reaches the named address alone", http.StatusPermanentRedirect, http.Header{"Location": {elsewhere + "/v1/block-releases"}}, "",
			"blockprov post: %s answered HTTP 308 and stored no release record: a redirect to " + elsewhere + "/v1/block-releases, which is not followed\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			platform, requests := fakePlatform(t, tc.status, tc.header, tc.answer)
			recordPath := writeFile(t, t.TempDir(), "record.json", sampleRecord)

			stdout, exit := runBlockprov(t, "post", "--record", recordPath, "--platform", platform)

			if want := strings.Replace(tc.wantLine, "%s", platform, 1); exit != 1 || stdout != want {
				t.Errorf("post = exit %d, stdout %q; want exit 1 and %q", exit, stdout, want)
			}
			if got := len(requests()) + len(followed()); got != 1 {
				t.Errorf("the named address and the one it redirects to got %d requests, want 1 to the named address", got)
			}
		})
	}
}

func TestRun_Post_APlatformThatNeverAnswersFailsAtTheTimeoutNamingItsAddress(t *testing.T) {
	platform := silentServer(t)
	recordPath := writeFile(t, t.TempDir(), "record.json", sampleRecord)

	stdout, exit := runBlockprov(t, "post", "--record", recordPath, "--platform", platform, "--timeout", "50ms")

	if want := "blockprov post: " + platform + " gave no answer, so the release record is not known to be stored: "; exit != 1 || !strings.HasPrefix(stdout, want) {
		t.Errorf("post = exit %d, stdout %q; want exit 1 starting %q", exit, stdout, want)
	}
}

func TestRun_UsageErrorsExitTwo(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no subcommand", nil},
		{"an unknown subcommand", []string{"frobnicate"}},
		{"build missing required flags", []string{"build", "--manifest", "/nonexistent"}},
		{"verify missing required flags", []string{"verify", "--record", "/nonexistent"}},
		{"push missing required flags", []string{"push", "--record", "/nonexistent"}},
		{"pull missing required flags", []string{"pull", "--out", "/nonexistent"}},
		{"post naming no platform", []string{"post", "--record", "/nonexistent", "--platform", ""}},
		{"post to an address that carries a user and a password", []string{"post", "--record", "/nonexistent", "--platform", "https://user:secret@api.example"}},
		{"post to an address that is no http or https address", []string{"post", "--record", "/nonexistent", "--platform", "api.example"}},
		{"post to an address that carries a query, which the log would print", []string{"post", "--record", "/nonexistent", "--platform", "https://api.example/?token=secret"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, exit := runBlockprov(t, tc.args...)

			if exit != 2 {
				t.Errorf("Run(%q) = exit %d, want 2\nstdout:\n%s", tc.args, exit, stdout)
			}
		})
	}
}

package reportproof_test

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/helpers/reportproof"
)

const hostKid = "host-1"

// opensslKey has openssl generate an Ed25519 key, as a host does at first boot, and reads it back for Sign.
func opensslKey(t *testing.T) (path string, key ed25519.PrivateKey) {
	t.Helper()
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Fatalf("the sign script needs openssl on PATH, as Ubuntu's base image has it: %v", err)
	}
	path = filepath.Join(t.TempDir(), "host.key")
	if out, err := exec.Command("openssl", "genpkey", "-algorithm", "ed25519", "-out", path).CombinedOutput(); err != nil {
		t.Fatalf("openssl genpkey: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatalf("openssl wrote no PEM key:\n%s", raw)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("reading openssl's key: %v", err)
	}
	return path, parsed.(ed25519.PrivateKey)
}

func runSignScript(t *testing.T, args ...string) (string, error) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "sign.sh")
	if err := os.WriteFile(script, []byte(reportproof.SignScript), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("bash", append([]string{script}, args...)...).Output()
	return string(out), err
}

func TestSignScript_PrintsTheKeySetThatKeySetMakesForTheSameKey(t *testing.T) {
	keyPath, key := opensslKey(t)

	got, err := runSignScript(t, "keyset", keyPath, hostKid)

	if err != nil {
		t.Fatalf("sign.sh keyset: %v", err)
	}
	if want := string(reportproof.KeySet(hostKid, key.Public().(ed25519.PublicKey))); got != want {
		t.Errorf("sign.sh keyset printed\n%s\nwant what KeySet makes:\n%s", got, want)
	}
}

func TestSignScript_PrintsTheProofThatSignMakesAndVerifyAccepts(t *testing.T) {
	keyPath, key := opensslKey(t)
	bodyPath := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(bodyPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)

	got, err := runSignScript(t, "proof", keyPath, hostKid, platform, bodyPath, strconv.FormatInt(now.Unix(), 10))

	if err != nil {
		t.Fatalf("sign.sh proof: %v", err)
	}
	if want := reportproof.Sign(key, hostKid, body, platform, now); got != want {
		t.Errorf("sign.sh proof printed\n%s\nwant what Sign makes:\n%s", got, want)
	}
	keySet := reportproof.KeySet(hostKid, key.Public().(ed25519.PublicKey))
	if err := reportproof.Verify(strings.TrimSpace(got), body, keySet, platform, now); err != nil {
		t.Errorf("Verify refused the script's proof: %v", err)
	}
}

func TestSignScript_RefusesAKidOrOriginThatCouldBreakOutOfTheJSON(t *testing.T) {
	keyPath, _ := opensslKey(t)
	bodyPath := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(bodyPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"a kid carrying a quote", []string{"keyset", keyPath, `host"1`}},
		{"a proof kid carrying a quote", []string{"proof", keyPath, `host"1`, platform, bodyPath, "1793400000"}},
		{"an origin that is not https", []string{"proof", keyPath, hostKid, "http://api.test.local", bodyPath, "1793400000"}},
		{"an origin carrying a quote", []string{"proof", keyPath, hostKid, `https://api.test.local"`, bodyPath, "1793400000"}},
		{"a time that is not a whole number", []string{"proof", keyPath, hostKid, platform, bodyPath, "now"}},
		{"an unknown mode", []string{"verify", keyPath, hostKid}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if out, err := runSignScript(t, tc.args...); err == nil {
				t.Errorf("sign.sh %s printed %q with no error; want a refusal", strings.Join(tc.args, " "), out)
			}
		})
	}
}

package reportproof_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/helpers/reportproof"
)

const platform = "https://api.test.local"

var body = []byte(`{"madeAt": "2026-10-30T06:00:00Z", "subscriptionResources": []}`)

func read(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// signed is a compact JWS of claims that key signs under header.
func signed(key ed25519.PrivateKey, header, claims map[string]any) string {
	encode := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	input := encode(header) + "." + encode(claims)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input)))
}

func TestVerify_AcceptsTheTokenAK3sAPIServerMintedForABodyAgainstTheKeySetItServes(t *testing.T) {
	var token struct{ Header, Payload, Signature string }
	if err := json.Unmarshal(read(t, "token-segments.json"), &token); err != nil {
		t.Fatal(err)
	}
	minted := time.Unix(1793341232, 0)

	err := reportproof.Verify(token.Header+"."+token.Payload+"."+token.Signature, read(t, "body.json"), read(t, "jwks.json"), platform, minted.Add(time.Minute))

	if err != nil {
		t.Fatalf("the RS256 token kubectl create token minted on rancher/k3s:v1.31.4-k3s1 is refused: %v", err)
	}
}

func TestSign_MakesAProofThatVerifiesAgainstTheKeySetOfItsKey(t *testing.T) {
	key := newKey(t)
	now := time.Now()

	err := reportproof.Verify(reportproof.Sign(key, "host-1", body, platform, now), body, reportproof.KeySet("host-1", key.Public().(ed25519.PublicKey)), platform, now)

	if err != nil {
		t.Fatalf("a proof Sign made is refused: %v", err)
	}
}

func TestVerify_RefusesAProofThatDoesNotSignThisBodyForThisPlatformNow(t *testing.T) {
	key, other := newKey(t), newKey(t)
	keySet := reportproof.KeySet("host-1", key.Public().(ed25519.PublicKey))
	now := time.Now()
	eddsa := map[string]any{"alg": "EdDSA", "kid": "host-1"}
	claims := func(exp, nbf time.Time, aud ...string) map[string]any {
		return map[string]any{"aud": aud, "exp": exp.Unix(), "nbf": nbf.Unix()}
	}
	valid := claims(now.Add(10*time.Minute), now, platform, reportproof.Audience(body))
	cases := []struct{ name, proof, want string }{
		{"not a JWS", "not.a-token", "is not a compact JWS"},
		{"signed by a key outside the set", reportproof.Sign(other, "host-1", body, platform, now), "is not signed by a key in the recorded set"},
		{"under a kid outside the set", reportproof.Sign(key, "host-2", body, platform, now), "is not signed by a key in the recorded set"},
		{"alg none", signed(key, map[string]any{"alg": "none", "kid": "host-1"}, valid), "is not signed by a key in the recorded set"},
		{"an alg that is not the key's", signed(key, map[string]any{"alg": "RS256", "kid": "host-1"}, valid), "is not signed by a key in the recorded set"},
		{"for another body", reportproof.Sign(key, "host-1", []byte(`{}`), platform, now), "was minted for another body"},
		{"for another platform", reportproof.Sign(key, "host-1", body, "https://elsewhere.example", now), "does not name the platform's audience"},
		{"expired", reportproof.Sign(key, "host-1", body, platform, now.Add(-time.Hour)), "has expired"},
		{"with no expiry", signed(key, eddsa, map[string]any{"aud": []string{platform, reportproof.Audience(body)}}), "carries no expiry"},
		{"not valid yet", signed(key, eddsa, claims(now.Add(time.Hour), now.Add(30*time.Minute), platform, reportproof.Audience(body))), "is not valid yet"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := reportproof.Verify(tc.proof, body, keySet, platform, now)

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Verify answered %v, want an error saying the proof %s", err, tc.want)
			}
		})
	}
}

func TestVerify_AcceptsAProofWithinAMinuteOfItsExpiryOrStart(t *testing.T) {
	key := newKey(t)
	keySet := reportproof.KeySet("host-1", key.Public().(ed25519.PublicKey))
	now := time.Now()
	proof := reportproof.Sign(key, "host-1", body, platform, now)

	for _, at := range []time.Time{now.Add(-reportproof.Leeway + time.Second), now.Add(10*time.Minute + reportproof.Leeway - time.Second)} {
		if err := reportproof.Verify(proof, body, keySet, platform, at); err != nil {
			t.Errorf("at %s the proof is refused: %v", at.Sub(now), err)
		}
	}
}

func TestTokenRequestScript_AsksTheAPIServerForATokenNamingThePlatformsOriginAndTheBodysDigest(t *testing.T) {
	dir := t.TempDir()
	script, bodyFile := filepath.Join(dir, "token-request.sh"), filepath.Join(dir, "body.json")
	for name, content := range map[string]string{script: reportproof.TokenRequestScript, bodyFile: string(body),
		filepath.Join(dir, "kubectl"): "#!/usr/bin/env bash\necho \"kubectl $*\"\n"} {
		if err := os.WriteFile(name, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", script, bodyFile, platform+"/v1/cluster-reports", "mitstack-node", "mitstack-node-report")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))

	out, err := cmd.CombinedOutput()

	want := fmt.Sprintf("kubectl create token mitstack-node-report -n mitstack-node --audience %s --audience %s --duration 10m\n", platform, reportproof.Audience(body))
	if err != nil || string(out) != want {
		t.Fatalf("the script ran %q (%v), want %q", out, err, want)
	}
}

func TestTokenRequestScript_RefusesAReportURLThatIsNotHTTPS(t *testing.T) {
	script := filepath.Join(t.TempDir(), "token-request.sh")
	if err := os.WriteFile(script, []byte(reportproof.TokenRequestScript), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("bash", script, "body.json", "http://api.test.local/v1/cluster-reports", "mitstack-node", "mitstack-node-report").CombinedOutput()

	if err == nil || !strings.Contains(string(out), "is not https") {
		t.Fatalf("the script answered %q (%v), want a refusal naming the URL as not https", out, err)
	}
}

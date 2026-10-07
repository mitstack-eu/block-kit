package reach

import "testing"

func TestSafeProbeURL_ReturnsTheURLThatWillBeDialled(t *testing.T) {
	got, err := safeProbeURL("https://app.example.test/health?x=1")
	if err != nil {
		t.Fatalf("safeProbeURL: %v", err)
	}
	if got != "https://app.example.test/health?x=1" {
		t.Fatalf("got %q — the probe dials THIS, so it must be the usable URL", got)
	}
}

func TestSafeProbeURL_RefusesASchemeThatIsNotHTTP(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "gopher://example.test/", "ftp://example.test/"} {
		if _, err := safeProbeURL(raw); err == nil {
			t.Fatalf("%s was accepted — only http and https may be probed", raw)
		}
	}
}

func TestSafeProbeURL_RefusesAURLWithNoHost(t *testing.T) {
	if _, err := safeProbeURL("https:///health"); err == nil {
		t.Fatal("a URL with no host was accepted")
	}
}

// Loopback stays allowed because operator tests drive the real path against an httptest fixture.
func TestSafeProbeURL_LeavesTheAddressPolicyToTheCaller(t *testing.T) {
	if _, err := safeProbeURL("http://127.0.0.1:8080/"); err != nil {
		t.Fatalf("loopback was refused here: %v — that decision belongs to the caller", err)
	}
}

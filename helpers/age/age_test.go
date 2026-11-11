// Encrypt and Decrypt read and write the age v1 format, checked against files the reference implementation wrote.
package age_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/age"
)

// goldenSizes are the plaintext lengths of testdata/golden-<n>.age, each holding byte(i % 251) at index i.
var goldenSizes = []int{0, 11, 65535, 65536, 65537, 70000, 131072}

func plaintext(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

func fixtureIdentity(t *testing.T) *age.Identity {
	t.Helper()
	raw, err := os.ReadFile("testdata/identity.txt")
	if err != nil {
		t.Fatal(err)
	}
	id, err := age.ParseIdentity("AGE-SECRET-KEY-1" + strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("ParseIdentity(testdata/identity.txt): %v", err)
	}
	return id
}

func golden(t *testing.T, n int) []byte {
	t.Helper()
	raw, err := os.ReadFile(fmt.Sprintf("testdata/golden-%d.age", n))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func encrypt(t *testing.T, to *age.Recipient, plain []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := age.Encrypt(&out, to)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatalf("Write(%d bytes): %v", len(plain), err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return out.Bytes()
}

func decrypt(raw []byte, id *age.Identity) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(raw), id)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func TestDecrypt_ReadsFilesTheReferenceImplementationWrote(t *testing.T) {
	id := fixtureIdentity(t)
	for _, n := range goldenSizes {
		t.Run(fmt.Sprintf("%d bytes", n), func(t *testing.T) {
			got, err := decrypt(golden(t, n), id)

			if err != nil {
				t.Fatalf("Decrypt(golden-%d.age): %v", n, err)
			}
			if !bytes.Equal(got, plaintext(n)) {
				t.Errorf("Decrypt(golden-%d.age) returned %d bytes that differ from the plaintext", n, len(got))
			}
		})
	}
}

func TestEncryptDecrypt_RoundTripsPlaintextAcrossChunkBoundaries(t *testing.T) {
	id := fixtureIdentity(t)
	for _, n := range append(goldenSizes, 200000) {
		t.Run(fmt.Sprintf("%d bytes", n), func(t *testing.T) {
			sealed := encrypt(t, id.Recipient(), plaintext(n))

			got, err := decrypt(sealed, id)

			if err != nil || !bytes.Equal(got, plaintext(n)) {
				t.Errorf("round trip of %d bytes returned %d bytes, error %v", n, len(got), err)
			}
			if n >= 16 && bytes.Contains(sealed, plaintext(n)[:16]) {
				t.Errorf("the sealed file of %d bytes holds the first 16 bytes of the plaintext", n)
			}
		})
	}
}

func TestDecrypt_RefusesInputThatIsNotTheWholeUntouchedFile(t *testing.T) {
	id := fixtureIdentity(t)
	other, err := age.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	two := golden(t, 131072)
	headerLen := len(two) - 2*(65536+16)
	flip := func(raw []byte, at int) []byte {
		out := bytes.Clone(raw)
		out[at] ^= 1
		return out
	}
	cases := []struct {
		label string
		raw   []byte
		id    *age.Identity
	}{
		{"nothing", nil, id},
		{"text that is not age", []byte("hello\n"), id},
		{"another identity", golden(t, 11), other},
		{"a payload cut at a chunk boundary", two[:len(two)-(65536+16)], id},
		{"a payload cut inside its last chunk", two[:len(two)-5], id},
		{"a payload cut inside the header", two[:40], id},
		{"a flipped payload byte", flip(two, headerLen+100), id},
		{"a flipped tag byte of the last chunk", flip(two, len(two)-1), id},
		{"a flipped header MAC byte", flip(two, headerLen-16-10), id},
		{"a flipped payload nonce byte", flip(two, headerLen-3), id},
		{"bytes after the last chunk", append(bytes.Clone(two), 0), id},
		{"a whole chunk after the last chunk", append(bytes.Clone(two), two[len(two)-(65536+16):]...), id},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got, err := decrypt(tc.raw, tc.id)

			if err == nil {
				t.Errorf("Decrypt of %s returned %d bytes and no error, want an error", tc.label, len(got))
			}
		})
	}
}

func TestParseRecipientAndIdentity_ReadWhatTheyWrite(t *testing.T) {
	id := fixtureIdentity(t)

	recipient, rerr := age.ParseRecipient(id.Recipient().String())
	again, ierr := age.ParseIdentity(id.String())

	if rerr != nil || recipient.String() != id.Recipient().String() || !strings.HasPrefix(recipient.String(), "age1") {
		t.Errorf("ParseRecipient(%q) = %v, %v; want the same age1 key", id.Recipient(), recipient, rerr)
	}
	if ierr != nil || again.String() != id.String() || !strings.HasPrefix(id.String(), "AGE-SECRET-KEY-1") {
		t.Errorf("ParseIdentity round trip failed: %v", ierr)
	}
}

func TestParseRecipientAndIdentity_RefuseMalformedKeysWithoutEchoingThem(t *testing.T) {
	id := fixtureIdentity(t)
	secret := id.String()
	public := id.Recipient().String()
	cases := map[string]func() error{
		"a recipient given an identity":        func() error { _, err := age.ParseRecipient(secret); return err },
		"an identity given a recipient":        func() error { _, err := age.ParseIdentity(public); return err },
		"an identity in lower case":            func() error { _, err := age.ParseIdentity(strings.ToLower(secret)); return err },
		"an identity with a changed character": func() error { _, err := age.ParseIdentity(secret[:len(secret)-2] + "QQ"); return err },
		"a recipient in mixed case":            func() error { _, err := age.ParseRecipient("A" + public[1:]); return err },
		"a recipient with a bad checksum":      func() error { _, err := age.ParseRecipient(public[:len(public)-1] + "q"); return err },
		"an empty identity":                    func() error { _, err := age.ParseIdentity(""); return err },
	}
	for label, parse := range cases {
		t.Run(label, func(t *testing.T) {
			err := parse()

			if err == nil {
				t.Fatalf("%s was accepted", label)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), secret[16:30]) {
				t.Errorf("the error for %s echoes the key: %v", label, err)
			}
		})
	}
}

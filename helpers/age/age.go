// Package age, an optional helper tied to no provider, reads and writes the age v1 file format for one X25519 recipient, sealing with golang.org/x/crypto's ChaCha20-Poly1305.
package age

import (
	"bufio"
	"bytes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	keySize   = chacha20poly1305.KeySize
	nonceSize = chacha20poly1305.NonceSize
	tagSize   = chacha20poly1305.Overhead

	chunkSize      = 64 << 10
	fileKeySize    = 16
	streamNonceLen = 16
	columns        = 64

	headerVersion  = "age-encryption.org/v1"
	stanzaPrefix   = "-> "
	macPrefix      = "---"
	x25519Type     = "X25519"
	x25519Info     = "age-encryption.org/v1/X25519"
	recipientHRP   = "age"
	identityHRP    = "age-secret-key-"
	finalChunkFlag = 1
)

var b64 = base64.RawStdEncoding.Strict()

var errOpen = errors.New("age: the sealed chunk does not authenticate")

// Recipient is the public half of an X25519 key, written age1....
type Recipient struct{ pub *ecdh.PublicKey }

// Identity is the secret half of an X25519 key, written AGE-SECRET-KEY-1....
type Identity struct{ priv *ecdh.PrivateKey }

func GenerateIdentity() (*Identity, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("age: generating an X25519 key: %w", err)
	}
	return &Identity{priv: priv}, nil
}

func ParseRecipient(s string) (*Recipient, error) {
	hrp, data, err := bech32Decode(s)
	if err != nil || hrp != recipientHRP || s != strings.ToLower(s) {
		return nil, errors.New("age: the recipient is not an age1 public key in lower case")
	}
	pub, err := ecdh.X25519().NewPublicKey(data)
	if err != nil {
		return nil, errors.New("age: the recipient does not hold a 32-byte X25519 public key")
	}
	return &Recipient{pub: pub}, nil
}

// ParseIdentity never echoes its input, since the input is a secret key.
func ParseIdentity(s string) (*Identity, error) {
	hrp, data, err := bech32Decode(s)
	if err != nil || hrp != identityHRP || s != strings.ToUpper(s) {
		return nil, errors.New("age: the identity is not an AGE-SECRET-KEY-1 key in upper case")
	}
	priv, err := ecdh.X25519().NewPrivateKey(data)
	if err != nil {
		return nil, errors.New("age: the identity does not hold a 32-byte X25519 secret key")
	}
	return &Identity{priv: priv}, nil
}

func (i *Identity) Recipient() *Recipient { return &Recipient{pub: i.priv.PublicKey()} }

func (i *Identity) String() string {
	s, _ := bech32Encode(identityHRP, i.priv.Bytes())
	return strings.ToUpper(s)
}

func (r *Recipient) String() string {
	s, _ := bech32Encode(recipientHRP, r.pub.Bytes())
	return s
}

func hkdfKey(secret, salt []byte, info string) ([]byte, error) {
	return hkdf.Key(sha256.New, secret, salt, info, keySize)
}

func headerMAC(fileKey, header []byte) ([]byte, error) {
	key, err := hkdfKey(fileKey, nil, "header")
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(header)
	return mac.Sum(nil), nil
}

func payloadAEAD(fileKey, nonce []byte) (cipher.AEAD, error) {
	key, err := hkdfKey(fileKey, nonce, "payload")
	if err != nil {
		return nil, err
	}
	return chacha20poly1305.New(key)
}

func wrapAEAD(shared, share, recipient []byte) (cipher.AEAD, error) {
	key, err := hkdfKey(shared, append(append([]byte{}, share...), recipient...), x25519Info)
	if err != nil {
		return nil, err
	}
	return chacha20poly1305.New(key)
}

// Encrypt writes the age header to dst and returns a writer whose Close must be called to seal the final chunk.
func Encrypt(dst io.Writer, r *Recipient) (io.WriteCloser, error) {
	fileKey := make([]byte, fileKeySize)
	if _, err := rand.Read(fileKey); err != nil {
		return nil, fmt.Errorf("age: drawing a file key: %w", err)
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("age: generating an ephemeral key: %w", err)
	}
	shared, err := eph.ECDH(r.pub)
	if err != nil {
		return nil, fmt.Errorf("age: the recipient is a low-order point: %w", err)
	}
	share := eph.PublicKey().Bytes()
	wrap, err := wrapAEAD(shared, share, r.pub.Bytes())
	if err != nil {
		return nil, err
	}
	body := b64.EncodeToString(wrap.Seal(nil, make([]byte, nonceSize), fileKey, nil))

	header := headerVersion + "\n" + stanzaPrefix + x25519Type + " " + b64.EncodeToString(share) + "\n" + body + "\n" + macPrefix
	mac, err := headerMAC(fileKey, []byte(header))
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, streamNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("age: drawing a payload nonce: %w", err)
	}
	payload, err := payloadAEAD(fileKey, nonce)
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(dst, header+" "+b64.EncodeToString(mac)+"\n"); err != nil {
		return nil, err
	}
	if _, err := dst.Write(nonce); err != nil {
		return nil, err
	}
	return &streamWriter{dst: dst, aead: payload, buf: make([]byte, 0, chunkSize)}, nil
}

// chunkNonce is the chunk's counter in big-endian order followed by one byte that marks the final chunk.
func chunkNonce(counter uint64, final bool) []byte {
	n := make([]byte, nonceSize)
	flag := nonceSize - 1
	binary.BigEndian.PutUint64(n[flag-8:flag], counter)
	if final {
		n[flag] = finalChunkFlag
	}
	return n
}

type streamWriter struct {
	dst     io.Writer
	aead    cipher.AEAD
	buf     []byte
	counter uint64
	err     error
	closed  bool
}

func (w *streamWriter) flush(final bool) error {
	if w.err != nil {
		return w.err
	}
	_, w.err = w.dst.Write(w.aead.Seal(nil, chunkNonce(w.counter, final), w.buf, nil))
	w.counter++
	w.buf = w.buf[:0]
	return w.err
}

func (w *streamWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errors.New("age: write to a closed stream")
	}
	written := 0
	for len(p) > 0 {
		if len(w.buf) == chunkSize {
			if err := w.flush(false); err != nil {
				return written, err
			}
		}
		n := min(len(p), chunkSize-len(w.buf))
		w.buf = append(w.buf, p[:n]...)
		p = p[n:]
		written += n
	}
	return written, nil
}

func (w *streamWriter) Close() error {
	if w.closed {
		return w.err
	}
	w.closed = true
	return w.flush(true)
}

// Decrypt reads the header of src with i and returns the plaintext, whose Read reports any truncation, trailing data or altered chunk once it reaches it.
func Decrypt(src io.Reader, i *Identity) (io.Reader, error) {
	br := bufio.NewReaderSize(src, chunkSize+tagSize+1)
	fileKey, err := readHeader(br, i)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, streamNonceLen)
	if _, err := io.ReadFull(br, nonce); err != nil {
		return nil, fmt.Errorf("age: reading the payload nonce: %w", errTruncated(err))
	}
	payload, err := payloadAEAD(fileKey, nonce)
	if err != nil {
		return nil, err
	}
	return &streamReader{src: br, aead: payload}, nil
}

func errTruncated(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

type stanza struct {
	args []string
	body []byte
}

func readHeader(br *bufio.Reader, i *Identity) ([]byte, error) {
	var raw bytes.Buffer
	line, err := readLine(br, &raw)
	if err != nil || line != headerVersion {
		return nil, errors.New("age: the input is not an age v1 file")
	}
	var stanzas []stanza
	for {
		line, err = readLine(br, &raw)
		if err != nil {
			return nil, fmt.Errorf("age: reading the header: %w", errTruncated(err))
		}
		if mac, ok := strings.CutPrefix(line, macPrefix+" "); ok {
			return unwrap(i, stanzas, mac, raw.Bytes())
		}
		args, ok := strings.CutPrefix(line, stanzaPrefix)
		if !ok {
			return nil, errors.New("age: the header holds a line that is neither a stanza nor the MAC")
		}
		s := stanza{args: strings.Fields(args)}
		if s.body, err = readStanzaBody(br, &raw); err != nil {
			return nil, err
		}
		stanzas = append(stanzas, s)
	}
}

func readLine(br *bufio.Reader, raw *bytes.Buffer) (string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	raw.WriteString(line)
	return strings.TrimSuffix(line, "\n"), nil
}

func readStanzaBody(br *bufio.Reader, raw *bytes.Buffer) ([]byte, error) {
	var body []byte
	for {
		line, err := readLine(br, raw)
		if err != nil {
			return nil, fmt.Errorf("age: reading a stanza body: %w", errTruncated(err))
		}
		chunk, err := b64.DecodeString(line)
		if err != nil || len(line) > columns {
			return nil, errors.New("age: a stanza body is not canonical base64")
		}
		body = append(body, chunk...)
		if len(line) < columns {
			return body, nil
		}
	}
}

func unwrap(i *Identity, stanzas []stanza, mac string, header []byte) ([]byte, error) {
	covered := bytes.TrimSuffix(header, []byte(" "+mac+"\n"))
	for _, s := range stanzas {
		fileKey := unwrapX25519(i, s)
		if fileKey == nil {
			continue
		}
		want, err := headerMAC(fileKey, covered)
		if err != nil {
			return nil, err
		}
		got, err := b64.DecodeString(mac)
		if err != nil || subtle.ConstantTimeCompare(got, want) != 1 {
			return nil, errors.New("age: the header MAC does not match")
		}
		return fileKey, nil
	}
	return nil, errors.New("age: the identity does not match any recipient of the file")
}

func unwrapX25519(i *Identity, s stanza) []byte {
	if len(s.args) != 2 || s.args[0] != x25519Type || len(s.body) != fileKeySize+tagSize {
		return nil
	}
	share, err := b64.DecodeString(s.args[1])
	if err != nil {
		return nil
	}
	pub, err := ecdh.X25519().NewPublicKey(share)
	if err != nil {
		return nil
	}
	shared, err := i.priv.ECDH(pub)
	if err != nil {
		return nil
	}
	wrap, err := wrapAEAD(shared, share, i.priv.PublicKey().Bytes())
	if err != nil {
		return nil
	}
	fileKey, err := wrap.Open(nil, make([]byte, nonceSize), s.body, nil)
	if err != nil {
		return nil
	}
	return fileKey
}

type streamReader struct {
	src     *bufio.Reader
	aead    cipher.AEAD
	out     []byte
	counter uint64
	done    bool
	err     error
}

func (r *streamReader) Read(p []byte) (int, error) {
	for len(r.out) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		if r.done {
			return 0, io.EOF
		}
		r.err = r.nextChunk()
	}
	n := copy(p, r.out)
	r.out = r.out[n:]
	return n, nil
}

func (r *streamReader) nextChunk() error {
	sealed := make([]byte, chunkSize+tagSize)
	n, err := io.ReadFull(r.src, sealed)
	final := false
	switch {
	case err == io.EOF:
		return io.ErrUnexpectedEOF
	case err == io.ErrUnexpectedEOF:
		final = true
	case err != nil:
		return err
	default:
		_, peek := r.src.Peek(1)
		final = peek == io.EOF
	}
	plain, err := r.aead.Open(nil, chunkNonce(r.counter, final), sealed[:n], nil)
	if err != nil {
		return errOpen
	}
	if final && len(plain) == 0 && r.counter > 0 {
		return errors.New("age: the final chunk is empty after other chunks")
	}
	r.counter++
	r.out = plain
	r.done = final
	return nil
}

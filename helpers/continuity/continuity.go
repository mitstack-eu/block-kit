// Package continuity, an optional helper tied to no provider, snapshots a block's state to an object store through links it is handed and restores it, with every snapshot encrypted with age before it leaves the node.
package continuity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/mitstack-eu/block-kit/helpers/age"
)

// transferTimeout bounds one upload or download, which no signal can end earlier.
const transferTimeout = 30 * time.Minute

// Link is a presigned URL, and Ref names the stored object an upload link writes.
type Link struct {
	URL string `json:"url"`
	Ref string `json:"ref,omitempty"`
}

// Links hands out the presigned links, so a hook never holds a storage key.
type Links interface {
	Upload(ctx context.Context, unit string) (Link, error)
	Download(ctx context.Context, unit, ref string) (Link, error)
}

// Keys holds the age keys of a unit, the restore key staying outside the bucket.
type Keys interface {
	Recipient(ctx context.Context, unit string) (string, error)
	Identity(ctx context.Context, unit string) (string, error)
}

// State is what a snapshot carries: the plaintext of Export is what Import and Verify read back.
type State interface {
	Export(ctx context.Context, w io.Writer) error
	Import(ctx context.Context, r io.Reader) error
	// Verify restores r into a throwaway scratch copy, checks it, and discards the copy whatever the outcome.
	Verify(ctx context.Context, r io.Reader) error
}

// Handover is what a snapshot publishes for its restore to read, holding no link and no key.
type Handover struct {
	Ref     string    `json:"ref"`
	SHA256  string    `json:"sha256"`
	Size    int64     `json:"size"`
	TakenAt time.Time `json:"takenAt"`
}

// ParseHandover reads the string Snapshot returned.
func ParseHandover(s string) (Handover, error) {
	var h Handover
	if err := json.Unmarshal([]byte(s), &h); err != nil {
		return Handover{}, errors.New("continuity: the handover is not one a snapshot published")
	}
	if h.Ref == "" || h.SHA256 == "" || h.Size <= 0 {
		return Handover{}, errors.New("continuity: the handover names no object, digest or size")
	}
	return h, nil
}

// Hook snapshots and restores one State, and a nil Client is a plain client with a request timeout that follows no redirect.
type Hook struct {
	Links  Links
	Keys   Keys
	State  State
	Client *http.Client
	// Clock stamps a handover and defaults to time.Now.
	Clock func() time.Time
}

func (h Hook) client() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	return newClient()
}

// newClient follows no redirect, since a redirect would hand a presigned link's query or a store's token to another host.
func newClient() *http.Client {
	return &http.Client{Timeout: transferTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (h Hook) now() time.Time {
	if h.Clock != nil {
		return h.Clock().UTC()
	}
	return time.Now().UTC()
}

// Snapshot publishes a handover only after the stored ciphertext decrypts and State.Verify accepts it, and on any failure returns none.
func (h Hook) Snapshot(ctx context.Context, unit string) (string, error) {
	recipient, identity, err := h.keys(ctx, unit)
	if err != nil {
		return "", fmt.Errorf("snapshot of %q: reading the keys: %w", unit, err)
	}
	spool, sum, err := h.encryptToSpool(ctx, recipient)
	if err != nil {
		return "", fmt.Errorf("snapshot of %q: exporting: %w", unit, err)
	}
	defer discard(spool)
	up, err := h.Links.Upload(ctx, unit)
	if err != nil {
		return "", fmt.Errorf("snapshot of %q: getting an upload link: %w", unit, err)
	}
	if err := putObject(ctx, h.client(), up.URL, spool); err != nil {
		return "", fmt.Errorf("snapshot of %q: uploading: %w", unit, err)
	}
	size, err := spoolSize(spool)
	if err != nil {
		return "", fmt.Errorf("snapshot of %q: %w", unit, err)
	}
	ho := Handover{Ref: up.Ref, SHA256: sum, Size: size, TakenAt: h.now()}
	if err := h.scratchCheck(ctx, unit, ho, identity); err != nil {
		return "", fmt.Errorf("snapshot of %q: scratch check: %w", unit, err)
	}
	raw, err := json.Marshal(ho)
	if err != nil {
		return "", fmt.Errorf("snapshot of %q: encoding the handover: %w", unit, err)
	}
	return string(raw), nil
}

// Restore puts the state a handover names back through State.Import once the stored object matches the handover's digest and size.
func (h Hook) Restore(ctx context.Context, unit, handover string) error {
	ho, err := ParseHandover(handover)
	if err != nil {
		return fmt.Errorf("restore of %q: %w", unit, err)
	}
	_, identity, err := h.keys(ctx, unit)
	if err != nil {
		return fmt.Errorf("restore of %q: reading the keys: %w", unit, err)
	}
	plain, closeStored, err := h.openStored(ctx, unit, ho, identity)
	if err != nil {
		return fmt.Errorf("restore of %q: %w", unit, err)
	}
	defer closeStored()
	if err := h.State.Import(ctx, plain); err != nil {
		return fmt.Errorf("restore of %q: importing: %w", unit, err)
	}
	if _, err := io.Copy(io.Discard, plain); err != nil {
		return fmt.Errorf("restore of %q: the stored ciphertext does not end cleanly: %w", unit, err)
	}
	return nil
}

func (h Hook) keys(ctx context.Context, unit string) (*age.Recipient, *age.Identity, error) {
	rawRecipient, err := h.Keys.Recipient(ctx, unit)
	if err != nil {
		return nil, nil, err
	}
	recipient, err := age.ParseRecipient(rawRecipient)
	if err != nil {
		return nil, nil, err
	}
	rawIdentity, err := h.Keys.Identity(ctx, unit)
	if err != nil {
		return nil, nil, err
	}
	identity, err := age.ParseIdentity(rawIdentity)
	if err != nil {
		return nil, nil, err
	}
	return recipient, identity, nil
}

func (h Hook) encryptToSpool(ctx context.Context, recipient *age.Recipient) (*os.File, string, error) {
	spool, err := os.CreateTemp("", "continuity-*.age")
	if err != nil {
		return nil, "", err
	}
	digest := sha256.New()
	if err := h.exportEncrypted(ctx, io.MultiWriter(spool, digest), recipient); err != nil {
		discard(spool)
		return nil, "", err
	}
	return spool, hex.EncodeToString(digest.Sum(nil)), nil
}

func (h Hook) exportEncrypted(ctx context.Context, dst io.Writer, recipient *age.Recipient) error {
	w, err := age.Encrypt(dst, recipient)
	if err != nil {
		return err
	}
	plain := &countingWriter{dst: w}
	if err := h.State.Export(ctx, plain); err != nil {
		return err
	}
	if plain.written == 0 {
		return errExportedNothing
	}
	return w.Close()
}

// errExportedNothing exists because age seals an empty export into a well-formed file, which the digest and size checks would pass.
var errExportedNothing = errors.New("the state exported nothing, and a snapshot of nothing restores nothing")

type countingWriter struct {
	dst     io.Writer
	written int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.dst.Write(p)
	c.written += int64(n)
	return n, err
}

func (h Hook) scratchCheck(ctx context.Context, unit string, ho Handover, identity *age.Identity) error {
	plain, closeStored, err := h.openStored(ctx, unit, ho, identity)
	if err != nil {
		return err
	}
	defer closeStored()
	if err := h.State.Verify(ctx, plain); err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, plain); err != nil {
		return fmt.Errorf("the stored ciphertext does not end cleanly: %w", err)
	}
	return nil
}

// openStored downloads the object ho names into a spool, checks it against ho before any byte is decrypted, and returns its plaintext.
func (h Hook) openStored(ctx context.Context, unit string, ho Handover, identity *age.Identity) (io.Reader, func(), error) {
	link, err := h.Links.Download(ctx, unit, ho.Ref)
	if err != nil {
		return nil, nil, fmt.Errorf("getting a download link: %w", err)
	}
	stored, sum, err := fetch(ctx, h.client(), link.URL)
	if err != nil {
		return nil, nil, fmt.Errorf("downloading: %w", err)
	}
	size, err := spoolSize(stored)
	if err != nil {
		discard(stored)
		return nil, nil, err
	}
	if sum != ho.SHA256 || size != ho.Size {
		discard(stored)
		return nil, nil, fmt.Errorf("the stored object holds %d bytes with another digest than the handover names (%d bytes)", size, ho.Size)
	}
	plain, err := age.Decrypt(stored, identity)
	if err != nil {
		discard(stored)
		return nil, nil, fmt.Errorf("decrypting: %w", err)
	}
	return plain, func() { discard(stored) }, nil
}

func discard(f *os.File) {
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
}

func spoolSize(f *os.File) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// scrub drops the URL net/http puts in its errors, since a presigned link carries its signature in the query.
func scrub(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

func putObject(ctx context.Context, client *http.Client, link string, body *os.File) error {
	size, err := spoolSize(body)
	if err != nil {
		return err
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, link, io.NewSectionReader(body, 0, size))
	if err != nil {
		return errors.New("the upload link is not a URL")
	}
	req.ContentLength = size
	return doOK(client, req)
}

// fetch leaves the spool positioned at its start, and returns the hex SHA-256 of what it holds.
func fetch(ctx context.Context, client *http.Client, link string) (*os.File, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, "", errors.New("the download link is not a URL")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", scrub(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("the store answered %s", resp.Status)
	}
	spool, err := os.CreateTemp("", "continuity-*.age")
	if err != nil {
		return nil, "", err
	}
	digest := sha256.New()
	if _, err := io.Copy(io.MultiWriter(spool, digest), resp.Body); err != nil {
		discard(spool)
		return nil, "", scrub(err)
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		discard(spool)
		return nil, "", err
	}
	return spool, hex.EncodeToString(digest.Sum(nil)), nil
}

func doOK(client *http.Client, req *http.Request) error {
	resp, err := client.Do(req)
	if err != nil {
		return scrub(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("the store answered %s", resp.Status)
	}
	return nil
}

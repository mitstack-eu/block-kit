package robot

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
)

// RescueKey is one install's SSH key pair, whose private half lives only in the caller's memory.
type RescueKey struct {
	private    ed25519.PrivateKey
	authorized string
}

func NewRescueKey() (RescueKey, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return RescueKey{}, fmt.Errorf("generating the rescue key: %w", err)
	}
	return RescueKey{private: private, authorized: "ssh-ed25519 " + base64.StdEncoding.EncodeToString(wireKey(public))}, nil
}

func wireKey(public ed25519.PublicKey) []byte {
	var out []byte
	for _, part := range [][]byte{[]byte("ssh-ed25519"), public} {
		out = binary.BigEndian.AppendUint32(out, uint32(len(part)))
		out = append(out, part...)
	}
	return out
}

func (k RescueKey) PrivateKey() ed25519.PrivateKey { return k.private }

// AuthorizedKey is the public half in OpenSSH's authorized_keys form.
func (k RescueKey) AuthorizedKey() string { return k.authorized }

// RescueSession is one boot into the rescue system: the key Robot admitted and the host keys it named, if any.
type RescueSession struct {
	Server      int64
	Fingerprint string
	HostKeys    []string
}

// BootRescue registers key as keyName and resets the server into a rescue system that admits this key alone.
func (c *Client) BootRescue(ctx context.Context, number int64, keyName string, key RescueKey) (RescueSession, error) {
	registered, err := c.CreateKey(ctx, keyName, key.AuthorizedKey())
	if err != nil {
		return RescueSession{}, fmt.Errorf("registering the rescue key %s: %w", keyName, err)
	}
	session := RescueSession{Server: number, Fingerprint: registered.Fingerprint}
	if err := c.DeactivateRescue(ctx, number); err != nil {
		return session, fmt.Errorf("clearing an earlier rescue activation of server %d: %w", number, err)
	}
	rescue, err := c.ActivateRescue(ctx, number, registered.Fingerprint)
	if err != nil {
		return session, fmt.Errorf("activating the rescue system of server %d: %w", number, err)
	}
	session.HostKeys = rescue.HostKeys
	if err := c.Reset(ctx, number); err != nil {
		return session, fmt.Errorf("resetting server %d into the rescue system: %w", number, err)
	}
	return session, nil
}

// LeaveRescue clears any activation Robot still holds, so the next reset boots the disk, and deletes the session's key.
func (c *Client) LeaveRescue(ctx context.Context, s RescueSession) error {
	if err := c.DeactivateRescue(ctx, s.Server); err != nil {
		return fmt.Errorf("clearing the rescue activation of server %d: %w", s.Server, err)
	}
	if err := c.DeleteKey(ctx, s.Fingerprint); err != nil {
		return fmt.Errorf("deleting the rescue key %s: %w", s.Fingerprint, err)
	}
	return nil
}

// DeleteKeys deletes every key whose name begins with prefix.
func (c *Client) DeleteKeys(ctx context.Context, prefix string) error {
	keys, err := c.ListKeys(ctx)
	if err != nil {
		return fmt.Errorf("listing Robot keys: %w", err)
	}
	for _, k := range keys {
		if !strings.HasPrefix(k.Name, prefix) {
			continue
		}
		if err := c.DeleteKey(ctx, k.Fingerprint); err != nil {
			return fmt.Errorf("deleting the Robot key %s: %w", k.Name, err)
		}
	}
	return nil
}

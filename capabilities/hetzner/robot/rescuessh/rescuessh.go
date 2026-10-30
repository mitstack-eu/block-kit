// Package rescuessh runs a block's fixed rescue script on a Robot server's rescue system over SSH.
package rescuessh

import (
	"bytes"
	"context"
	"crypto"
	"errors"
	"fmt"
	"log"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	user        = "root"
	dialTimeout = 10 * time.Second
	// MaxOutput is how much of a script's output a session keeps.
	MaxOutput = 64 << 10
)

type Config struct {
	// Script is sent as the stdin of every Run, so the session runs nothing else.
	Script []byte
	// Interval is the wait between dials while the rescue system boots; zero waits 10 seconds.
	Interval time.Duration
}

type Session struct {
	client *ssh.Client
	script []byte
}

// Dial retries while the rescue system of server still boots, and stops at once on a host key it refuses. It accepts
// only a host key whose MD5 or SHA256 fingerprint is in hostKeys, and any key when Robot named none, since the
// session then carries no secret.
func (c Config) Dial(ctx context.Context, addr string, key crypto.Signer, server int64, hostKeys []string) (*Session, error) {
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		return nil, fmt.Errorf("wrapping the rescue key: %w", err)
	}
	config := &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: hostKeyCheck(server, hostKeys), Timeout: dialTimeout}
	interval := c.Interval
	if interval == 0 {
		interval = 10 * time.Second
	}
	for {
		client, err := dial(ctx, addr, config)
		var refused *hostKeyRefused
		switch {
		case err == nil:
			return &Session{client: client, script: c.Script}, nil
		case errors.As(err, &refused):
			return nil, refused.error
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("the rescue system of server %d never admitted the session's key: %w (last attempt: %v)", server, ctx.Err(), err)
		case <-time.After(interval):
		}
	}
}

func hostKeyCheck(server int64, hostKeys []string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		md5, sha := ssh.FingerprintLegacyMD5(key), ssh.FingerprintSHA256(key)
		if len(hostKeys) == 0 {
			log.Printf("rescuessh: server %d: Robot named no host key for the rescue system, so the session accepts %s and sends it no secret", server, sha)
			return nil
		}
		if slices.Contains(hostKeys, md5) || slices.Contains(hostKeys, sha) {
			return nil
		}
		return &hostKeyRefused{fmt.Errorf("the rescue system of server %d presented host key %s, none of the host keys Robot named (%s)", server, sha, strings.Join(hostKeys, ", "))}
	}
}

// hostKeyRefused marks a handshake the host key check refused, which no retry can fix.
type hostKeyRefused struct{ error }

func (e *hostKeyRefused) Unwrap() error { return e.error }

func dial(ctx context.Context, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	conn, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	// The handshake takes no context, so a rescue system that accepts the connection and stalls is cut off here.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if !stop() || err != nil {
		conn.Close()
		return nil, errors.Join(err, ctx.Err())
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// Run runs the script with args and returns its output, ended at MaxOutput.
func (s *Session) Run(ctx context.Context, args ...string) (string, error) {
	session, err := s.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("opening a rescue session: %w", err)
	}
	defer session.Close()
	var out output
	session.Stdin, session.Stdout, session.Stderr = bytes.NewReader(s.script), &out, &out
	done := make(chan error, 1)
	go func() { done <- session.Run(strings.Join(append([]string{"bash", "-s", "--"}, args...), " ")) }()
	select {
	case err = <-done:
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("the rescue script %s failed: %w; its output: %s", args[0], err, out.String())
	}
	return out.String(), nil
}

func (s *Session) Close() error { return s.client.Close() }

// output keeps the first MaxOutput bytes the script wrote, from stdout and stderr at once.
type output struct {
	mu  sync.Mutex
	buf []byte
}

func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.buf = append(o.buf, p[:min(len(p), max(MaxOutput-len(o.buf), 0))]...)
	return len(p), nil
}

func (o *output) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return string(o.buf)
}

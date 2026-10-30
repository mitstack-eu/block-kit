// Package rescuesshtest is a rescue system's SSH server on 127.0.0.1 that runs each command with bash, for block tests.
package rescuesshtest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Command is one command a session ran and the stdin it was sent.
type Command struct {
	Line, Stdin string
}

type Host struct {
	// Addr is where the host listens.
	Addr string

	admit  func(authorizedKey string) bool
	signer ssh.Signer

	mu   sync.Mutex
	env  []string
	sent []Command
}

// New listens until the test ends, admitting a client key only while admit answers true for its authorized_keys form.
func New(t testing.TB, admit func(authorizedKey string) bool) *Host {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating the rescue host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatalf("wrapping the rescue host key: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the rescue host: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	h := &Host{Addr: ln.Addr().String(), admit: admit, signer: signer}
	config := &ssh.ServerConfig{PublicKeyCallback: h.checkKey}
	config.AddHostKey(signer)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go h.handle(conn, config)
		}
	}()
	return h
}

// SetEnv is the environment each later command runs with.
func (h *Host) SetEnv(env []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.env = env
}

func (h *Host) FingerprintMD5() string { return ssh.FingerprintLegacyMD5(h.signer.PublicKey()) }

func (h *Host) FingerprintSHA256() string { return ssh.FingerprintSHA256(h.signer.PublicKey()) }

// Sent lists the commands run so far, each recorded once it exited.
func (h *Host) Sent() []Command {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Command(nil), h.sent...)
}

func (h *Host) checkKey(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	if h.admit(strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))) {
		return nil, nil
	}
	return nil, errors.New("this key is not admitted to the rescue system")
}

func (h *Host) handle(conn net.Conn, config *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, requests, err := nc.Accept()
		if err != nil {
			continue
		}
		go h.exec(ch, requests)
	}
}

func (h *Host) exec(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	for req := range requests {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		var command struct{ Command string }
		_ = ssh.Unmarshal(req.Payload, &command)
		_ = req.Reply(true, nil)
		h.mu.Lock()
		env := h.env
		h.mu.Unlock()
		var stdin bytes.Buffer
		cmd := exec.Command("bash", "-c", command.Command)
		cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = env, io.TeeReader(ch, &stdin), ch, ch.Stderr()
		status := 0
		if err := cmd.Run(); err != nil {
			status = 1
		}
		h.mu.Lock()
		h.sent = append(h.sent, Command{Line: command.Command, Stdin: stdin.String()})
		h.mu.Unlock()
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(status)}))
		return
	}
}

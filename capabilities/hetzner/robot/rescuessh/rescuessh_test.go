package rescuessh_test

// A rescue session against rescuesshtest's host: which host keys it accepts, how it waits for the boot, and how it runs the script.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/hetzner/robot/rescuessh"
	"github.com/mitstack-eu/block-kit/capabilities/hetzner/robot/rescuessh/rescuesshtest"
)

const (
	server = int64(3041839)
	script = "printf 'ran %s\\n' \"$*\"\n"
)

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return private
}

func admitAll(string) bool { return true }

func dial(t *testing.T, ctx context.Context, h *rescuesshtest.Host, hostKeys []string) (*rescuessh.Session, error) {
	t.Helper()
	config := rescuessh.Config{Script: []byte(script), Interval: 10 * time.Millisecond}
	return config.Dial(ctx, h.Addr, newKey(t), server, hostKeys)
}

func TestDial_AcceptsAHostKeyRobotNamedOrAnyWhenItNamedNone(t *testing.T) {
	h := rescuesshtest.New(t, admitAll)
	for _, tc := range []struct {
		name     string
		hostKeys []string
	}{
		{"none named", nil},
		{"named by MD5", []string{h.FingerprintMD5()}},
		{"named by SHA256", []string{"SHA256:another", h.FingerprintSHA256()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := dial(t, context.Background(), h, tc.hostKeys)
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			defer s.Close()
			if out, err := s.Run(context.Background(), "read"); err != nil || out != "ran read\n" {
				t.Errorf("Run(read) = %q, %v, want the script's output", out, err)
			}
		})
	}
}

func TestDial_RefusesAHostKeyRobotDidNotNameAtOnceAndRunsNothing(t *testing.T) {
	h := rescuesshtest.New(t, admitAll)
	config := rescuessh.Config{Script: []byte(script), Interval: time.Hour}
	start := time.Now()

	_, err := config.Dial(context.Background(), h.Addr, newKey(t), server, []string{"SHA256:bQg19BgeJ2qso3fT1u1q9RrW25lITHoeLNIli/UWguY"})

	if err == nil || !strings.Contains(err.Error(), "none of the host keys Robot named") || !strings.Contains(err.Error(), h.FingerprintSHA256()) {
		t.Fatalf("Dial = %v, want a refusal naming the presented key %s", err, h.FingerprintSHA256())
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("Dial took %s, want it to stop at once rather than retry a refused key", elapsed)
	}
	if sent := h.Sent(); len(sent) != 0 {
		t.Errorf("the host ran %v, want nothing", sent)
	}
}

func TestDial_RetriesUntilTheRescueSystemAdmitsTheKey(t *testing.T) {
	var attempts atomic.Int32
	h := rescuesshtest.New(t, func(string) bool { return attempts.Add(1) > 3 })

	s, err := dial(t, context.Background(), h, nil)

	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	s.Close()
	if attempts.Load() < 4 {
		t.Errorf("the host saw %d attempts, want the dial retried until the fourth", attempts.Load())
	}
}

func TestDial_GivesUpWhenTheRescueSystemNeverAdmitsTheKey(t *testing.T) {
	h := rescuesshtest.New(t, func(string) bool { return false })
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_, err := dial(t, ctx, h, nil)

	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "never admitted") {
		t.Fatalf("Dial = %v, want the deadline and that the rescue system never admitted the key", err)
	}
}

func TestDial_GivesUpOnARescueSystemThatNeverAnswersTheHandshake(t *testing.T) {
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { silent.Close() })
	go func() {
		for {
			conn, err := silent.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { conn.Close() })
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)

	go func() {
		_, err := rescuessh.Config{Script: []byte(script)}.Dial(ctx, silent.Addr().String(), newKey(t), server, nil)
		done <- err
	}()

	select {
	case err = <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("Dial still waits on the handshake after its context ended")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Dial = %v, want the deadline", err)
	}
}

func TestRun_SendsTheFixedScriptAsStdinWithItsArguments(t *testing.T) {
	h := rescuesshtest.New(t, admitAll)
	s, err := dial(t, context.Background(), h, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer s.Close()

	out, err := s.Run(context.Background(), "write", "4000", "10.0.1.2/24")

	if err != nil || out != "ran write 4000 10.0.1.2/24\n" {
		t.Fatalf("Run = %q, %v, want the script's output", out, err)
	}
	want := []rescuesshtest.Command{{Line: "bash -s -- write 4000 10.0.1.2/24", Stdin: script}}
	if sent := h.Sent(); len(sent) != 1 || sent[0] != want[0] {
		t.Errorf("the host ran %+v, want %+v", sent, want)
	}
}

func TestRun_KeepsTheFirst64KiBOfOutputAndQuotesItWhenTheScriptFails(t *testing.T) {
	h := rescuesshtest.New(t, admitAll)
	config := rescuessh.Config{Script: []byte("head -c 100000 /dev/zero | tr '\\0' x; exit 3\n")}
	s, err := config.Dial(context.Background(), h.Addr, newKey(t), server, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer s.Close()

	_, err = s.Run(context.Background(), "write")

	if err == nil || !strings.Contains(err.Error(), "the rescue script write failed") {
		t.Fatalf("Run = %v, want the script's failure", err)
	}
	if _, quoted, _ := strings.Cut(err.Error(), "its output: "); quoted != strings.Repeat("x", rescuessh.MaxOutput) {
		t.Errorf("the error quotes %d bytes of output, want the first %d", len(quoted), rescuessh.MaxOutput)
	}
}

func TestRun_StopsWhenTheContextEnds(t *testing.T) {
	h := rescuesshtest.New(t, admitAll)
	s, err := rescuessh.Config{Script: []byte("sleep 30\n")}.Dial(context.Background(), h.Addr, newKey(t), server, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()

	_, err = s.Run(ctx, "write")

	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 10*time.Second {
		t.Errorf("Run = %v after %s, want the deadline at once", err, time.Since(start))
	}
}

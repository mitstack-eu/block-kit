package rescuessh_test

// A rescue session against rescuesshtest's host: which host keys it accepts, how it waits for the boot, and how it runs the script.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/hetzner/robot/rescuessh"
	"github.com/mitstack-eu/block-kit/capabilities/hetzner/robot/rescuessh/rescuesshtest"
	"golang.org/x/crypto/ssh"
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

// A script names its failure at its end, and a caller may log the error before it scrubs the output.
func TestRun_ReturnsTheLastWholeLinesThatFitAndTheExitErrorWhenTheScriptFails(t *testing.T) {
	h := rescuesshtest.New(t, admitAll)
	for _, lineLength := range []int{64, 100} {
		t.Run(fmt.Sprintf("%d-byte lines", lineLength), func(t *testing.T) {
			pad := strings.Repeat("x", lineLength-len("line 00001 \n"))
			var lines []string
			for i := 1; i <= 2000; i++ {
				lines = append(lines, fmt.Sprintf("line %05d %s\n", i, pad))
			}
			config := rescuessh.Config{Script: []byte("for i in $(seq 1 2000); do printf 'line %05d %s\\n' \"$i\" " + pad + "; done; exit 3\n")}
			s, err := config.Dial(context.Background(), h.Addr, newKey(t), server, nil)
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			defer s.Close()

			out, err := s.Run(context.Background(), "write")

			var exit *ssh.ExitError
			if !errors.As(err, &exit) || !strings.Contains(err.Error(), "the rescue script write failed") || strings.Contains(err.Error(), "line ") {
				t.Fatalf("Run error = %v, want the script's exit error, naming the script and quoting none of its output", err)
			}
			if want := strings.Join(lines[len(lines)-rescuessh.MaxOutput/lineLength:], ""); out != want {
				t.Errorf("Run returned %d bytes starting %.20q, want the last %d whole lines, %d bytes starting %.20q", len(out), out, rescuessh.MaxOutput/lineLength, len(want), want)
			}
		})
	}
}

func TestRun_RunsTheScriptWithNoArguments(t *testing.T) {
	h := rescuesshtest.New(t, admitAll)
	s, err := rescuessh.Config{Script: []byte("echo read the logs; exit 4\n")}.Dial(context.Background(), h.Addr, newKey(t), server, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer s.Close()

	out, err := s.Run(context.Background())

	var exit *ssh.ExitError
	if out != "read the logs\n" || !errors.As(err, &exit) || !strings.Contains(err.Error(), "the rescue script failed") {
		t.Errorf("Run() = %q, %v, want the script's output and its exit error", out, err)
	}
	if sent := h.Sent(); len(sent) != 1 || sent[0].Line != "bash -s --" {
		t.Errorf("the host ran %+v, want the script alone under bash -s --", sent)
	}
}

func TestRun_StopsWhenTheContextEndsAndReturnsTheOutputSoFar(t *testing.T) {
	h := rescuesshtest.New(t, admitAll)
	s, err := rescuessh.Config{Script: []byte("echo started; exec timeout 30 tail -f /dev/null\n")}.Dial(context.Background(), h.Addr, newKey(t), server, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()

	out, err := s.Run(ctx, "write")

	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 10*time.Second || out != "started\n" {
		t.Errorf("Run = %q, %v after %s, want the output so far and the deadline at once", out, err, time.Since(start))
	}
}

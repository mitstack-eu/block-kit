package hostexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func runSSH(ctx context.Context, h Host, script string) error {
	_, err := runSSHCommand(ctx, h, script, false)
	return err
}

// runSSHOutput never returns the partial output the script printed before failing.
func runSSHOutput(ctx context.Context, h Host, script string) (string, error) {
	return runSSHCommand(ctx, h, script, true)
}

// runSSHCommand delivers the script on stdin so neither it nor the PEM identity ever reaches argv, and the identity file lives only as long as the call needs it.
func runSSHCommand(ctx context.Context, h Host, script string, captureOutput bool) (stdout string, err error) {
	keyFile, err := writeIdentityFile(h.Credential)
	if err != nil {
		return "", fmt.Errorf("hostexec: %w", err)
	}
	// A failed removal leaves the PEM on disk so it must not be dropped, but it must not mask an error the run already produced.
	defer func() {
		if rmErr := os.Remove(keyFile); rmErr != nil {
			if err == nil {
				err = fmt.Errorf("hostexec: ssh: remove identity file %s: %w", keyFile, rmErr)
				stdout = ""
			} else {
				fmt.Fprintf(os.Stderr, "hostexec: ssh: remove identity file %s: %v\n", keyFile, rmErr)
			}
		}
	}()

	cmd := exec.CommandContext(ctx, "ssh",
		"-i", keyFile,
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-T",
		h.Target,
	)
	setCmdEnv(cmd, h.Env)
	cmd.Stdin = strings.NewReader(script)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	var stdoutBuf strings.Builder
	if captureOutput {
		cmd.Stdout = &stdoutBuf
	}
	// WaitDelay caps how long Wait drains pipes a killed ssh's grandchild holds open, so the call returns promptly.
	cmd.WaitDelay = subprocessWaitDelay

	if runErr := cmd.Run(); runErr != nil {
		// A cancelled context killed the subprocess, which is why it failed.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("hostexec: ssh: %w", ctxErr)
		}
		// A leaked grandchild holding stderr past WaitDelay yields exec.ErrWaitDelay even when ssh exited zero.
		if errors.Is(runErr, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
			if captureOutput {
				return strings.TrimSpace(stdoutBuf.String()), nil
			}
			return "", nil
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("hostexec: ssh: %w: %s", runErr, msg)
		}
		return "", fmt.Errorf("hostexec: ssh: %w", runErr)
	}
	if captureOutput {
		return strings.TrimSpace(stdoutBuf.String()), nil
	}
	return "", nil
}

// writeIdentityFile leaves removal of the file to the caller.
func writeIdentityFile(pem string) (string, error) {
	tf, err := os.CreateTemp("", "hostexec-ssh-key-*")
	if err != nil {
		return "", fmt.Errorf("create identity file: %w", err)
	}
	name := tf.Name()

	if _, err := tf.WriteString(pem); err != nil {
		tf.Close()
		os.Remove(name)
		return "", fmt.Errorf("write identity file: %w", err)
	}
	if err := tf.Close(); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("close identity file: %w", err)
	}
	// chmod pins 0600 explicitly rather than relying on the process's umask.
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("chmod identity file: %w", err)
	}
	return name, nil
}

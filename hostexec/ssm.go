package hostexec

// The ssm driver, lifted from web-app-aws/postgres-db/provision.go's
// runShellOnInstance and pollCommandInvocation.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// runSSM sends script to h.Target as an AWS-RunShellScript document and
// waits for it to reach a terminal state.
func runSSM(ctx context.Context, h Host, script string, o sendOptions) error {
	commandID, err := sendSSMCommand(ctx, h, script, o)
	if err != nil {
		return err
	}
	pollCtx, cancel := ssmPollContext(ctx, o)
	defer cancel()
	return waitForSSMPoll(pollCtx, o, h, commandID)
}

// runSSMOutput sends script exactly as runSSM does — sharing sendSSMCommand
// and waitForSSMTerminalStatus rather than duplicating that send/poll path —
// and, only once the poll has already reached Success, issues a second
// get-command-invocation call to read the captured stdout. Run itself must
// never take this extra call, which is why it stays a separate function
// rather than folding into runSSM. The output read runs on the same context
// as the poll, so a detached poll's read also outlives cancellation.
func runSSMOutput(ctx context.Context, h Host, script string, o sendOptions) (string, error) {
	commandID, err := sendSSMCommand(ctx, h, script, o)
	if err != nil {
		return "", err
	}
	pollCtx, cancel := ssmPollContext(ctx, o)
	defer cancel()
	if err := waitForSSMPoll(pollCtx, o, h, commandID); err != nil {
		return "", err
	}
	return ssmCommandOutput(pollCtx, h, commandID)
}

// ssmPollContext returns the context the poll and output read run on. It is
// ctx itself unless o asks for a detached poll, in which case it keeps ctx's
// values but drops its cancellation and deadline, and is bounded by
// SSMPollTimeout instead: the command is already running on the host, and
// cancelling ctx does not stop it there.
func ssmPollContext(ctx context.Context, o sendOptions) (context.Context, context.CancelFunc) {
	if !o.detachedPoll {
		return ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), SSMPollTimeout)
}

// waitForSSMPoll runs waitForSSMTerminalStatus on pollCtx. A detached poll's
// context ends only when the SSMPollTimeout bound runs out, so for one it
// reports that bound rather than a bare context error.
func waitForSSMPoll(pollCtx context.Context, o sendOptions, h Host, commandID string) error {
	err := waitForSSMTerminalStatus(pollCtx, h, commandID)
	if err != nil && o.detachedPoll {
		if ctxErr := pollCtx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
			return fmt.Errorf("hostexec: ssm command %s timed out waiting for a terminal state after %s: %w", commandID, SSMPollTimeout, ctxErr)
		}
	}
	return err
}

// sendSSMCommand issues the send-command call and returns the resulting
// CommandId. The argv order is exact — a caller depends on the document
// name, instance id and region arriving in precisely this sequence, then
// --comment and --timeout-seconds when o sets them, then --parameters last,
// with executionTimeout ahead of commands when o sets it. The zero
// sendOptions adds none of them.
func sendSSMCommand(ctx context.Context, h Host, script string, o sendOptions) (string, error) {
	// The aws CLI's shorthand parser decodes only \" and \\ inside the
	// commands list, so the JSON handed to it must carry <, > and & literally:
	// json.Marshal's HTML escapes (\u003e and friends) would reach the host
	// as text and turn every redirect into an argument.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(strings.Split(script, "\n")); err != nil {
		return "", fmt.Errorf("hostexec: ssm: encoding script as commands JSON: %w", err)
	}
	commandsJSON := strings.TrimSuffix(buf.String(), "\n")

	args := []string{
		"ssm", "send-command",
		"--document-name", "AWS-RunShellScript",
		"--instance-ids", h.Target,
		"--region", h.Region,
		"--query", "Command.CommandId",
		"--output", "text",
	}
	if o.commentSet {
		args = append(args, "--comment", o.comment)
	}
	if o.deliveryTimeoutSet {
		args = append(args, "--timeout-seconds", seconds(o.deliveryTimeout))
	}
	params := "commands=" + commandsJSON
	if o.executionTimeoutSet {
		params = `executionTimeout=["` + seconds(o.executionTimeout) + `"],` + params
	}
	args = append(args, "--parameters", params)

	cmd := exec.CommandContext(ctx, "aws", args...)
	setCmdEnv(cmd, h.Env)
	cmd.WaitDelay = subprocessWaitDelay

	var stderr strings.Builder
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr.Write(ee.Stderr)
		}
		// A cancelled context killed the subprocess; that is why it failed,
		// not whatever exit status the kill produced.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("hostexec: ssm send-command: %w", ctxErr)
		}
		// A leaked grandchild holding the subprocess's stderr pipe open past
		// WaitDelay produces exec.ErrWaitDelay even when aws itself exited
		// zero; that is not a failure of the send this call cares about, but
		// cmd.Output may then return truncated stdout — the empty-CommandId
		// guard below still catches that case.
		if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
			commandID := strings.TrimSpace(string(out))
			if commandID == "" {
				return "", errors.New("hostexec: ssm send-command returned no CommandId")
			}
			return commandID, nil
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("hostexec: ssm send-command: %w: %s", err, msg)
		}
		return "", fmt.Errorf("hostexec: ssm send-command: %w", err)
	}
	commandID := strings.TrimSpace(string(out))
	if commandID == "" {
		return "", errors.New("hostexec: ssm send-command returned no CommandId")
	}
	return commandID, nil
}

// waitForSSMTerminalStatus polls get-command-invocation until commandID
// reaches a terminal state, returning nil only on Success. A poll
// subprocess error (the invocation not visible yet, a transient CLI
// failure) is treated as still-pending and retried, never as terminal.
func waitForSSMTerminalStatus(ctx context.Context, h Host, commandID string) error {
	var lastPollErr error
	deadline := time.Now().Add(SSMPollTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return fmt.Errorf("hostexec: ssm poll: %w", ctx.Err())
		default:
		}

		status, pollErr := ssmCommandStatus(ctx, h, commandID)
		if pollErr == nil {
			switch status {
			case "Success":
				return nil
			case "Failed", "Cancelled", "TimedOut", "Delivery Timed Out":
				return fmt.Errorf("hostexec: ssm command %s reached terminal state %s", commandID, status)
			}
		} else {
			lastPollErr = pollErr
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("hostexec: ssm poll: %w", ctx.Err())
		case <-time.After(SSMPollInterval):
		}
	}
	if lastPollErr != nil {
		return fmt.Errorf("hostexec: ssm command %s timed out waiting for a terminal state after %s — last poll failure: %v", commandID, SSMPollTimeout, lastPollErr)
	}
	return fmt.Errorf("hostexec: ssm command %s timed out waiting for a terminal state after %s", commandID, SSMPollTimeout)
}

// ssmCommandStatus reads the current status of commandID. Any error from
// running the CLI — including a not-yet-visible invocation — is returned
// as-is so the caller retries rather than treats it as terminal.
func ssmCommandStatus(ctx context.Context, h Host, commandID string) (string, error) {
	cmd := exec.CommandContext(ctx, "aws",
		"ssm", "get-command-invocation",
		"--command-id", commandID,
		"--instance-id", h.Target,
		"--region", h.Region,
		"--query", "Status",
		"--output", "text",
	)
	setCmdEnv(cmd, h.Env)
	cmd.WaitDelay = subprocessWaitDelay

	out, err := cmd.Output()
	if err != nil {
		// A leaked grandchild holding this call's own stderr pipe open past
		// WaitDelay must not turn an otherwise-successful poll into a
		// spurious retry.
		if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
			return strings.TrimSpace(string(out)), nil
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
				return "", fmt.Errorf("%w: %s", err, msg)
			}
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ssmCommandOutput reads the captured stdout of commandID via a second
// get-command-invocation call, issued only once waitForSSMTerminalStatus
// has already found commandID Success — never before, and never interleaved
// with the Status polls themselves.
func ssmCommandOutput(ctx context.Context, h Host, commandID string) (string, error) {
	cmd := exec.CommandContext(ctx, "aws",
		"ssm", "get-command-invocation",
		"--command-id", commandID,
		"--instance-id", h.Target,
		"--region", h.Region,
		"--query", "StandardOutputContent",
		"--output", "text",
	)
	setCmdEnv(cmd, h.Env)
	cmd.WaitDelay = subprocessWaitDelay

	out, err := cmd.Output()
	if err != nil {
		// A leaked grandchild holding this call's own stderr pipe open past
		// WaitDelay must not turn an otherwise-successful read into a
		// spurious failure.
		if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
			return strings.TrimSpace(string(out)), nil
		}
		// A cancelled context killed the subprocess; that is why it failed,
		// not whatever exit status the kill produced.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("hostexec: ssm get-command-invocation (output): %w", ctxErr)
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
				return "", fmt.Errorf("hostexec: ssm get-command-invocation (output): %w: %s", err, msg)
			}
		}
		return "", fmt.Errorf("hostexec: ssm get-command-invocation (output): %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

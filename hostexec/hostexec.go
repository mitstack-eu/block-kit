// Package hostexec runs a shell script on a single host over one of two
// transports — an AWS Systems Manager (SSM) RunShellScript document, or an
// SSH connection authenticated by a supplied private key — behind ONE call,
// so an operator's provisioning code names a Kind and a Target and never
// shells out to an aws or ssh subprocess directly (MIT-2440).
//
// Both drivers exist because the reference operators that provision a box
// reach it two different ways depending on how it is fenced: an
// SSM-managed EC2 instance has no open inbound SSH port at all, while other
// hosts — and every future non-EC2 target — are reachable only over ssh.
// Each driver had already been hand-rolled once, inside
// web-app-aws/postgres-db/provision.go's runShellOnInstance /
// pollCommandInvocation poll loop; this package is that logic lifted out so
// a second operator does not re-derive the same send-command argv, or the
// same secret-handling decision, from a blank page.
//
// The two drivers give different guarantees about a secret embedded in the
// script, so they must not be treated as interchangeable on that point. The
// ssh driver carries the script on its subprocess's stdin, so a secret
// embedded in it never appears on a command line or in a log line. The ssm
// driver instead passes the script as a SendCommand parameter, which AWS
// itself records in CloudTrail — an inherent property of the
// AWS-RunShellScript document, not a shortcut this package took. A caller
// using the ssm driver must therefore never embed a secret in the script; it
// must instead pass the script a REFERENCE it can resolve on the host once
// it is already running there — an SSM SecureString parameter name, say —
// and let the host fetch the value itself, exactly as the original
// runShellOnInstance did (web-app-aws/postgres-db/provision.go:113) by
// handing the database password over through an SSM parameter rather than
// the script text.
//
// This is deliberately not a general remote-execution library. It runs one
// script to completion and reports success or failure; it does not stream
// output, hold a persistent connection, or retry anything beyond the SSM
// poll loop's own transient-error retry. An operator that needs a live
// shell or incremental output needs a different tool.
//
// The ssm driver takes opt-in SendOptions: WithComment, WithDeliveryTimeout
// and WithExecutionTimeout add the matching send-command arguments, and
// WithDetachedPoll keeps polling for a terminal status after the caller's
// context is cancelled, because cancelling the caller does not stop a command
// already running on the host. Every poll, detached or not, gives up after
// SSMPollTimeout when neither timeout option is set; with either set it waits
// at least the delivery timeout plus the execution timeout plus one
// SSMPollInterval, and never less than SSMPollTimeout. The options are
// ssm-only; the ssh driver rejects any of them. A call with no option sends
// the base send-command argv and returns once ctx is cancelled.
package hostexec

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Kind selects which transport Run uses to reach the host.
type Kind string

const (
	// KindSSM drives the host through AWS Systems Manager RunShellScript.
	KindSSM Kind = "aws-ssm"
	// KindSSH drives the host over an SSH connection.
	KindSSH Kind = "ssh"
)

// Host describes where a script runs and how to reach it.
type Host struct {
	// Kind selects the driver.
	Kind Kind
	// Target is the driver-specific address: an EC2 instance id for ssm,
	// or "user@hostname" for ssh.
	Target string
	// Credential is the ssh driver's PEM private key. Unused by ssm, but
	// must never leak even when a caller populates it on a reused Host.
	Credential string
	// Region is the AWS region the ssm driver operates in. Required for
	// ssm, and never defaulted — a package default that shifts silently
	// strands whatever already ran under the real one.
	Region string
	// Env is the driver subprocess's complete process environment. A nil or
	// empty Env means the subprocess inherits the ambient environment;
	// a non-empty Env is used VERBATIM, matching exec.Cmd.Env's own
	// semantics, so it must already include anything the subprocess needs
	// (PATH, credentials, ...) — cloudid.Env returns exactly such a
	// complete environment, ready to pass here.
	Env []string
}

// SSMPollInterval is how often the ssm driver polls get-command-invocation.
// A package variable so a test can shrink it rather than sleep for real.
var SSMPollInterval = 5 * time.Second

// SSMPollTimeout is how long the ssm driver waits for a terminal status
// before giving up when the call sets neither WithDeliveryTimeout nor
// WithExecutionTimeout. A call that sets either waits the delivery timeout
// plus the execution timeout plus one SSMPollInterval, with SSMPollTimeout as
// the floor. A package variable for the same reason as SSMPollInterval.
var SSMPollTimeout = 10 * time.Minute

// Run executes script on h and returns once it reaches a terminal state.
//
// Kind, Target and (for ssm) Region are validated before anything is
// spawned: an unrecognised or empty Kind, an empty Target, or an empty
// Region on the ssm driver are all rejected up front rather than falling
// back to a default or a partially-started subprocess.
//
// opts adjust how the ssm driver sends and waits for the command: a comment,
// a delivery timeout, an execution timeout, and a poll detached from ctx's
// cancellation. They are validated before anything is spawned too, and they
// are ssm-only: any option at all on the ssh driver is rejected rather than
// ignored. With no option, the ssm driver sends the base send-command argv
// and the whole call stops promptly once ctx is cancelled. With
// WithDetachedPoll, send-command still runs on ctx, but once the command is
// sent Run keeps polling after ctx is cancelled, so it can outlive the
// cancellation by up to the poll bound: SSMPollTimeout, or with a delivery or
// execution timeout set, their sum plus one SSMPollInterval if that is longer.
func Run(ctx context.Context, h Host, script string, opts ...SendOption) error {
	if err := validateHost(h); err != nil {
		return err
	}
	switch h.Kind {
	case KindSSM:
		o, err := resolveSendOptions(opts)
		if err != nil {
			return err
		}
		return runSSM(ctx, h, script, o)
	case KindSSH:
		if err := refuseSendOptions(h.Kind, opts); err != nil {
			return err
		}
		return runSSH(ctx, h, script)
	default:
		return fmt.Errorf("hostexec: unrecognised host kind %q", h.Kind)
	}
}

// RunOutput executes script on h exactly as Run does, but additionally
// captures and returns its stdout once it reaches a terminal state, with
// only the OUTER whitespace trimmed — internal newlines round-trip
// verbatim. A failed send, a poll that never reaches a terminal state, a
// terminal state other than Success, or a non-zero ssh exit all return
// ("", err); RunOutput never hands back partial output alongside an error.
//
// RunOutput shares Run's validation and, for the ssm driver, Run's own
// send/poll path (sendSSMCommand and waitForSSMTerminalStatus), so it sends
// script into AWS-RunShellScript's "commands" parameter exactly as Run
// does — and AWS records that same script in CloudTrail. A caller using the
// ssm driver must therefore never embed a secret in script here either, for
// exactly the reason given on Run: pass a reference the host resolves
// itself once it is already running, never the secret value. The ssh
// driver instead reads the stdout its own subprocess already produced;
// script itself still travels only on that subprocess's stdin, never argv.
//
// RunOutput takes the same ssm-only opts as Run, validated and applied the
// same way. With WithDetachedPoll, the output read runs on the same detached
// context as the poll, so it too can outlive ctx's cancellation by up to the
// same poll bound Run describes.
func RunOutput(ctx context.Context, h Host, script string, opts ...SendOption) (string, error) {
	if err := validateHost(h); err != nil {
		return "", err
	}
	switch h.Kind {
	case KindSSM:
		o, err := resolveSendOptions(opts)
		if err != nil {
			return "", err
		}
		return runSSMOutput(ctx, h, script, o)
	case KindSSH:
		if err := refuseSendOptions(h.Kind, opts); err != nil {
			return "", err
		}
		return runSSHOutput(ctx, h, script)
	default:
		return "", fmt.Errorf("hostexec: unrecognised host kind %q", h.Kind)
	}
}

// validateHost applies the up-front checks shared by Run and RunOutput: an
// empty or dash-prefixed Target, or (for the ssm driver) a missing Region,
// are all rejected before anything is spawned.
func validateHost(h Host) error {
	if strings.TrimSpace(h.Target) == "" {
		return fmt.Errorf("hostexec: Target is required (kind %q)", h.Kind)
	}
	if strings.HasPrefix(h.Target, "-") {
		return fmt.Errorf("hostexec: Target %q begins with \"-\" — neither ssh nor the aws CLI has an end-of-options marker, so this would be parsed as an option rather than a host", h.Target)
	}
	if h.Kind == KindSSM && strings.TrimSpace(h.Region) == "" {
		return fmt.Errorf("hostexec: Region is required for the %q driver — it is never defaulted", KindSSM)
	}
	return nil
}

// setCmdEnv applies Host.Env to cmd. A nil or empty env leaves cmd.Env unset
// so the subprocess inherits the ambient environment; a non-empty env is set
// verbatim, matching exec.Cmd.Env's own semantics.
func setCmdEnv(cmd *exec.Cmd, env []string) {
	if len(env) == 0 {
		return
	}
	cmd.Env = env
}

// subprocessWaitDelay bounds how long Wait keeps draining a killed
// subprocess's stdout/stderr pipes after the context that killed it is
// done. Without it, a grandchild the subprocess spawned (a script's own
// child command, say) can keep a pipe's write end open long after the
// direct child has been killed, and Wait blocks on that pipe rather than
// returning — the context cancellation would otherwise not be "honoured"
// promptly at all. Short enough to keep cancellation prompt, long enough
// that a well-behaved subprocess is not force-closed mid-flush.
const subprocessWaitDelay = 1 * time.Second

package dockerhosttest

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
)

// dockerTimeout bounds one docker command and one wait, long enough for an image pull and a database's first setup.
const dockerTimeout = 4 * time.Minute

// Container is a service a Database rendered, run for real through the docker CLI, for a block's tests of its engine.
type Container struct {
	t    testing.TB
	Name string
}

// Docker runs the docker CLI with stdin, answering its output, and its error output beside it on a failure.
func Docker(stdin []byte, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return out.String() + errOut.String(), err
	}
	return out.String(), nil
}

// Scratch is a fresh docker name for what, whose container, volume and network are removed when t ends.
func Scratch(t testing.TB, what string) string {
	t.Helper()
	name := strings.ToLower("dockerhosttest-" + what + "-" + rand.Text()[:8])
	t.Cleanup(func() {
		for _, args := range [][]string{{"rm", "-f", name}, {"volume", "rm", "-f", name}, {"network", "rm", name}} {
			_, _ = Docker(nil, args...)
		}
	})
	return name
}

// Create makes, without starting, a container running s as the host would, on a volume of its own, with env set and on network unless it is empty.
func Create(t testing.TB, s dockerhost.ComposeService, network string, env ...string) Container {
	t.Helper()
	c := Container{t: t, Name: Scratch(t, "db")}
	if _, err := Docker(nil, "image", "inspect", s.Image); err != nil {
		c.Must("pull", "-q", s.Image)
	}
	c.Must(createArgs(c.Name, s, network, env)...)
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := Docker(nil, "logs", "--tail", "40", c.Name)
			t.Logf("%s's last lines:\n%s", c.Name, logs)
		}
	})
	return c
}

// createArgs reads Compose's "$$" back as the "$" the docker CLI passes through.
func createArgs(name string, s dockerhost.ComposeService, network string, env []string) []string {
	_, dataDir, _ := strings.Cut(s.Volumes[0], ":")
	args := []string{
		"create", "--name", name, "--cpus", s.CPUs.String(),
		"--memory", fmt.Sprint(s.MemLimit), "--memory-swap", fmt.Sprint(s.MemswapLimit),
		"--volume", name + ":" + dataDir,
	}
	for _, opt := range s.SecurityOpt {
		args = append(args, "--security-opt", opt)
	}
	args = append(args, "--health-cmd", strings.Join(s.Healthcheck.Test[1:], " "), "--health-interval", "1s")
	for _, e := range env {
		args = append(args, "--env", e)
	}
	if network != "" {
		args = append(args, "--network", network)
	}
	args = append(args, "--entrypoint", s.Entrypoint[0], s.Image)
	for _, word := range s.Entrypoint[1:] {
		args = append(args, strings.ReplaceAll(word, "$$", "$"))
	}
	return append(args, s.Command...)
}

// Must runs the docker CLI and fails the test on an error.
func (c Container) Must(args ...string) string {
	c.t.Helper()
	out, err := Docker(nil, args...)
	if err != nil {
		c.t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (c Container) Logs() string {
	out, _ := Docker(nil, "logs", c.Name)
	return out
}

// WaitFor fails the test unless done holds within the timeout, what naming what it waited for.
func (c Container) WaitFor(what string, done func() bool) {
	c.t.Helper()
	for deadline := time.Now().Add(dockerTimeout); !done(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			c.t.Fatalf("%s never %s", c.Name, what)
		}
	}
}

func (c Container) WaitHealthy() {
	c.t.Helper()
	c.WaitFor("read healthy", func() bool {
		out, _ := Docker(nil, "inspect", "--format", "{{.State.Health.Status}}", c.Name)
		return strings.TrimSpace(out) == "healthy"
	})
}

// Exec runs line through a shell in the container, as the host runs a backup file's line, and fails the test on an error.
func (c Container) Exec(line string, stdin []byte) string {
	c.t.Helper()
	out, err := Docker(stdin, "exec", "-i", c.Name, "sh", "-c", line)
	if err != nil {
		c.t.Fatalf("running %q: %v\n%s", line, err, out)
	}
	return out
}

package dockerhosttest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
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

// Create makes, without starting, a container running s as the host would but for its restart policy, which a test drives itself, on a volume of its own, with env set beside s's own and on network unless it is empty.
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
	for _, k := range slices.Sorted(maps.Keys(s.Labels)) {
		args = append(args, "--label", k+"="+unescape(s.Labels[k]))
	}
	args = append(args, "--health-cmd", strings.Join(s.Healthcheck.Test[1:], " "), "--health-interval", "1s")
	for _, k := range slices.Sorted(maps.Keys(s.Environment)) {
		args = append(args, "--env", k+"="+unescape(s.Environment[k]))
	}
	for _, e := range env {
		args = append(args, "--env", e)
	}
	if network != "" {
		args = append(args, "--network", network)
	}
	args = append(args, "--entrypoint", s.Entrypoint[0], s.Image)
	for _, word := range s.Entrypoint[1:] {
		args = append(args, unescape(word))
	}
	return append(args, s.Command...)
}

func unescape(value string) string { return strings.ReplaceAll(value, "$$", "$") }

// Must runs the docker CLI and fails the test on an error.
func (c Container) Must(args ...string) string {
	c.t.Helper()
	out, err := Docker(nil, args...)
	if err != nil {
		c.t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// WaitForLine follows the container's output, every start's included, and fails the test unless a line holding text comes within the timeout.
func (c Container) WaitForLine(text string) {
	c.t.Helper()
	found, err := follow(func(line string) bool { return strings.Contains(line, text) }, "logs", "--follow", c.Name)
	if !found {
		c.t.Fatalf("%s logged no line holding %q: %v", c.Name, text, err)
	}
}

// WaitHealthy reads the container's health, then Docker's health events from that moment, and fails the test unless it reads healthy within the timeout.
func (c Container) WaitHealthy() {
	c.t.Helper()
	since := time.Now()
	if strings.TrimSpace(c.Must("inspect", "--format", "{{.State.Health.Status}}", c.Name)) == "healthy" {
		return
	}
	found, err := follow(func(line string) bool { return strings.TrimSpace(line) == "health_status: healthy" },
		"events", "--since", fmt.Sprintf("%d.%09d", since.Unix(), since.Nanosecond()),
		"--filter", "container="+c.Name, "--filter", "event=health_status", "--format", "{{.Status}}")
	if !found {
		c.t.Fatalf("%s never read healthy: %v", c.Name, err)
	}
}

// follow runs a docker command that streams lines until one matches or the timeout ends it, and ends the command either way.
func follow(match func(line string) bool, args ...string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		return false, err
	}
	defer r.Close()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout, cmd.Stderr = w, w
	err = cmd.Start()
	w.Close()
	if err != nil {
		return false, err
	}
	defer func() {
		cancel()
		_ = cmd.Wait()
	}()
	lines := bufio.NewScanner(r)
	lines.Buffer(nil, 1<<20)
	for lines.Scan() {
		if match(lines.Text()) {
			return true, nil
		}
	}
	switch {
	case lines.Err() != nil:
		return false, lines.Err()
	case ctx.Err() != nil:
		return false, fmt.Errorf("no line matched within %s", dockerTimeout)
	}
	return false, errors.New("the output ended before a line matched")
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

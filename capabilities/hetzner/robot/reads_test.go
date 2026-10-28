package robot_test

// Repeat reads inside the read window reach Robot once, a write drops what it changes, and Robot's rate-limit answer is its own retryable class.

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/hetzner/robot"
	"github.com/mitstack-eu/block-kit/capabilities/hetzner/robot/robottest"
)

type clock struct{ now time.Time }

func (c *clock) read() time.Time { return c.now }

func (c *clock) advance(d time.Duration) { c.now = c.now.Add(d) }

// windowed is a fake with one vSwitch and a client that keeps the read window NewAt gives it, on a clock the test moves.
func windowed(t *testing.T) (*robottest.Fake, *robot.Client, *clock, int64) {
	t.Helper()
	f, _ := newFake(t)
	id := f.AddVSwitch(name, robot.FirstVLAN, false)
	clk := &clock{now: time.Date(2026, 10, 28, 14, 0, 0, 0, time.UTC)}
	return f, loggedIn(f, robottest.Password, clk), clk, id
}

func loggedIn(f *robottest.Fake, password string, clk *clock) *robot.Client {
	c := robot.NewAt(f.URL, robottest.User, password)
	c.PollInterval = time.Millisecond
	c.Now = clk.read
	return c
}

var (
	serverPath   = "/server/" + strconv.FormatInt(number, 10)
	firewallPath = "/firewall/" + strconv.FormatInt(number, 10)
)

func vswitchPath(id int64) string { return "/vswitch/" + strconv.FormatInt(id, 10) }

func readEach(t *testing.T, c *robot.Client, id int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := c.GetServer(ctx, number); err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	if _, err := c.ListVSwitches(ctx); err != nil {
		t.Fatalf("ListVSwitches: %v", err)
	}
	if _, err := c.GetVSwitch(ctx, id); err != nil {
		t.Fatalf("GetVSwitch: %v", err)
	}
	if _, err := c.GetFirewall(ctx, number); err != nil {
		t.Fatalf("GetFirewall: %v", err)
	}
}

func wantCalls(t *testing.T, f *robottest.Fake, want int, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if got := f.Calls("GET", p); got != want {
			t.Errorf("GET %s reached Robot %d times, want %d", p, got, want)
		}
	}
}

func TestReads_RepeatReadsInsideTheWindowReachRobotOnce(t *testing.T) {
	f, c, clk, id := windowed(t)
	paths := []string{serverPath, "/vswitch", vswitchPath(id), firewallPath}

	for range 3 {
		readEach(t, c, id)
		clk.advance(robot.ReadWindow / 3)
	}
	wantCalls(t, f, 1, paths...)

	readEach(t, c, id)
	wantCalls(t, f, 2, paths...)
}

func TestReads_AnotherClientOfTheSameLoginIsAnsweredFromTheSameWindow(t *testing.T) {
	f, c, clk, _ := windowed(t)

	if _, err := c.GetServer(context.Background(), number); err != nil {
		t.Fatal(err)
	}
	if _, err := loggedIn(f, robottest.Password, clk).GetServer(context.Background(), number); err != nil {
		t.Fatal(err)
	}

	wantCalls(t, f, 1, serverPath)
}

func TestReads_AnotherLoginIsNeverAnsweredFromTheWindow(t *testing.T) {
	f, c, clk, _ := windowed(t)
	if _, err := c.GetServer(context.Background(), number); err != nil {
		t.Fatal(err)
	}

	_, err := loggedIn(f, "a-wrong-password-0002", clk).GetServer(context.Background(), number)

	if robot.ErrorCode(err) != "UNAUTHORIZED" {
		t.Errorf("GetServer with another password inside the window = %v, want Robot's UNAUTHORIZED", err)
	}
	wantCalls(t, f, 2, serverPath)
}

func TestReads_AWriteDropsTheRememberedReadsItChanges(t *testing.T) {
	f, c, clk, id := windowed(t)
	ctx := context.Background()
	readEach(t, c, id)
	other := loggedIn(f, robottest.Password, clk)

	if err := other.SetServerName(ctx, number, name); err != nil {
		t.Fatal(err)
	}
	if err := c.SetFirewall(ctx, number, robot.Firewall{Status: "active", Input: robot.ReplyRules()}); err != nil {
		t.Fatal(err)
	}
	created, err := c.CreateVSwitch(ctx, name+"-second", robot.FirstVLAN+1)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddVSwitchServer(ctx, id, number); err != nil {
		t.Fatal(err)
	}

	server, err := c.GetServer(ctx, number)
	if err != nil || server.Name != name {
		t.Errorf("GetServer after another client named the server reads %q (%v), want %q", server.Name, err, name)
	}
	all, err := c.ListVSwitches(ctx)
	if err != nil || len(all) != 2 || all[1].ID != created.ID {
		t.Errorf("ListVSwitches after a create lists %v (%v), want the new vSwitch %d second", all, err, created.ID)
	}
	vs, err := c.GetVSwitch(ctx, id)
	if err != nil || len(vs.Servers) != 1 {
		t.Errorf("GetVSwitch after the server was added lists %v (%v), want the server", vs.Servers, err)
	}
	if _, err := c.GetFirewall(ctx, number); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, 2, serverPath, "/vswitch", vswitchPath(id), firewallPath)
}

func TestReads_ACallThatWritesReadsRobotAfreshInsideTheWindow(t *testing.T) {
	f, c, _, _ := windowed(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.GetServer(ctx, number); err != nil {
		t.Fatal(err)
	}
	f.EditServer(number, func(_ *robottest.Server, s *robottest.State) { s.Name = "mitstack-sub_other" })

	if _, err := c.Adopt(ctx, number, name); refusedClass(err) != "server-taken" {
		t.Errorf("Adopt of a server renamed since the remembered read = %v, want the server-taken refusal", err)
	}
	f.EditServer(number, func(_ *robottest.Server, s *robottest.State) { s.Name = "" })
	if _, err := c.Couple(ctx, name, number); err != nil {
		t.Errorf("Couple inside the window: %v", err)
	}
	if err := c.WriteFirewall(ctx, number, robot.Firewall{Status: "active", Input: robot.ReplyRules()}); err != nil {
		t.Errorf("WriteFirewall inside the window: %v", err)
	}
}

func TestReads_RobotsRateLimitAnswerIsARetryableClassOfItsOwn(t *testing.T) {
	f, c, _, _ := windowed(t)
	f.SetRateLimited(true)

	_, err := c.GetServer(context.Background(), number)

	if !errors.Is(err, robot.ErrRateLimited) || robot.ErrorCode(err) != "RATE_LIMIT_EXCEEDED" {
		t.Errorf("GetServer past Robot's limit = %v, want robot.ErrRateLimited carrying RATE_LIMIT_EXCEEDED", err)
	}
	f.SetRateLimited(false)
	if _, err := c.GetServer(context.Background(), 7); err == nil || errors.Is(err, robot.ErrRateLimited) {
		t.Errorf("GetServer of a server Robot does not hold = %v, want an error that is not robot.ErrRateLimited", err)
	}
}

func TestReads_ARateLimitedReadIsNotSentAgainInsideTheWindow(t *testing.T) {
	f, c, clk, _ := windowed(t)
	f.SetRateLimited(true)

	for range 2 {
		if _, err := c.GetServer(context.Background(), number); !errors.Is(err, robot.ErrRateLimited) {
			t.Fatalf("GetServer past Robot's limit = %v, want robot.ErrRateLimited", err)
		}
	}
	wantCalls(t, f, 1, serverPath)

	f.SetRateLimited(false)
	clk.advance(robot.ReadWindow)
	if _, err := c.GetServer(context.Background(), number); err != nil {
		t.Errorf("GetServer once the window passed: %v", err)
	}
	wantCalls(t, f, 2, serverPath)
}

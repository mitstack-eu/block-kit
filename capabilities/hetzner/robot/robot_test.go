package robot_test

// Contract tests of the Robot calls against robottest's fake, which answers in the shapes Robot answered a live trial.

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/hetzner/robot"
	"github.com/mitstack-eu/block-kit/capabilities/hetzner/robot/robottest"
	"github.com/mitstack-eu/block-kit/handler"
)

const (
	number = int64(3041839)
	name   = "mitstack-sub_0123456789ab"
)

func newFake(t *testing.T, edit ...func(*robottest.Server)) (*robottest.Fake, *robot.Client) {
	t.Helper()
	f := robottest.New(t)
	s := robottest.Server{Number: number, IP: "65.108.234.220", DC: "HEL1-DC1"}
	for _, e := range edit {
		e(&s)
	}
	f.AddServer(s)
	return f, f.Client()
}

func refusedClass(err error) string {
	var r *handler.Refused
	if errors.As(err, &r) {
		return r.Class
	}
	return ""
}

func TestAdopt_NamesAReadyServerAndReadsItsLocation(t *testing.T) {
	f, c := newFake(t)

	got, err := c.Adopt(context.Background(), number, name)

	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if got.Name != name || f.State(number).Name != name {
		t.Errorf("the server is named %q in the answer and %q in Robot, want %q in both", got.Name, f.State(number).Name, name)
	}
	if got.Location() != "hel1" {
		t.Errorf("Location() = %q for DC %s, want hel1", got.Location(), got.DC)
	}
}

func TestAdopt_RefusesAServerItMayNotInstallOverAndWritesNothing(t *testing.T) {
	cases := []struct {
		name, class string
		edit        func(*robottest.Server)
	}{
		{"a cancelled server", "server-cancelled", func(s *robottest.Server) { s.Cancelled = true }},
		{"a server another subscription named", "server-taken", func(s *robottest.Server) { s.Name = "mitstack-sub_other" }},
		{"a server Robot cannot firewall", "firewall-unavailable", func(s *robottest.Server) { s.NoFirewall = true }},
		{"a server the account does not hold", "server-not-found", func(s *robottest.Server) { s.Number = 7 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, c := newFake(t, tc.edit)

			_, err := c.Adopt(context.Background(), number, name)

			if got := refusedClass(err); got != tc.class {
				t.Errorf("Adopt refused with class %q (%v), want %q", got, err, tc.class)
			}
			if w := f.Writes(); len(w) != 0 {
				t.Errorf("Adopt wrote %v, want nothing", w)
			}
		})
	}
}

func TestAdopt_ReturnsARetryableErrorForAServerNotReadyYet(t *testing.T) {
	_, c := newFake(t, func(s *robottest.Server) { s.Status = "in process" })

	_, err := c.Adopt(context.Background(), number, name)

	if err == nil || refusedClass(err) != "" {
		t.Errorf("Adopt = %v, want an error that is no refusal", err)
	}
}

func TestBootRescue_AdmitsTheOneTimeKeyAloneAndLeaveRescueDeletesIt(t *testing.T) {
	f, c := newFake(t)
	key, err := robot.NewRescueKey()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	session, err := c.BootRescue(ctx, number, name+"-install", key)
	if err != nil {
		t.Fatalf("BootRescue: %v", err)
	}
	booted := f.State(number)
	if booted.Booted != "rescue" || !slices.Equal(booted.RescueKeys, []string{key.AuthorizedKey()}) {
		t.Errorf("the server booted %s admitting %q, want rescue admitting only %q", booted.Booted, booted.RescueKeys, key.AuthorizedKey())
	}
	if len(session.HostKeys) != 0 {
		t.Errorf("HostKeys = %v, want none, as Robot returned on the trial", session.HostKeys)
	}
	if err := c.LeaveRescue(ctx, session); err != nil {
		t.Fatalf("LeaveRescue: %v", err)
	}
	if keys := f.Keys(); len(keys) != 0 {
		t.Errorf("Robot still holds the keys %v, want none", keys)
	}
	if err := c.Reset(ctx, number); err != nil || f.State(number).Booted != "disk" {
		t.Errorf("the reset after LeaveRescue booted %s (%v), want the disk", f.State(number).Booted, err)
	}
}

func TestCouple_CreatesTheVSwitchOnTheLowestFreeVLANAndWaitsUntilTheServerIsReady(t *testing.T) {
	f, c := newFake(t)
	f.AddVSwitch("other", robot.FirstVLAN, false)
	f.AddVSwitch("cancelled", robot.FirstVLAN+1, true)

	vs, err := c.Couple(context.Background(), name, number)

	if err != nil {
		t.Fatalf("Couple: %v", err)
	}
	if vs.VLAN != robot.FirstVLAN+2 || vs.Name != name {
		t.Errorf("Couple made vSwitch %q on VLAN %d, want %q on %d", vs.Name, vs.VLAN, name, robot.FirstVLAN+2)
	}
	if !slices.Equal(vs.Servers, []robot.VSwitchServer{{Number: number, Status: "ready"}}) {
		t.Errorf("Couple returned the servers %v, want the server ready", vs.Servers)
	}
	again, err := c.Couple(context.Background(), name, number)
	if err != nil || again.ID != vs.ID || len(f.VSwitches()) != 3 {
		t.Errorf("a second Couple gave vSwitch %d (%v) among %d, want the same %d among 3", again.ID, err, len(f.VSwitches()), vs.ID)
	}
}

func TestUncouple_WaitsUntilRobotNoLongerListsTheServerSoTheVSwitchCanBeCancelled(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	vs, err := c.Couple(ctx, name, number)
	if err != nil {
		t.Fatal(err)
	}

	if err := c.Uncouple(ctx, vs.ID, number); err != nil {
		t.Fatalf("Uncouple: %v", err)
	}
	if err := c.CancelVSwitch(ctx, vs.ID); err != nil {
		t.Fatalf("CancelVSwitch after Uncouple: %v", err)
	}
	if err := c.CancelVSwitch(ctx, vs.ID); err != nil {
		t.Errorf("a second CancelVSwitch = %v, want the cancelled vSwitch read as done", err)
	}
	if got := f.VSwitches()[0]; !got.Cancelled || len(got.Servers) != 0 {
		t.Errorf("the vSwitch reads %+v, want it cancelled with no server", got)
	}
}

func TestWriteFirewall_SendsNoInternalRuleBackAndWaitsUntilRobotAppliesIt(t *testing.T) {
	f, c := newFake(t)
	allowAll := robot.Rule{Name: "Allow all", Action: "accept"}
	f.SetFirewall(number, robot.Firewall{Status: "active", WhitelistHOS: true, Output: append(slices.Clone(robottest.InternalRules), allowAll)})
	want := robot.Firewall{Status: "active", FilterIPv6: true, WhitelistHOS: true, Input: robot.ReplyRules()}

	if err := c.WriteFirewall(context.Background(), number, want); err != nil {
		t.Fatalf("WriteFirewall: %v", err)
	}
	got := f.Firewall(number)
	if wantOut := append(slices.Clone(robottest.InternalRules), allowAll); !slices.Equal(got.Output, wantOut) {
		t.Errorf("the output rules read %v, want Robot's two internal rules and the client's allow-all once: %v", got.Output, wantOut)
	}
	if !slices.Equal(got.Input, want.Input) || !got.WhitelistHOS || got.Status != "active" {
		t.Errorf("the firewall reads %+v, want the input rules written and active", got)
	}
}

func TestOpenRescueWindow_AdmitsSSHOnlyFromTheInstallerAndRestoreBringsBackTheRules(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	worker := robot.Firewall{Status: "active", FilterIPv6: true, WhitelistHOS: true, Input: robot.ReplyRules()}
	if err := c.WriteFirewall(ctx, number, worker); err != nil {
		t.Fatal(err)
	}
	installer := netip.MustParseAddr("203.0.113.9")

	restore, err := c.OpenRescueWindow(ctx, number, installer)
	if err != nil {
		t.Fatalf("OpenRescueWindow: %v", err)
	}
	open := f.Firewall(number)
	ssh := slices.DeleteFunc(slices.Clone(open.Input), func(r robot.Rule) bool { return slices.Contains(worker.Input, r) })
	if want := []robot.Rule{{Name: "rescue ssh", IPVersion: "ipv4", SrcIP: "203.0.113.9/32", DstIP: "65.108.234.220/32", Protocol: "tcp", DstPort: "22", Action: "accept"}}; !slices.Equal(ssh, want) {
		t.Errorf("the window added %v, want only %v", ssh, want)
	}
	if err := restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := f.Firewall(number); !slices.Equal(got.Input, worker.Input) || got.Status != "active" {
		t.Errorf("after restore the input rules read %v, want the worker's %v", got.Input, worker.Input)
	}
}

func TestOpenRescueWindow_RestoresTheRulesWhenTheWindowNeverReadsApplied(t *testing.T) {
	f, c := newFake(t)
	worker := robot.Firewall{Status: "active", FilterIPv6: true, WhitelistHOS: true, Input: robot.ReplyRules()}
	if err := c.WriteFirewall(context.Background(), number, worker); err != nil {
		t.Fatal(err)
	}
	f.SettleReads = 100
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, err := c.OpenRescueWindow(ctx, number, netip.MustParseAddr("203.0.113.9")); err == nil {
		t.Fatal("OpenRescueWindow succeeded on a window Robot never read applied")
	}
	if got := f.Firewall(number); !slices.Equal(got.Input, worker.Input) {
		t.Errorf("after the failed window the input rules read %v, want the worker's %v", got.Input, worker.Input)
	}
}

func TestRelease_ResetsAServerItNamedAndLeavesAnotherSubscriptionsServerAlone(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	if _, err := c.Adopt(ctx, number, name); err != nil {
		t.Fatal(err)
	}
	key, _ := robot.NewRescueKey()
	if _, err := c.BootRescue(ctx, number, name+"-install", key); err != nil {
		t.Fatal(err)
	}
	f.EditServer(number, func(_ *robottest.Server, s *robottest.State) { s.RescueActive = true })

	if err := c.Release(ctx, number, "mitstack-sub_other"); err != nil || f.State(number).Name != name {
		t.Fatalf("Release under another name = %v and left the name %q, want nothing changed", err, f.State(number).Name)
	}
	if err := c.Release(ctx, number, name); err != nil {
		t.Fatalf("Release: %v", err)
	}
	got := f.State(number)
	if got.Name != "" || got.RescueActive || len(f.Keys()) != 0 {
		t.Errorf("after Release the server is named %q with rescue active %v and keys %v, want no name, no rescue, no key", got.Name, got.RescueActive, f.Keys())
	}
	if fw := f.Firewall(number); len(fw.Input) != 1 || fw.Input[0].Action != "accept" || fw.Input[0].SrcIP != "" {
		t.Errorf("after Release the input rules read %v, want one that admits everything", fw.Input)
	}
}

func TestClient_KeepsTheLoginOutOfEveryError(t *testing.T) {
	f, _ := newFake(t)
	wrong := robot.NewAt(f.URL, robottest.User, "a-wrong-password-0002")

	_, err := wrong.GetServer(context.Background(), number)

	if robot.ErrorCode(err) != "UNAUTHORIZED" || strings.Contains(err.Error(), "a-wrong-password-0002") {
		t.Errorf("GetServer with a wrong login = %v, want UNAUTHORIZED without the password", err)
	}
}

func TestName_KeepsTheWholeSubscriptionIDSoNoOtherSubscriptionMatchesIt(t *testing.T) {
	for id, want := range map[string]string{
		"sub_0123456789ab": "mitstack-sub-0123456789ab",
		"SUB_0123456789AB": "mitstack-sub-0123456789ab",
		"sub_01.23 45":     "mitstack-sub-01-23-45",
	} {
		if got := robot.Name(id); got != want {
			t.Errorf("Name(%q) = %q, want %q", id, got, want)
		}
	}
}

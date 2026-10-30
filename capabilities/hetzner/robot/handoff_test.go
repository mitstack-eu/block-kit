package robot_test

// The install hand-off against the Robot fake, a rescue session that admits only the key Robot booted it with, and a Cloud.

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/hetzner/robot"
	"github.com/mitstack-eu/block-kit/capabilities/hetzner/robot/robottest"
)

const (
	handOffSecret = "a cluster key only the short-lived server holds"
	handOffPin    = "sha256//qHFy7mJk1n0Jt8Yb2Yc8Q2hX3o1oQ8m1Vb1z3C4sY5E="
	firstSerial   = "S675NF0WC35363"
	hardwareRead  = "efi yes\nmac 9c:6b:00:55:d8:f5\ndisk nvme0n1 S675NF0WC35475 1000215216\ndisk nvme1n1 " + firstSerial + " 1000215216\n"
)

var shortLivedAddress = netip.MustParseAddr("10.0.1.3")

type fakeCloud struct {
	created []robot.ShortLivedServer
	deleted []int64
}

func (c *fakeCloud) CreateServer(_ context.Context, s robot.ShortLivedServer) (int64, error) {
	c.created = append(c.created, s)
	return int64(len(c.created)), nil
}

func (c *fakeCloud) ServerAddress(context.Context, int64) (netip.Addr, error) {
	return shortLivedAddress, nil
}

func (c *fakeCloud) DeleteServer(_ context.Context, id int64) error {
	c.deleted = append(c.deleted, id)
	return nil
}

// fakeRescue answers read with hardware and write with writeErr, as the block's rescue script would.
type fakeRescue struct {
	hardware string
	writeErr error
	runs     [][]string
	closed   bool
}

func (r *fakeRescue) Run(_ context.Context, args ...string) (string, error) {
	r.runs = append(r.runs, args)
	if args[0] == "read" {
		return r.hardware, nil
	}
	return "", r.writeErr
}

func (r *fakeRescue) Close() error {
	r.closed = true
	return nil
}

type handOffHarness struct {
	robot     *robottest.Fake
	cloud     *fakeCloud
	rescue    *fakeRescue
	installer robot.Installer
	target    robot.Target
	dialed    []string
	seen      []robot.Hardware
	vlans     []int
}

func newHandOff(t *testing.T, dc string) *handOffHarness {
	t.Helper()
	f, c := newFake(t, func(s *robottest.Server) { s.DC = dc })
	h := &handOffHarness{robot: f, cloud: &fakeCloud{}, rescue: &fakeRescue{hardware: hardwareRead}}
	h.installer = robot.Installer{
		Robot: c, Cloud: h.cloud, From: netip.MustParseAddr("198.51.100.7"),
		Dial: func(_ context.Context, addr string, key robot.RescueKey, s robot.RescueSession) (robot.RescueShell, error) {
			h.dialed = append(h.dialed, addr)
			state := f.State(s.Server)
			if state.Booted != "rescue" || !slices.Contains(state.RescueKeys, key.AuthorizedKey()) {
				return nil, errors.New("the rescue system does not admit this key")
			}
			return h.rescue, nil
		},
	}
	h.target = robot.Target{
		Server: number, Name: name, VSwitch: "mitstack-shop", Address: netip.MustParsePrefix("10.0.1.2/24"),
		Firewall: robot.Firewall{Status: "active", FilterIPv6: true, WhitelistHOS: true, Input: append(robot.ReplyRules(),
			robot.Rule{Name: "gateway to apid", IPVersion: "ipv4", SrcIP: "10.0.0.2/32", Protocol: "tcp", DstPort: "50000", Action: "accept"})},
	}
	return h
}

func (h *handOffHarness) handOff(hw robot.Hardware, vlan int, location string) (robot.HandOff, error) {
	h.seen, h.vlans = append(h.seen, hw), append(h.vlans, vlan)
	return robot.HandOff{
		Server: robot.ShortLivedServer{Name: name + "-image", Location: location, Image: "docker-ce", UserData: "#cloud-config\n# " + handOffSecret},
		Port:   8443, Pin: handOffPin,
	}, nil
}

func (h *handOffHarness) install(t *testing.T) error {
	t.Helper()
	return h.installer.Install(context.Background(), h.target, h.handOff)
}

func ruleNames(rules []robot.Rule) []string {
	var names []string
	for _, r := range rules {
		names = append(names, r.Name)
	}
	return names
}

// assertCleanedUp checks the install left no rescue key and no rescue SSH rule, and reset the server into its disk.
func assertCleanedUp(t *testing.T, h *handOffHarness) {
	t.Helper()
	if keys := h.robot.Keys(); len(keys) != 0 {
		t.Errorf("Robot still holds the keys %v, want the one-time rescue key deleted", keys)
	}
	if got := ruleNames(h.robot.Firewall(number).Input); slices.Contains(got, "rescue ssh") {
		t.Errorf("the firewall keeps the rules %v, want the rescue SSH rule removed", got)
	}
	if booted := h.robot.State(number).Booted; booted != "disk" {
		t.Errorf("the server booted %q after the install, want disk", booted)
	}
}

func TestInstall_HandsOffThroughTheShortLivedServerInEachLocation(t *testing.T) {
	for _, tc := range []struct{ location, dc string }{{"fsn1", "FSN1-DC14"}, {"nbg1", "NBG1-DC3"}, {"hel1", "HEL1-DC1"}} {
		t.Run(tc.location, func(t *testing.T) {
			h := newHandOff(t, tc.dc)

			if err := h.install(t); err != nil {
				t.Fatalf("Install: %v", err)
			}

			if got := h.robot.State(number).Name; got != name {
				t.Errorf("the server is named %q, want %q", got, name)
			}
			if vs := h.robot.VSwitches(); len(vs) != 1 || vs[0].VLAN != robot.FirstVLAN || len(vs[0].Servers) != 1 || vs[0].Servers[0].Number != number {
				t.Errorf("the vSwitches are %+v, want one at VLAN %d holding server %d", vs, robot.FirstVLAN, number)
			}
			if resets := h.robot.FirewallsAtReset(number); len(resets) != 2 || !slices.Contains(ruleNames(resets[0].Input), "rescue ssh") {
				t.Errorf("the firewalls at reset are %+v, want two resets, the rescue boot's admitting the rescue SSH rule", resets)
			}
			if got, want := ruleNames(h.robot.Firewall(number).Input), ruleNames(h.target.Firewall.Input); !slices.Equal(got, want) {
				t.Errorf("the firewall after the install holds %v, want the target's %v", got, want)
			}
			if want := []string{"65.108.234.220:22"}; !slices.Equal(h.dialed, want) {
				t.Errorf("the rescue session dialled %v, want %v", h.dialed, want)
			}
			wantHW := robot.Hardware{EFI: true, MAC: net.HardwareAddr{0x9c, 0x6b, 0x00, 0x55, 0xd8, 0xf5}, Disk: firstSerial}
			if len(h.seen) != 1 || h.seen[0].EFI != wantHW.EFI || h.seen[0].MAC.String() != wantHW.MAC.String() || h.seen[0].Disk != wantHW.Disk || h.vlans[0] != robot.FirstVLAN {
				t.Errorf("the hand-off was asked for with %+v on VLANs %v, want %+v on VLAN %d", h.seen, h.vlans, wantHW, robot.FirstVLAN)
			}
			wantRuns := [][]string{{"read"}, {"write", "4000", "10.0.1.2/24", "10.0.1.1", "10.0.1.3", "8443", handOffPin, firstSerial}}
			if !slices.EqualFunc(h.rescue.runs, wantRuns, slices.Equal[[]string]) {
				t.Errorf("the rescue session ran %q, want %q", h.rescue.runs, wantRuns)
			}
			if len(h.cloud.created) != 1 || h.cloud.created[0].Location != tc.location || h.cloud.created[0].Name != name+"-image" {
				t.Errorf("the Cloud created %+v, want one short-lived server in %s", h.cloud.created, tc.location)
			}
			if !slices.Equal(h.cloud.deleted, []int64{1}) || !h.rescue.closed {
				t.Errorf("the Cloud deleted %v and the session closed=%v, want server 1 deleted and the session closed", h.cloud.deleted, h.rescue.closed)
			}
			assertCleanedUp(t, h)
		})
	}
}

func TestInstall_SendsNoSecretOverTheRescueSession(t *testing.T) {
	h := newHandOff(t, "FSN1-DC14")

	if err := h.install(t); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if len(h.rescue.runs) == 0 {
		t.Fatal("the rescue session ran nothing, so the check below proves nothing")
	}
	for _, run := range h.rescue.runs {
		for _, secret := range []string{handOffSecret, robottest.Password, h.cloud.created[0].UserData} {
			if strings.Contains(strings.Join(run, " "), secret) {
				t.Errorf("the rescue session ran %q, which carries the secret %q", run, secret)
			}
		}
	}
}

func TestInstall_RefusesALocationOutsideTheThreeAndChangesNothing(t *testing.T) {
	h := newHandOff(t, "ASH-DC1")

	err := h.install(t)

	if refusedClass(err) != "location-unsupported" || !strings.Contains(err.Error(), "fsn1, nbg1 or hel1") {
		t.Fatalf("Install = %v, want the location-unsupported refusal naming fsn1, nbg1 or hel1", err)
	}
	if writes := h.robot.Writes(); len(writes) != 0 || len(h.cloud.created) != 0 || len(h.dialed) != 0 {
		t.Errorf("the refused install wrote %v to Robot, created %v and dialled %v, want nothing", writes, h.cloud.created, h.dialed)
	}
}

func TestInstall_DeletesTheShortLivedServerWhenTheHandOffFails(t *testing.T) {
	for _, tc := range []struct{ name, writeErr string }{
		{"it presents another key", "the image server presented a TLS key other than the pinned one"},
		{"it never answers", "the image server at 10.0.1.3 gave no answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHandOff(t, "NBG1-DC3")
			h.rescue.writeErr = errors.New(tc.writeErr)

			err := h.install(t)

			if err == nil || !strings.Contains(err.Error(), tc.writeErr) {
				t.Fatalf("Install = %v, want the rescue script's %q", err, tc.writeErr)
			}
			if !slices.Equal(h.cloud.deleted, []int64{1}) {
				t.Errorf("the Cloud deleted %v, want the short-lived server 1 deleted", h.cloud.deleted)
			}
			assertCleanedUp(t, h)
		})
	}
}

func TestInstall_StopsBeforeAnyServerWhenTheRescueSessionIsRefused(t *testing.T) {
	h := newHandOff(t, "HEL1-DC1")
	refused := errors.New("the rescue system of server 3041839 presented host key SHA256:x, none of the host keys Robot named")
	h.installer.Dial = func(context.Context, string, robot.RescueKey, robot.RescueSession) (robot.RescueShell, error) {
		return nil, refused
	}

	err := h.install(t)

	if !errors.Is(err, refused) {
		t.Fatalf("Install = %v, want the dial's refusal", err)
	}
	if len(h.cloud.created) != 0 || len(h.rescue.runs) != 0 {
		t.Errorf("the refused session created %v and ran %q, want neither", h.cloud.created, h.rescue.runs)
	}
	assertCleanedUp(t, h)
}

func TestInstall_RefusesToSendAPinOrAddressMatchingNoPattern(t *testing.T) {
	for _, tc := range []struct {
		name, address string
		edit          func(*robot.HandOff)
	}{
		{"a pin with a space", "", func(o *robot.HandOff) { o.Pin = "sha256//x; reboot" }},
		{"a port out of range", "", func(o *robot.HandOff) { o.Port = 70000 }},
		{"an IPv6 address", "fd00::2/64", func(*robot.HandOff) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHandOff(t, "FSN1-DC14")
			if tc.address != "" {
				h.target.Address = netip.MustParsePrefix(tc.address)
			}
			handOff := func(hw robot.Hardware, vlan int, location string) (robot.HandOff, error) {
				o, err := h.handOff(hw, vlan, location)
				tc.edit(&o)
				return o, err
			}

			err := h.installer.Install(context.Background(), h.target, handOff)

			if err == nil || !strings.Contains(err.Error(), "match no pattern") {
				t.Fatalf("Install = %v, want a refusal to send arguments that match no pattern", err)
			}
			if len(h.rescue.runs) != 1 || len(h.cloud.deleted) != len(h.cloud.created) {
				t.Errorf("the session ran %q and the Cloud created %v and deleted %v, want read alone and every server deleted", h.rescue.runs, h.cloud.created, h.cloud.deleted)
			}
			assertCleanedUp(t, h)
		})
	}
}

func TestInstall_ReadsTheHardwareTheRescueSystemReports(t *testing.T) {
	for _, tc := range []struct{ name, read, wantErr, wantClass string }{
		{"a disk without a serial pattern is skipped", "efi no\nmac 9c:6b:00:55:d8:f5\ndisk sda bad/serial 1\ndisk sdb S1 1\n", "", ""},
		{"no MAC", "efi yes\ndisk sda S1 1\n", "named no MAC", ""},
		{"no disk", "efi yes\nmac 9c:6b:00:55:d8:f5\n", "", "no-disk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHandOff(t, "HEL1-DC1")
			h.rescue.hardware = tc.read

			err := h.install(t)

			switch {
			case tc.wantClass != "":
				if refusedClass(err) != tc.wantClass {
					t.Fatalf("Install = %v, want the %s refusal", err, tc.wantClass)
				}
			case tc.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Install = %v, want an error naming %q", err, tc.wantErr)
				}
			default:
				if err != nil || len(h.seen) != 1 || h.seen[0].EFI || h.seen[0].Disk != "S1" {
					t.Fatalf("Install = %v with hardware %+v, want a BIOS boot onto disk S1 handed to the caller", err, h.seen)
				}
			}
			if tc.wantErr != "" || tc.wantClass != "" {
				if len(h.cloud.created) != 0 {
					t.Errorf("the Cloud created %v, want no short-lived server for hardware it cannot write", h.cloud.created)
				}
				assertCleanedUp(t, h)
			}
		})
	}
}

func TestInstall_ReturnsTheCallersRefusalAndCreatesNoServer(t *testing.T) {
	h := newHandOff(t, "FSN1-DC14")
	notUEFI := errors.New("the server did not boot by UEFI")

	err := h.installer.Install(context.Background(), h.target, func(robot.Hardware, int, string) (robot.HandOff, error) { return robot.HandOff{}, notUEFI })

	if !errors.Is(err, notUEFI) {
		t.Fatalf("Install = %v, want the caller's refusal", err)
	}
	if len(h.cloud.created) != 0 {
		t.Errorf("the Cloud created %v, want nothing", h.cloud.created)
	}
	assertCleanedUp(t, h)
}

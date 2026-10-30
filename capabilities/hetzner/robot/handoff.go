package robot

// Robot names no host key for the rescue system, so the session runs only the block's fixed rescue script with
// arguments matched against patterns, and carries no secret. The secret reaches the server over the vSwitch from a
// short-lived Cloud server whose TLS key the rescue script checks against a pin.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mitstack-eu/block-kit/handler"
)

// Locations are where a Robot server's vSwitch reaches a Cloud network.
var Locations = []string{"fsn1", "nbg1", "hel1"}

const handOffCleanupTimeout = 10 * time.Minute

var (
	serialRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	pinRe    = regexp.MustCompile(`^sha256//[A-Za-z0-9+/]{43}=$`)
)

// ShortLivedServer serves the hand-off on the vSwitch. Its UserData holds the secret, so it is deleted after use.
type ShortLivedServer struct {
	Name, Location, Image, UserData string
}

// Cloud places the short-lived server in the Cloud network whose subnet is coupled to the vSwitch.
type Cloud interface {
	CreateServer(ctx context.Context, s ShortLivedServer) (int64, error)
	ServerAddress(ctx context.Context, id int64) (netip.Addr, error)
	DeleteServer(ctx context.Context, id int64) error
}

// RescueShell runs the block's fixed rescue script on the rescue system with args, and returns its output.
type RescueShell interface {
	Run(ctx context.Context, args ...string) (string, error)
	Close() error
}

// Hardware is what the rescue script's read reports; Disk is the serial that sorts first, since kernel names change between boots.
type Hardware struct {
	EFI  bool
	MAC  net.HardwareAddr
	Disk string
}

// HandOff is the short-lived server a caller renders for the hardware read, the port it serves on and its TLS key's pin.
type HandOff struct {
	Server ShortLivedServer
	Port   int
	Pin    string
}

type Target struct {
	Server  int64
	Name    string
	VSwitch string
	// Address lies on the vSwitch subnet, whose first address is Hetzner's vSwitch gateway.
	Address netip.Prefix
	// Firewall is the server's firewall once the install ends.
	Firewall Firewall
}

type Installer struct {
	Robot *Client
	Cloud Cloud
	// Dial opens a session on the rescue system at addr that admits key, accepting only a host key s names.
	Dial func(ctx context.Context, addr string, key RescueKey, s RescueSession) (RescueShell, error)
	// From is the public IPv4 address the rescue session leaves from, the one address the firewall admits SSH from.
	From          netip.Addr
	RescueAddress func(ip string) string
}

// Install runs once from adoption to the reset that boots the written disk, which takes longer than a subscribe
// waits, so a block runs it in the background. It refuses a server outside Locations before any write.
func (i Installer) Install(ctx context.Context, t Target, handOff func(hw Hardware, vlan int, location string) (HandOff, error)) (err error) {
	server, err := i.Robot.GetServer(ctx, t.Server)
	if err != nil && ErrorCode(err) != "SERVER_NOT_FOUND" {
		return fmt.Errorf("reading dedicated server %d: %w", t.Server, err)
	}
	if err == nil && !slices.Contains(Locations, server.Location()) {
		return handler.Refuse("location-unsupported", "dedicated server %d is in %s, and a Robot server is installed only in fsn1, nbg1 or hel1, where its vSwitch reaches a Cloud network, so nothing was changed",
			t.Server, server.DC)
	}
	if server, err = i.Robot.Adopt(ctx, t.Server, t.Name); err != nil {
		return err
	}
	vs, err := i.Robot.Couple(ctx, t.VSwitch, t.Server)
	if err != nil {
		return fmt.Errorf("coupling server %d to vSwitch %s: %w", t.Server, t.VSwitch, err)
	}
	if err := i.Robot.WriteFirewall(ctx, t.Server, t.Firewall); err != nil {
		return fmt.Errorf("writing the firewall of server %d: %w", t.Server, err)
	}
	restore, err := i.Robot.OpenRescueWindow(ctx, t.Server, i.From)
	if err != nil {
		return err
	}
	job := &install{Installer: i, target: t, server: server, vlan: vs.VLAN, handOff: handOff}
	defer func() { err = errors.Join(err, job.cleanUp(ctx, restore)) }()
	return job.run(ctx)
}

type install struct {
	Installer
	target   Target
	server   Server
	vlan     int
	handOff  func(Hardware, int, string) (HandOff, error)
	session  *RescueSession
	serverID int64
}

func (j *install) run(ctx context.Context) error {
	keyName := j.target.Name + "-rescue"
	// An install that died before its cleanup leaves its key behind.
	if err := j.Robot.DeleteKeys(ctx, keyName); err != nil {
		return err
	}
	key, err := NewRescueKey()
	if err != nil {
		return err
	}
	session, err := j.Robot.BootRescue(ctx, j.target.Server, keyName, key)
	if session.Fingerprint != "" {
		j.session = &session
	}
	if err != nil {
		return err
	}
	rescue, err := j.Dial(ctx, j.rescueAddress(), key, session)
	if err != nil {
		return err
	}
	defer rescue.Close()
	hw, err := readHardware(ctx, rescue)
	if err != nil {
		return err
	}
	offer, err := j.handOff(hw, j.vlan, j.server.Location())
	if err != nil {
		return err
	}
	if j.serverID, err = j.Cloud.CreateServer(ctx, offer.Server); err != nil {
		return fmt.Errorf("creating the short-lived server of server %d: %w", j.target.Server, err)
	}
	addr, err := j.Cloud.ServerAddress(ctx, j.serverID)
	if err != nil {
		return fmt.Errorf("reading the address of short-lived server %d: %w", j.serverID, err)
	}
	if err := write(ctx, rescue, j.vlan, j.target.Address, addr, offer, hw.Disk); err != nil {
		return fmt.Errorf("handing off to server %d: %w", j.target.Server, err)
	}
	return nil
}

// cleanUp runs after every run, handed off or failed: it deletes the short-lived server and the rescue key, restores
// the firewall and, once the rescue boot began, resets the server so it boots its disk.
func (j *install) cleanUp(ctx context.Context, restore func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handOffCleanupTimeout)
	defer cancel()
	var errs []error
	if j.serverID != 0 {
		errs = append(errs, j.Cloud.DeleteServer(ctx, j.serverID))
	}
	if j.session != nil {
		errs = append(errs, j.Robot.LeaveRescue(ctx, *j.session))
	}
	errs = append(errs, restore(ctx))
	if j.session != nil {
		errs = append(errs, j.Robot.Reset(ctx, j.target.Server))
	}
	return errors.Join(errs...)
}

func (j *install) rescueAddress() string {
	if j.RescueAddress != nil {
		return j.RescueAddress(j.server.IP)
	}
	return net.JoinHostPort(j.server.IP, "22")
}

func readHardware(ctx context.Context, rescue RescueShell) (Hardware, error) {
	out, err := rescue.Run(ctx, "read")
	if err != nil {
		return Hardware{}, err
	}
	var hw Hardware
	var serials []string
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		switch {
		case len(fields) == 2 && fields[0] == "efi":
			hw.EFI = fields[1] == "yes"
		case len(fields) == 2 && fields[0] == "mac":
			if hw.MAC, err = net.ParseMAC(fields[1]); err != nil {
				return hw, fmt.Errorf("the rescue system named the MAC %q: %w", fields[1], err)
			}
		case len(fields) == 4 && fields[0] == "disk" && serialRe.MatchString(fields[2]):
			serials = append(serials, fields[2])
		}
	}
	slices.Sort(serials)
	switch {
	case hw.MAC == nil:
		return hw, errors.New("the rescue system named no MAC for the card its default route leaves by")
	case len(serials) == 0:
		return hw, handler.Refuse("no-disk", "the rescue system found no NVMe or SATA disk with a serial number, and the install chooses its disk only by serial")
	}
	hw.Disk = serials[0]
	return hw, nil
}

func write(ctx context.Context, rescue RescueShell, vlan int, address netip.Prefix, server netip.Addr, offer HandOff, serial string) error {
	gateway := address.Masked().Addr().Next()
	if !serialRe.MatchString(serial) || !pinRe.MatchString(offer.Pin) || offer.Port < 1 || offer.Port > 65535 || !server.Is4() || !address.Addr().Is4() {
		return fmt.Errorf("refusing to send the rescue system the serial %q, the pin %q, the port %d or the addresses %s and %s, which match no pattern",
			serial, offer.Pin, offer.Port, server, address)
	}
	_, err := rescue.Run(ctx, "write", strconv.Itoa(vlan), address.String(), gateway.String(), server.String(), strconv.Itoa(offer.Port), offer.Pin, serial)
	return err
}

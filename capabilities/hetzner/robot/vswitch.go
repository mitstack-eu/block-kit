package robot

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/mitstack-eu/block-kit/handler"
)

// FirstVLAN and LastVLAN bound the VLAN ids Robot gives a vSwitch.
const (
	FirstVLAN = 4000
	LastVLAN  = 4091
)

type vswitchAnswer struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	VLAN      int    `json:"vlan"`
	Cancelled bool   `json:"cancelled"`
	Server    []struct {
		Number int64  `json:"server_number"`
		Status string `json:"status"`
	} `json:"server"`
	CloudNetwork []struct {
		ID int64 `json:"id"`
	} `json:"cloud_network"`
}

func (a vswitchAnswer) vswitch() VSwitch {
	v := VSwitch{ID: a.ID, Name: a.Name, VLAN: a.VLAN, Cancelled: a.Cancelled}
	for _, s := range a.Server {
		v.Servers = append(v.Servers, VSwitchServer{Number: s.Number, Status: s.Status})
	}
	for _, n := range a.CloudNetwork {
		v.CloudNetworks = append(v.CloudNetworks, n.ID)
	}
	return v
}

func (c *Client) ListVSwitches(ctx context.Context) ([]VSwitch, error) {
	var answer []vswitchAnswer
	if err := c.do(ctx, http.MethodGet, "/vswitch", nil, &answer); err != nil {
		return nil, err
	}
	out := make([]VSwitch, 0, len(answer))
	for _, a := range answer {
		out = append(out, a.vswitch())
	}
	return out, nil
}

func (c *Client) GetVSwitch(ctx context.Context, id int64) (VSwitch, error) {
	var answer vswitchAnswer
	err := c.do(ctx, http.MethodGet, numbered("/vswitch/", id), nil, &answer)
	return answer.vswitch(), err
}

func (c *Client) CreateVSwitch(ctx context.Context, name string, vlan int) (VSwitch, error) {
	var answer vswitchAnswer
	err := c.do(ctx, http.MethodPost, "/vswitch", url.Values{"name": {name}, "vlan": {strconv.Itoa(vlan)}}, &answer)
	return answer.vswitch(), err
}

func (c *Client) AddVSwitchServer(ctx context.Context, id, number int64) error {
	return c.do(ctx, http.MethodPost, numbered("/vswitch/", id)+"/server", url.Values{"server[]": {strconv.FormatInt(number, 10)}}, nil)
}

func (c *Client) RemoveVSwitchServer(ctx context.Context, id, number int64) error {
	return c.do(ctx, http.MethodDelete, numbered("/vswitch/", id)+"/server", url.Values{"server[]": {strconv.FormatInt(number, 10)}}, nil)
}

// CancelVSwitch cancels the vSwitch at once, which deletes it, and treats one already cancelled as done.
func (c *Client) CancelVSwitch(ctx context.Context, id int64) error {
	err := c.do(ctx, http.MethodDelete, numbered("/vswitch/", id), url.Values{"cancellation_date": {"now"}}, nil)
	if ErrorCode(err) == "CONFLICT" {
		return nil
	}
	return err
}

// Couple finds the one vSwitch named name, or creates it on the lowest free VLAN, adds the server and waits until
// Robot reads the server ready on it.
func (c *Client) Couple(ctx context.Context, name string, number int64) (VSwitch, error) {
	all, err := c.ListVSwitches(ctx)
	if err != nil {
		return VSwitch{}, fmt.Errorf("listing vSwitches: %w", err)
	}
	named := slices.DeleteFunc(slices.Clone(all), func(v VSwitch) bool { return v.Name != name || v.Cancelled })
	var vs VSwitch
	switch len(named) {
	case 0:
		vlan, ok := freeVLAN(all)
		if !ok {
			return vs, handler.Refuse("vlan-exhausted", "every VLAN from %d to %d is taken by a vSwitch in this Robot account, so no vSwitch could be created", FirstVLAN, LastVLAN)
		}
		if vs, err = c.CreateVSwitch(ctx, name, vlan); err != nil {
			return vs, fmt.Errorf("creating vSwitch %s: %w", name, err)
		}
	case 1:
		vs = named[0]
	default:
		return vs, handler.Refuse("ambiguous-vswitch", "%d vSwitches are named %s, so nothing was changed; cancel the extra ones in Robot and retry", len(named), name)
	}
	added := false
	err = c.wait(ctx, fmt.Sprintf("server %d to be ready on vSwitch %d", number, vs.ID), func() (bool, error) {
		if vs, err = c.GetVSwitch(ctx, vs.ID); err != nil {
			return false, fmt.Errorf("reading vSwitch %d: %w", vs.ID, err)
		}
		i := slices.IndexFunc(vs.Servers, func(s VSwitchServer) bool { return s.Number == number })
		if i < 0 && !added {
			added = true
			return false, c.AddVSwitchServer(ctx, vs.ID, number)
		}
		return i >= 0 && vs.Servers[i].Status == "ready", nil
	})
	return vs, err
}

// freeVLAN counts a cancelled vSwitch's VLAN as taken, since Robot may hold it until the cancellation completes.
func freeVLAN(all []VSwitch) (int, bool) {
	for vlan := FirstVLAN; vlan <= LastVLAN; vlan++ {
		if !slices.ContainsFunc(all, func(v VSwitch) bool { return v.VLAN == vlan }) {
			return vlan, true
		}
	}
	return 0, false
}

// Uncouple takes the server off the vSwitch and waits until Robot no longer lists it, since Robot refuses to cancel
// a vSwitch it is still updating.
func (c *Client) Uncouple(ctx context.Context, id, number int64) error {
	removed := false
	return c.wait(ctx, fmt.Sprintf("server %d to leave vSwitch %d", number, id), func() (bool, error) {
		vs, err := c.GetVSwitch(ctx, id)
		if err != nil {
			return false, fmt.Errorf("reading vSwitch %d: %w", id, err)
		}
		if !slices.ContainsFunc(vs.Servers, func(s VSwitchServer) bool { return s.Number == number }) {
			return true, nil
		}
		if !removed {
			removed = true
			return false, c.RemoveVSwitchServer(ctx, id, number)
		}
		return false, nil
	})
}

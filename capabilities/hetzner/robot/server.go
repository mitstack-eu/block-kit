package robot

import (
	"context"
	"fmt"
	"strings"

	"github.com/mitstack-eu/block-kit/handler"
)

// NamePrefix starts every name a block gives a server, so a server another subscription adopted is recognised.
const NamePrefix = "mitstack-"

// Adopt names a ready server name, refusing one that is cancelled, taken, absent or without a firewall before any write.
func (c *Client) Adopt(ctx context.Context, number int64, name string) (Server, error) {
	if !strings.HasPrefix(name, NamePrefix) {
		return Server{}, fmt.Errorf("the name %q does not begin with %s", name, NamePrefix)
	}
	ctx = afresh(ctx)
	server, err := c.GetServer(ctx, number)
	switch {
	case ErrorCode(err) == "SERVER_NOT_FOUND":
		return server, handler.Refuse("server-not-found", "Robot holds no server %d in this account, so there is nothing to adopt", number)
	case err != nil:
		return server, fmt.Errorf("reading dedicated server %d: %w", number, err)
	case server.Cancelled:
		return server, handler.Refuse("server-cancelled", "dedicated server %d is cancelled, so no block installs over it", number)
	case server.Name != name && strings.HasPrefix(server.Name, NamePrefix):
		return server, handler.Refuse("server-taken", "dedicated server %d is named %s, the name of another mitstack subscription, so nothing was changed", number, server.Name)
	case server.Status != "ready":
		return server, fmt.Errorf("dedicated server %d is %q in Robot, not ready yet", number, server.Status)
	}
	if _, err := c.GetFirewall(ctx, number); ErrorCode(err) == "FIREWALL_NOT_AVAILABLE" {
		return server, handler.Refuse("firewall-unavailable", "Robot offers no firewall for server %d, and a block adopts a server only behind one, so nothing was changed", number)
	} else if err != nil {
		return server, fmt.Errorf("reading the firewall of dedicated server %d: %w", number, err)
	}
	if server.Name == name {
		return server, nil
	}
	if err := c.SetServerName(ctx, number, name); err != nil {
		return server, fmt.Errorf("naming dedicated server %d %s: %w", number, name, err)
	}
	// Of two subscriptions naming one server at once only the last write's owner may go on.
	if server, err = c.GetServer(ctx, number); err != nil {
		return server, fmt.Errorf("reading dedicated server %d: %w", number, err)
	}
	if server.Name != name {
		return server, handler.Refuse("server-taken", "dedicated server %d is named %s right after this subscription named it, so another subscription adopted it", number, server.Name)
	}
	return server, nil
}

// Release clears the rescue, the keys named after name and the name, and admits all traffic, since Robot refuses to
// disable a firewall that holds its internal rules; it leaves a server under any other name alone.
func (c *Client) Release(ctx context.Context, number int64, name string) error {
	ctx = afresh(ctx)
	server, err := c.GetServer(ctx, number)
	if ErrorCode(err) == "SERVER_NOT_FOUND" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading dedicated server %d: %w", number, err)
	}
	if server.Name != name {
		return nil
	}
	if err := c.DeactivateRescue(ctx, number); err != nil {
		return fmt.Errorf("clearing the rescue activation of dedicated server %d: %w", number, err)
	}
	if err := c.DeleteKeys(ctx, name+"-"); err != nil {
		return err
	}
	released := Firewall{Status: "active", WhitelistHOS: true, Input: []Rule{{Name: "allow all", Action: "accept"}}}
	if err := c.WriteFirewall(ctx, number, released); err != nil {
		return fmt.Errorf("resetting the firewall of dedicated server %d: %w", number, err)
	}
	if err := c.SetServerName(ctx, number, ""); err != nil {
		return fmt.Errorf("clearing the name of dedicated server %d: %w", number, err)
	}
	return nil
}

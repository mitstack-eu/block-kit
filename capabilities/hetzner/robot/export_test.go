package robot

import (
	"context"
	"time"
)

func (c *Client) SetFirewallSettleTimeout(d time.Duration) { c.firewallSettleTimeout = d }

func (c *Client) Wait(ctx context.Context, what string, done func() (bool, error)) error {
	return c.wait(ctx, what, done)
}

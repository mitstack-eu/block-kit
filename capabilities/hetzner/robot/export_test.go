package robot

import "time"

func (c *Client) SetFirewallSettleTimeout(d time.Duration) { c.firewallSettleTimeout = d }

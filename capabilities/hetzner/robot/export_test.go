package robot

import "time"

// SetFirewallSettleTimeout shortens the bound on each firewall settle wait, a seam only this package's tests reach.
func (c *Client) SetFirewallSettleTimeout(d time.Duration) { c.firewallSettleTimeout = d }

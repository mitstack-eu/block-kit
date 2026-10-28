package robot

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

const (
	statusInProcess = "in process"
	ephemeralPorts  = "32768-65535"
	// internalRuleName names the two mail-port output rules Robot keeps ahead of whatever a write sends.
	internalRuleName = "Block mail ports"
)

// ReplyRules admit the replies to a server's own TCP, DNS and NTP traffic, which the stateless firewall otherwise drops.
func ReplyRules() []Rule {
	return []Rule{
		{Name: "tcp replies v4", IPVersion: "ipv4", Protocol: "tcp", DstPort: ephemeralPorts, TCPFlags: "ack", Action: "accept"},
		{Name: "tcp replies v6", IPVersion: "ipv6", Protocol: "tcp", DstPort: ephemeralPorts, TCPFlags: "ack", Action: "accept"},
		{Name: "dns replies v4", IPVersion: "ipv4", Protocol: "udp", SrcPort: "53", DstPort: ephemeralPorts, Action: "accept"},
		{Name: "ntp replies v4", IPVersion: "ipv4", Protocol: "udp", SrcPort: "123", DstPort: ephemeralPorts, Action: "accept"},
	}
}

type ruleAnswer struct {
	Name      *string `json:"name"`
	IPVersion *string `json:"ip_version"`
	SrcIP     *string `json:"src_ip"`
	DstIP     *string `json:"dst_ip"`
	Protocol  *string `json:"protocol"`
	SrcPort   *string `json:"src_port"`
	DstPort   *string `json:"dst_port"`
	TCPFlags  *string `json:"tcp_flags"`
	Action    *string `json:"action"`
}

func orEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func decodeRules(answers []ruleAnswer) []Rule {
	var out []Rule
	for _, r := range answers {
		out = append(out, Rule{
			Name: orEmpty(r.Name), IPVersion: orEmpty(r.IPVersion), SrcIP: orEmpty(r.SrcIP), DstIP: orEmpty(r.DstIP),
			Protocol: orEmpty(r.Protocol), SrcPort: orEmpty(r.SrcPort), DstPort: orEmpty(r.DstPort), TCPFlags: orEmpty(r.TCPFlags),
			Action: orEmpty(r.Action),
		})
	}
	return out
}

// GetFirewall answers an Error with code FIREWALL_NOT_AVAILABLE for a server Robot cannot firewall.
func (c *Client) GetFirewall(ctx context.Context, number int64) (Firewall, error) {
	var answer struct {
		Firewall struct {
			Status       string `json:"status"`
			FilterIPv6   bool   `json:"filter_ipv6"`
			WhitelistHOS bool   `json:"whitelist_hos"`
			Rules        struct {
				Input  []ruleAnswer `json:"input"`
				Output []ruleAnswer `json:"output"`
			} `json:"rules"`
		} `json:"firewall"`
	}
	if err := c.do(ctx, http.MethodGet, numbered("/firewall/", number), nil, &answer); err != nil {
		return Firewall{}, err
	}
	fw := answer.Firewall
	return Firewall{Status: fw.Status, FilterIPv6: fw.FilterIPv6, WhitelistHOS: fw.WhitelistHOS, Input: decodeRules(fw.Rules.Input), Output: decodeRules(fw.Rules.Output)}, nil
}

// SetFirewall sends one write of both directions, as rules[direction][i][field], the shape Robot's documentation gives.
func (c *Client) SetFirewall(ctx context.Context, number int64, fw Firewall) error {
	form := url.Values{
		"status":        {fw.Status},
		"filter_ipv6":   {strconv.FormatBool(fw.FilterIPv6)},
		"whitelist_hos": {strconv.FormatBool(fw.WhitelistHOS)},
	}
	for direction, rules := range map[string][]Rule{"input": fw.Input, "output": fw.Output} {
		for i, r := range rules {
			prefix := fmt.Sprintf("rules[%s][%d]", direction, i)
			for field, value := range map[string]string{
				"name": r.Name, "ip_version": r.IPVersion, "src_ip": r.SrcIP, "dst_ip": r.DstIP, "protocol": r.Protocol,
				"src_port": r.SrcPort, "dst_port": r.DstPort, "tcp_flags": r.TCPFlags, "action": r.Action,
			} {
				if value != "" {
					form.Set(prefix+"["+field+"]", value)
				}
			}
		}
	}
	return c.do(ctx, http.MethodPost, numbered("/firewall/", number), form, nil)
}

// WriteFirewall waits out an earlier change, writes want and waits until Robot reads it active. With no output rules
// in want, it keeps the output rules it read but Robot's internal ones, which Robot keeps itself and would otherwise
// list twice, and admits all output when none is left, as the trial server's firewall did before any write.
func (c *Client) WriteFirewall(ctx context.Context, number int64, want Firewall) error {
	current, err := c.settledFirewall(ctx, number)
	if err != nil {
		return err
	}
	if want.Output == nil {
		for _, r := range current.Output {
			if r.Name != internalRuleName && !slices.Contains(want.Output, r) {
				want.Output = append(want.Output, r)
			}
		}
		if want.Output == nil {
			want.Output = []Rule{{Name: "Allow all", Action: "accept"}}
		}
	}
	if err := c.SetFirewall(ctx, number, want); err != nil {
		return fmt.Errorf("writing the firewall of server %d: %w", number, err)
	}
	applied, err := c.settledFirewall(ctx, number)
	if err != nil {
		return err
	}
	if applied.Status != want.Status || !slices.EqualFunc(applied.Input, want.Input, func(a, b Rule) bool { return hostForm(a) == hostForm(b) }) {
		return fmt.Errorf("the firewall of server %d reads %s with input rules %v after the write, not the %s rules written", number, applied.Status, applied.Input, want.Status)
	}
	return nil
}

func (c *Client) settledFirewall(ctx context.Context, number int64) (Firewall, error) {
	var fw Firewall
	err := c.wait(ctx, fmt.Sprintf("the firewall of server %d to settle", number), func() (bool, error) {
		var err error
		if fw, err = c.GetFirewall(ctx, number); err != nil {
			return false, fmt.Errorf("reading the firewall of server %d: %w", number, err)
		}
		return fw.Status != statusInProcess, nil
	})
	return fw, err
}

// hostForm reads a host address with or without /32 alike, since Robot's documentation does not say which it reads back.
func hostForm(r Rule) Rule {
	r.SrcIP, r.DstIP = strings.TrimSuffix(r.SrcIP, "/32"), strings.TrimSuffix(r.DstIP, "/32")
	return r
}

// OpenRescueWindow adds a rule admitting SSH from installer alone to the server's address, and admits Hetzner's own
// services, which the rescue boot needed on the trial; restore writes back the firewall it found.
func (c *Client) OpenRescueWindow(ctx context.Context, number int64, installer netip.Addr) (restore func(context.Context) error, err error) {
	if !installer.Is4() {
		return nil, fmt.Errorf("the installer's address %s is no IPv4 address", installer)
	}
	server, err := c.GetServer(ctx, number)
	if err != nil {
		return nil, fmt.Errorf("reading dedicated server %d: %w", number, err)
	}
	found, err := c.settledFirewall(ctx, number)
	if err != nil {
		return nil, err
	}
	found.Output = nil
	window := found
	window.WhitelistHOS = true
	window.Input = append(slices.Clone(found.Input), Rule{
		Name: "rescue ssh", IPVersion: "ipv4", SrcIP: installer.String() + "/32", DstIP: server.IP + "/32", Protocol: "tcp", DstPort: "22", Action: "accept",
	})
	if err := c.WriteFirewall(ctx, number, window); err != nil {
		return nil, fmt.Errorf("opening port 22 for the rescue session: %w", err)
	}
	return func(ctx context.Context) error { return c.WriteFirewall(ctx, number, found) }, nil
}

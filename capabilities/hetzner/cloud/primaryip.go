package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/mitstack-eu/block-kit/handler"
)

// primaryIPsPerPage is the most the API serves on one page.
const primaryIPsPerPage = 50

// PrimaryIP is one public IPv4 of a Hetzner Cloud project, with AssigneeID 0 while no server holds it.
type PrimaryIP struct {
	ID         int64
	Name       string
	IP         string
	Location   string
	AssigneeID int64
	AutoDelete bool
}

// PrimaryIPSpec names the address a block makes. Its Labels are what the other calls find the address by, so they name the subscription.
type PrimaryIPSpec struct {
	Name, Location string
	Labels         map[string]string
}

type wirePrimaryIP struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	IP         string `json:"ip"`
	AssigneeID *int64 `json:"assignee_id"`
	AutoDelete bool   `json:"auto_delete"`
	Location   struct {
		Name string `json:"name"`
	} `json:"location"`
}

func (w wirePrimaryIP) primaryIP() PrimaryIP {
	ip := PrimaryIP{ID: w.ID, Name: w.Name, IP: w.IP, Location: w.Location.Name, AutoDelete: w.AutoDelete}
	if w.AssigneeID != nil {
		ip.AssigneeID = *w.AssigneeID
	}
	return ip
}

// selector refuses no labels, which would select every address in the project.
func selector(labels map[string]string) (string, error) {
	if len(labels) == 0 {
		return "", errors.New("no labels name the subscription's primary IPs, and an empty selector reaches every address in the project")
	}
	terms := make([]string, 0, len(labels))
	for k, v := range labels {
		terms = append(terms, k+"="+v)
	}
	slices.Sort(terms)
	return strings.Join(terms, ","), nil
}

func (c *Client) primaryIPs(ctx context.Context, labels map[string]string) ([]PrimaryIP, error) {
	sel, err := selector(labels)
	if err != nil {
		return nil, err
	}
	var ips []PrimaryIP
	for page := 1; page != 0; {
		var answer struct {
			PrimaryIPs []wirePrimaryIP `json:"primary_ips"`
			Meta       struct {
				Pagination struct {
					NextPage int `json:"next_page"`
				} `json:"pagination"`
			} `json:"meta"`
		}
		query := url.Values{"label_selector": {sel}, "page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(primaryIPsPerPage)}}
		if err := c.do(ctx, http.MethodGet, "/primary_ips", query, nil, &answer); err != nil {
			return nil, fmt.Errorf("listing primary IPs: %w", err)
		}
		for _, w := range answer.PrimaryIPs {
			ips = append(ips, w.primaryIP())
		}
		page = answer.Meta.Pagination.NextPage
	}
	return ips, nil
}

func (c *Client) keep(ctx context.Context, labels map[string]string) ([]PrimaryIP, error) {
	ips, err := c.primaryIPs(ctx, labels)
	if err != nil {
		return nil, err
	}
	for i, ip := range ips {
		if !ip.AutoDelete {
			continue
		}
		path := "/primary_ips/" + strconv.FormatInt(ip.ID, 10)
		if err := c.do(ctx, http.MethodPut, path, nil, map[string]any{"auto_delete": false}, nil); err != nil {
			return nil, fmt.Errorf("switching primary IP %d (%s) to outlive its server: %w", ip.ID, ip.Name, err)
		}
		ips[i].AutoDelete = false
	}
	return ips, nil
}

// KeepPrimaryIPs switches every address of these labels to outlive its server, so call it on every ensure, also while a server stands.
func (c *Client) KeepPrimaryIPs(ctx context.Context, labels map[string]string) error {
	_, err := c.keep(ctx, labels)
	return err
}

// PrimaryIPForNewServer returns the address a server about to be created takes: the one of these labels left unassigned in the location, or a new one when the labels hold none.
//
// It never deletes an address, since one released while DNS records name it can be drawn by another Hetzner customer. An address still assigned fails, which the engine retries, and one in another location is refused, since Hetzner assigns an address only in its own location.
func (c *Client) PrimaryIPForNewServer(ctx context.Context, spec PrimaryIPSpec) (PrimaryIP, error) {
	ips, err := c.keep(ctx, spec.Labels)
	if err != nil {
		return PrimaryIP{}, err
	}
	for _, ip := range ips {
		if ip.AssigneeID == 0 && ip.Location == spec.Location {
			return ip, nil
		}
	}
	if len(ips) == 0 {
		return c.createPrimaryIP(ctx, spec)
	}
	ip := ips[0]
	if ip.AssigneeID != 0 {
		return PrimaryIP{}, fmt.Errorf("primary IP %d (%s) is still assigned to server %d, so a new server cannot take it until that server is gone", ip.ID, ip.Name, ip.AssigneeID)
	}
	return PrimaryIP{}, handler.Refuse("primary-ip-elsewhere",
		"primary IP %d (%s) lies in %s, a server in %s cannot take it, and releasing it would free the address DNS records name; go back to %s or remove this subscription",
		ip.ID, ip.Name, ip.Location, spec.Location, ip.Location)
}

func (c *Client) createPrimaryIP(ctx context.Context, spec PrimaryIPSpec) (PrimaryIP, error) {
	var answer struct {
		PrimaryIP wirePrimaryIP `json:"primary_ip"`
	}
	body := map[string]any{"name": spec.Name, "type": "ipv4", "location": spec.Location, "assignee_type": "server", "auto_delete": false, "labels": spec.Labels}
	if err := c.do(ctx, http.MethodPost, "/primary_ips", nil, body, &answer); err != nil {
		return PrimaryIP{}, fmt.Errorf("creating primary IP %s: %w", spec.Name, err)
	}
	return answer.PrimaryIP.primaryIP(), nil
}

// DeletePrimaryIPs deletes every address of these labels, so call it once their servers are deleted, since Hetzner refuses to delete an address still assigned.
func (c *Client) DeletePrimaryIPs(ctx context.Context, labels map[string]string) error {
	ips, err := c.primaryIPs(ctx, labels)
	if err != nil {
		return err
	}
	var errs []error
	for _, ip := range ips {
		err := c.do(ctx, http.MethodDelete, "/primary_ips/"+strconv.FormatInt(ip.ID, 10), nil, nil, nil)
		if err != nil && ErrorCode(err) != "not_found" {
			errs = append(errs, fmt.Errorf("deleting primary IP %d (%s): %w", ip.ID, ip.Name, err))
		}
	}
	return errors.Join(errs...)
}

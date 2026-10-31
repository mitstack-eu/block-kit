// Package apphostname points an app's hostname in mitstack's apps zone at its addresses, beside a TXT record naming the one subscription that holds it.
package apphostname

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
)

const (
	ZoneEnv   = "MITSTACK_APPS_ZONE"
	ZoneIDEnv = "MITSTACK_APPS_ZONE_ID"
	// TTL is short, so a replaced host's new address is picked up quickly.
	TTL = 60

	Create  = "CREATE"
	Delete  = "DELETE"
	TypeA   = "A"
	TypeTXT = "TXT"

	ownerPrefix = "mitstack-subscription="
	// addressSeparator joins a record's addresses in its identity, whose values are strings.
	addressSeparator = ","
)

// ErrConflict marks a change batch Route 53 rejected because a record it creates exists, or one it deletes is gone or holds other values.
var ErrConflict = errors.New("the hostname's records changed since they were read")

// Record is one hostname and the addresses it points at, sorted.
type Record struct {
	ZoneID string
	Name   string
	Values []string
}

// Held is what the zone holds at a hostname: its A record's addresses, nil when absent, and its TXT value, "" when absent.
type Held struct {
	Addresses []string
	Owner     string
}

// Change creates or deletes one record set, which Route 53 deletes only when Values names every value it holds.
type Change struct {
	Action string
	Type   string
	Name   string
	Values []string
	TTL    int
}

// Writer reads a hostname's records and changes them in batches that apply whole or not at all.
type Writer interface {
	LookupHost(ctx context.Context, zoneID, name string) (Held, error)
	// ChangeRecords wraps ErrConflict when the zone no longer holds what the batch was planned against.
	ChangeRecords(ctx context.Context, zoneID string, changes []Change) error
}

// OwnerText is the TXT value that marks a hostname as subscription's.
func OwnerText(subscription string) string { return ownerPrefix + subscription }

// ZoneID returns the apps zone's Route 53 id, refusing when the operator is not set up for domain, the zone the block's hostnames live in.
func ZoneID(domain string) (string, error) {
	zone := strings.Trim(os.Getenv(ZoneEnv), ".")
	id := os.Getenv(ZoneIDEnv)
	switch {
	case zone == "":
		return "", handler.Refuse("apps-zone-unset", "%s is not set, so the app's hostname cannot be published", ZoneEnv)
	case zone != domain:
		return "", handler.Refuse("apps-zone-mismatch", "%s is %s, but the block's hostnames live under %s", ZoneEnv, zone, domain)
	case id == "":
		return "", handler.Refuse("apps-zone-unset", "%s is not set, so the app's hostname cannot be published", ZoneIDEnv)
	}
	return id, nil
}

func NewRecord(zoneID, name string, addresses ...string) Record {
	return Record{ZoneID: zoneID, Name: name, Values: slices.Sorted(slices.Values(addresses))}
}

// Identity holds everything Observe and Release need, so a hostname is found again with no other input.
func Identity(rec Record) map[string]string {
	return map[string]string{"zone": rec.ZoneID, "hostname": rec.Name, "addresses": strings.Join(rec.Values, addressSeparator)}
}

// FromIdentity is false when identity names no zone, hostname and addresses.
func FromIdentity(identity map[string]string) (Record, bool) {
	zone, name, addresses := identity["zone"], identity["hostname"], identity["addresses"]
	if zone == "" || name == "" || addresses == "" {
		return Record{}, false
	}
	return NewRecord(zone, name, strings.Split(addresses, addressSeparator)...), true
}

// Claim is the changes that give a subscription its hostname, read before the block writes anything else, so a taken hostname is refused with nothing written.
type Claim struct {
	w            Writer
	rec          Record
	subscription string
	changes      []Change
}

func ReadClaim(ctx context.Context, w Writer, rec Record, subscription string) (Claim, error) {
	if len(rec.Values) == 0 {
		return Claim{}, handler.Refuse("no-address", "the hostname %s has no address to point at", rec.Name)
	}
	held, err := w.LookupHost(ctx, rec.ZoneID, rec.Name)
	if err != nil {
		return Claim{}, fmt.Errorf("reading the records of %s: %w", rec.Name, err)
	}
	changes, err := planClaim(held, rec, subscription)
	if err != nil {
		return Claim{}, err
	}
	return Claim{w: w, rec: rec, subscription: subscription, changes: changes}, nil
}

// Apply lands the claim in one batch, and on a conflict reads the hostname again, since a racing subscribe may hold it now, or this subscription already does.
func (c Claim) Apply(ctx context.Context) error {
	if len(c.changes) == 0 {
		return nil
	}
	err := c.w.ChangeRecords(ctx, c.rec.ZoneID, c.changes)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrConflict) {
		return fmt.Errorf("pointing %s at %s: %w", c.rec.Name, strings.Join(c.rec.Values, ", "), err)
	}
	again, readErr := ReadClaim(ctx, c.w, c.rec, c.subscription)
	switch {
	case readErr != nil:
		return readErr
	case len(again.changes) == 0:
		return nil
	default:
		return fmt.Errorf("pointing %s at %s: %w", c.rec.Name, strings.Join(c.rec.Values, ", "), err)
	}
}

// Release deletes the records subscription holds at rec's hostname, leaving records another subscription holds and counting records already gone as deleted.
func Release(ctx context.Context, w Writer, rec Record, subscription string) error {
	held, err := w.LookupHost(ctx, rec.ZoneID, rec.Name)
	if err != nil {
		return fmt.Errorf("reading the records of %s: %w", rec.Name, err)
	}
	release := planRelease(held, rec, subscription)
	if len(release) == 0 {
		return nil
	}
	err = w.ChangeRecords(ctx, rec.ZoneID, release)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrConflict) {
		return fmt.Errorf("deleting the records of %s: %w", rec.Name, err)
	}
	if held, err = w.LookupHost(ctx, rec.ZoneID, rec.Name); err != nil {
		return fmt.Errorf("reading the records of %s: %w", rec.Name, err)
	}
	if len(planRelease(held, rec, subscription)) != 0 {
		return fmt.Errorf("deleting the records of %s: %w", rec.Name, ErrConflict)
	}
	return nil
}

// Observe reads the hostname present while its A record holds rec's addresses and its TXT names subscription or no subscription.
func Observe(ctx context.Context, w Writer, rec Record, subscription string) (contract.Observation, error) {
	held, err := w.LookupHost(ctx, rec.ZoneID, rec.Name)
	if err != nil {
		return contract.Observation{}, fmt.Errorf("reading the records of %s: %w", rec.Name, err)
	}
	if len(rec.Values) == 0 || !sameAddresses(held.Addresses, rec.Values) || !holds(held, subscription) {
		return contract.Observation{State: contract.Absent}, nil
	}
	// The type's address output is one IPv4 address, so it carries the first of the sorted set.
	return contract.Observation{State: contract.Present, Identity: Identity(rec), Outputs: map[string]string{
		"appHostname": rec.Name,
		"address":     rec.Values[0],
		"appUrl":      "https://" + rec.Name,
	}}, nil
}

func sameAddresses(a, b []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}

func holds(held Held, subscription string) bool {
	return held.Owner == "" || held.Owner == OwnerText(subscription)
}

// planClaim adopts an A record that names no subscription only when it already holds rec's addresses, so adopting it redirects no visitor.
func planClaim(held Held, rec Record, subscription string) ([]Change, error) {
	owner := OwnerText(subscription)
	change := func(action, recordType string, values ...string) Change {
		return Change{Action: action, Type: recordType, Name: rec.Name, Values: values, TTL: TTL}
	}
	atAddresses := sameAddresses(held.Addresses, rec.Values)
	switch {
	case held.Owner == owner && atAddresses:
		return nil, nil
	case held.Owner == owner && len(held.Addresses) == 0:
		return []Change{change(Create, TypeA, rec.Values...)}, nil
	case held.Owner == owner:
		return []Change{change(Delete, TypeA, held.Addresses...), change(Create, TypeA, rec.Values...)}, nil
	case held.Owner == "" && len(held.Addresses) == 0:
		return []Change{change(Create, TypeA, rec.Values...), change(Create, TypeTXT, owner)}, nil
	case held.Owner == "" && atAddresses:
		return []Change{change(Create, TypeTXT, owner)}, nil
	case held.Owner == "":
		return nil, handler.Refuse("hostname-taken", "the hostname %s points at %s, which no subscription holds, so claiming it would move its visitors",
			rec.Name, strings.Join(held.Addresses, ", "))
	default:
		return nil, handler.Refuse("hostname-taken", "the hostname %s is held by another subscription", rec.Name)
	}
}

func planRelease(held Held, rec Record, subscription string) []Change {
	change := func(recordType string, values ...string) Change {
		return Change{Action: Delete, Type: recordType, Name: rec.Name, Values: values, TTL: TTL}
	}
	switch {
	case held.Owner == OwnerText(subscription) && len(held.Addresses) != 0:
		return []Change{change(TypeA, held.Addresses...), change(TypeTXT, held.Owner)}
	case held.Owner == OwnerText(subscription):
		return []Change{change(TypeTXT, held.Owner)}
	case held.Owner == "" && sameAddresses(held.Addresses, rec.Values) && len(held.Addresses) != 0:
		return []Change{change(TypeA, held.Addresses...)}
	default:
		return nil
	}
}

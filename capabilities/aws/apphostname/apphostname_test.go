package apphostname_test

// Claims, releases and observations over the in-memory apps zone, which applies a change batch whole or not at all.

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/aws/apphostname"
	"github.com/mitstack-eu/block-kit/capabilities/aws/apphostname/apphostnametest"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
)

const (
	zoneID   = "Z0APPS"
	host     = "shop.apps.mitstack.dev"
	address  = "203.0.113.10"
	moved    = "203.0.113.200"
	sub      = "sub_shop"
	otherSub = "sub_other"
)

func record() apphostname.Record { return apphostname.NewRecord(zoneID, host, address) }

func change(action, recordType string, values ...string) apphostname.Change {
	return apphostname.Change{Action: action, Type: recordType, Name: host, Values: values, TTL: apphostname.TTL}
}

func claim(t *testing.T, zone *apphostnametest.Zone, rec apphostname.Record, subscription string) error {
	t.Helper()
	c, err := apphostname.ReadClaim(context.Background(), zone, rec, subscription)
	if err != nil {
		return err
	}
	return c.Apply(context.Background())
}

func wantHeld(t *testing.T, zone *apphostnametest.Zone, addresses []string, owner string) {
	t.Helper()
	if got := zone.Addresses(host); !slices.Equal(got, addresses) || zone.Owner(host) != owner {
		t.Errorf("the zone holds A %q and TXT %q at %s, want A %q and TXT %q", got, zone.Owner(host), host, addresses, owner)
	}
}

func wantRefused(t *testing.T, err error, class string) {
	t.Helper()
	var refused *handler.Refused
	if !errors.As(err, &refused) || refused.Class != class || !strings.Contains(refused.Detail, host) {
		t.Errorf("error = %v, want a %s refusal naming %s", err, class, host)
	}
}

func TestZoneID_RefusesAnOperatorNotSetUpForTheBlocksDomain(t *testing.T) {
	for _, tc := range []struct {
		name, zone, id, wantID, wantClass string
	}{
		{name: "set for the domain", zone: "apps.mitstack.dev", id: zoneID, wantID: zoneID},
		{name: "set with a trailing dot", zone: "apps.mitstack.dev.", id: zoneID, wantID: zoneID},
		{name: "no zone", id: zoneID, wantClass: "apps-zone-unset"},
		{name: "no zone id", zone: "apps.mitstack.dev", wantClass: "apps-zone-unset"},
		{name: "another zone", zone: "apps.example.test", id: zoneID, wantClass: "apps-zone-mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(apphostname.ZoneEnv, tc.zone)
			t.Setenv(apphostname.ZoneIDEnv, tc.id)

			id, err := apphostname.ZoneID("apps.mitstack.dev")

			var refused *handler.Refused
			switch {
			case tc.wantClass == "" && (err != nil || id != tc.wantID):
				t.Errorf("ZoneID = %q, %v; want %q", id, err, tc.wantID)
			case tc.wantClass != "" && (!errors.As(err, &refused) || refused.Class != tc.wantClass):
				t.Errorf("ZoneID error = %v, want a %s refusal", err, tc.wantClass)
			}
		})
	}
}

func TestNewRecord_SortsTheAddresses(t *testing.T) {
	rec := apphostname.NewRecord(zoneID, host, "198.51.100.9", "198.51.100.1")

	if want := []string{"198.51.100.1", "198.51.100.9"}; !slices.Equal(rec.Values, want) {
		t.Errorf("NewRecord's addresses = %q, want %q", rec.Values, want)
	}
}

func TestClaim_ChangesTheRecordsByWhatTheHostnameHolds(t *testing.T) {
	for _, tc := range []struct {
		name        string
		addresses   []string
		owner       string
		wantChanges []apphostname.Change
		wantClass   string
	}{
		{
			name:        "a hostname with no record gets its A record and the TXT naming the subscription in one batch",
			wantChanges: []apphostname.Change{change("CREATE", "A", address), change("CREATE", "TXT", apphostname.OwnerText(sub))},
		},
		{
			name:      "its own hostname at its addresses changes nothing",
			addresses: []string{address}, owner: apphostname.OwnerText(sub),
		},
		{
			name:      "its own hostname whose address moved swaps the A record in one batch",
			addresses: []string{moved}, owner: apphostname.OwnerText(sub),
			wantChanges: []apphostname.Change{change("DELETE", "A", moved), change("CREATE", "A", address)},
		},
		{
			name:        "its own TXT with the A record lost recreates the A record alone",
			owner:       apphostname.OwnerText(sub),
			wantChanges: []apphostname.Change{change("CREATE", "A", address)},
		},
		{
			name:        "an A record at its addresses that names no subscription is adopted by its TXT alone",
			addresses:   []string{address},
			wantChanges: []apphostname.Change{change("CREATE", "TXT", apphostname.OwnerText(sub))},
		},
		{
			name:      "an A record elsewhere that names no subscription is refused",
			addresses: []string{moved}, wantClass: "hostname-taken",
		},
		{
			name:      "a TXT naming another subscription is refused even at its addresses",
			addresses: []string{address}, owner: apphostname.OwnerText(otherSub), wantClass: "hostname-taken",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			zone := apphostnametest.New()
			zone.Set(host, tc.addresses, tc.owner)

			err := claim(t, zone, record(), sub)

			if tc.wantClass != "" {
				wantRefused(t, err, tc.wantClass)
				wantHeld(t, zone, tc.addresses, tc.owner)
				if n := len(zone.Batches()); n != 0 {
					t.Errorf("a refused claim applied %d change batches, want none", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
			wantHeld(t, zone, []string{address}, apphostname.OwnerText(sub))
			var got []apphostname.Change
			for _, b := range zone.Batches() {
				if b.ZoneID != zoneID {
					t.Errorf("a batch went to zone %q, want %q", b.ZoneID, zoneID)
				}
				got = append(got, b.Changes...)
			}
			if len(zone.Batches()) > 1 || !reflect.DeepEqual(got, tc.wantChanges) {
				t.Errorf("the claim applied %+v, want one batch of %+v", zone.Batches(), tc.wantChanges)
			}
		})
	}
}

func TestClaim_ReadsBeforeItWritesSoATakenHostnameIsRefusedBeforeTheBlockWritesAnythingElse(t *testing.T) {
	zone := apphostnametest.New()
	zone.Set(host, []string{moved}, apphostname.OwnerText(otherSub))

	_, err := apphostname.ReadClaim(context.Background(), zone, record(), sub)

	wantRefused(t, err, "hostname-taken")
}

func TestClaim_ASubscribeRacingAnotherSubscriptionForTheHostnameIsRefusedAndTheWinnerKeepsIt(t *testing.T) {
	zone := apphostnametest.New()
	c, err := apphostname.ReadClaim(context.Background(), zone, record(), sub)
	if err != nil {
		t.Fatalf("ReadClaim: %v", err)
	}
	zone.Set(host, []string{moved}, apphostname.OwnerText(otherSub))

	err = c.Apply(context.Background())

	wantRefused(t, err, "hostname-taken")
	wantHeld(t, zone, []string{moved}, apphostname.OwnerText(otherSub))
}

func TestClaim_PassesWhenARacingRunOfItsOwnSubscribeTookTheHostnameFirst(t *testing.T) {
	zone := apphostnametest.New()
	c, err := apphostname.ReadClaim(context.Background(), zone, record(), sub)
	if err != nil {
		t.Fatalf("ReadClaim: %v", err)
	}
	zone.Set(host, []string{address}, apphostname.OwnerText(sub))

	if err := c.Apply(context.Background()); err != nil {
		t.Fatalf("Apply = %v, want it to pass once the records name its own subscription", err)
	}
	if n := len(zone.Batches()); n != 0 {
		t.Errorf("the claim applied %d change batches beyond the racing run's, want none", n)
	}
}

func TestClaim_AFailedBatchFailsTheClaimAndChangesNoRecord(t *testing.T) {
	zone := apphostnametest.New()
	zone.BeforeChange(func(string) error { return errors.New("route53: throttled") })

	err := claim(t, zone, record(), sub)

	if err == nil || !strings.Contains(err.Error(), "throttled") || !strings.Contains(err.Error(), host) {
		t.Errorf("claim error = %v, want the throttle naming %s", err, host)
	}
	wantHeld(t, zone, nil, "")
}

func TestRelease_DeletesOnlyWhatTheSubscriptionHolds(t *testing.T) {
	for _, tc := range []struct {
		name          string
		addresses     []string
		owner         string
		wantAddresses []string
		wantOwner     string
		wantChanges   []apphostname.Change
	}{
		{
			name:      "its own A record and TXT go in one batch",
			addresses: []string{address}, owner: apphostname.OwnerText(sub),
			wantChanges: []apphostname.Change{change("DELETE", "A", address), change("DELETE", "TXT", apphostname.OwnerText(sub))},
		},
		{
			name:      "its own A record at a moved address goes too",
			addresses: []string{moved}, owner: apphostname.OwnerText(sub),
			wantChanges: []apphostname.Change{change("DELETE", "A", moved), change("DELETE", "TXT", apphostname.OwnerText(sub))},
		},
		{
			name:        "its own TXT with no A record goes alone",
			owner:       apphostname.OwnerText(sub),
			wantChanges: []apphostname.Change{change("DELETE", "TXT", apphostname.OwnerText(sub))},
		},
		{
			name:        "an A record at its addresses that names no subscription goes",
			addresses:   []string{address},
			wantChanges: []apphostname.Change{change("DELETE", "A", address)},
		},
		{
			name:      "another subscription's hostname is left as it is",
			addresses: []string{address}, owner: apphostname.OwnerText(otherSub),
			wantAddresses: []string{address}, wantOwner: apphostname.OwnerText(otherSub),
		},
		{
			name:      "an A record elsewhere that names no subscription is left as it is",
			addresses: []string{moved}, wantAddresses: []string{moved},
		},
		{name: "a hostname with no record changes nothing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			zone := apphostnametest.New()
			zone.Set(host, tc.addresses, tc.owner)

			if err := apphostname.Release(context.Background(), zone, record(), sub); err != nil {
				t.Fatalf("Release: %v", err)
			}

			wantHeld(t, zone, tc.wantAddresses, tc.wantOwner)
			var got []apphostname.Change
			for _, b := range zone.Batches() {
				got = append(got, b.Changes...)
			}
			if len(zone.Batches()) > 1 || !reflect.DeepEqual(got, tc.wantChanges) {
				t.Errorf("the release applied %+v, want one batch of %+v", zone.Batches(), tc.wantChanges)
			}
		})
	}
}

func TestRelease_SucceedsWhenTheRecordsWentAwayBetweenItsReadAndItsDelete(t *testing.T) {
	zone := apphostnametest.New()
	zone.Set(host, []string{address}, apphostname.OwnerText(sub))
	zone.BeforeChange(func(name string) error {
		zone.Set(name, nil, "")
		return nil
	})

	if err := apphostname.Release(context.Background(), zone, record(), sub); err != nil {
		t.Fatalf("Release = %v, want success once the records are gone", err)
	}
	wantHeld(t, zone, nil, "")
}

func TestRelease_FailsWhenItsRecordsChangedAndAreStillItsOwn(t *testing.T) {
	zone := apphostnametest.New()
	zone.Set(host, []string{address}, apphostname.OwnerText(sub))
	zone.BeforeChange(func(name string) error {
		zone.Set(name, []string{moved}, apphostname.OwnerText(sub))
		return nil
	})

	err := apphostname.Release(context.Background(), zone, record(), sub)

	if !errors.Is(err, apphostname.ErrConflict) || !strings.Contains(err.Error(), host) {
		t.Errorf("Release error = %v, want ErrConflict naming %s", err, host)
	}
}

func TestObserve_ReadsTheHostnamePresentOnlyWhileItPointsAtItsAddressesForItsSubscription(t *testing.T) {
	for _, tc := range []struct {
		name      string
		addresses []string
		owner     string
		want      contract.State
	}{
		{name: "its own record at its addresses", addresses: []string{address}, owner: apphostname.OwnerText(sub), want: contract.Present},
		{name: "a record at its addresses naming no subscription", addresses: []string{address}, want: contract.Present},
		{name: "its own record at a moved address", addresses: []string{moved}, owner: apphostname.OwnerText(sub), want: contract.Absent},
		{name: "another subscription's record", addresses: []string{address}, owner: apphostname.OwnerText(otherSub), want: contract.Absent},
		{name: "no record", want: contract.Absent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			zone := apphostnametest.New()
			zone.Set(host, tc.addresses, tc.owner)

			obs, err := apphostname.Observe(context.Background(), zone, record(), sub)

			if err != nil || obs.State != tc.want {
				t.Fatalf("Observe = %+v, %v; want %s", obs, err, tc.want)
			}
			if tc.want != contract.Present {
				return
			}
			wantOutputs := map[string]string{"appHostname": host, "address": address, "appUrl": "https://" + host}
			if !reflect.DeepEqual(obs.Outputs, wantOutputs) || !reflect.DeepEqual(obs.Identity, apphostname.Identity(record())) {
				t.Errorf("Observe = outputs %v, identity %v; want %v and %v", obs.Outputs, obs.Identity, wantOutputs, apphostname.Identity(record()))
			}
		})
	}
}

func TestIdentity_ReadsBackAsTheRecordItWasMadeFrom(t *testing.T) {
	rec := apphostname.NewRecord(zoneID, host, "198.51.100.9", "198.51.100.1")

	got, ok := apphostname.FromIdentity(apphostname.Identity(rec))

	if !ok || !reflect.DeepEqual(got, rec) {
		t.Errorf("FromIdentity(Identity(%+v)) = %+v, %v; want the record back", rec, got, ok)
	}
	if _, ok := apphostname.FromIdentity(map[string]string{"zone": zoneID, "hostname": host}); ok {
		t.Error("FromIdentity read a record from an identity naming no addresses")
	}
}

func TestClaimAndObserve_ARecordWithNoAddressIsRefusedAndReadAbsent(t *testing.T) {
	zone := apphostnametest.New()
	rec := apphostname.NewRecord(zoneID, host)

	_, err := apphostname.ReadClaim(context.Background(), zone, rec, sub)
	obs, obsErr := apphostname.Observe(context.Background(), zone, rec, sub)

	wantRefused(t, err, "no-address")
	if obsErr != nil || obs.State != contract.Absent {
		t.Errorf("Observe of a record with no address = %+v, %v; want absent", obs, obsErr)
	}
}

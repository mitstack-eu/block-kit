package apphostnametest_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/aws/apphostname"
	"github.com/mitstack-eu/block-kit/capabilities/aws/apphostname/apphostnametest"
)

const (
	host    = "shop.apps.mitstack.dev"
	address = "203.0.113.10"
)

func change(action, recordType string, values ...string) apphostname.Change {
	return apphostname.Change{Action: action, Type: recordType, Name: host, Values: values, TTL: apphostname.TTL}
}

func TestZone_RejectsAWholeBatchWhenOneOfItsChangesDoesNotApply(t *testing.T) {
	for _, tc := range []struct {
		name  string
		batch []apphostname.Change
	}{
		{"a create of a record that exists", []apphostname.Change{change("CREATE", "TXT", "mitstack-subscription=sub_shop"), change("CREATE", "A", address)}},
		{"a delete of a record that is gone", []apphostname.Change{change("DELETE", "A", address), change("DELETE", "TXT", "mitstack-subscription=sub_shop")}},
		{"a delete whose values are not the record's", []apphostname.Change{change("DELETE", "A", "198.51.100.9")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			zone := apphostnametest.New()
			zone.Set(host, []string{address}, "")

			err := zone.ChangeRecords(context.Background(), "Z0APPS", tc.batch)

			if !errors.Is(err, apphostname.ErrConflict) {
				t.Errorf("ChangeRecords error = %v, want ErrConflict", err)
			}
			if !slices.Equal(zone.Addresses(host), []string{address}) || zone.Owner(host) != "" || len(zone.Batches()) != 0 {
				t.Errorf("the zone holds A %q, TXT %q and %d batches after a rejected batch, want it unchanged", zone.Addresses(host), zone.Owner(host), len(zone.Batches()))
			}
		})
	}
}

func TestZone_AnErrorFromBeforeChangeRejectsTheBatch(t *testing.T) {
	zone := apphostnametest.New()
	fault := errors.New("injected")
	zone.BeforeChange(func(name string) error {
		if name != host {
			t.Errorf("BeforeChange got %q, want the batch's hostname %s", name, host)
		}
		return fault
	})

	err := zone.ChangeRecords(context.Background(), "Z0APPS", []apphostname.Change{change("CREATE", "A", address)})

	if !errors.Is(err, fault) || zone.Addresses(host) != nil || len(zone.Batches()) != 0 {
		t.Errorf("ChangeRecords = %v with A %q and %d batches, want the injected error and nothing changed", err, zone.Addresses(host), len(zone.Batches()))
	}
}

func TestZone_SetWithNoAddressesAndNoOwnerLeavesTheHostnameWithoutRecords(t *testing.T) {
	zone := apphostnametest.New()
	zone.Set(host, []string{address}, apphostname.OwnerText("sub_shop"))

	zone.Set(host, nil, "")

	held, _ := zone.LookupHost(context.Background(), "Z0APPS", host)
	if held.Addresses != nil || held.Owner != "" {
		t.Errorf("LookupHost = %+v after clearing, want no records", held)
	}
}

func TestZone_HeldByNamesEachHostnameWhoseTXTNamesTheSubscription(t *testing.T) {
	zone := apphostnametest.New()
	zone.Set(host, []string{address}, apphostname.OwnerText("sub_shop"))
	zone.Set("cart.apps.mitstack.dev", []string{address}, apphostname.OwnerText("sub_other"))
	zone.Set("blog.apps.mitstack.dev", []string{address}, "")

	if got := zone.HeldBy("sub_shop"); !slices.Equal(got, []string{host}) {
		t.Errorf("HeldBy(sub_shop) = %q, want only %s", got, host)
	}
}

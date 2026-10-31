package apphostname

// Route53 against a stand-in aws on PATH, which logs each call and answers from files the test writes.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const fakeAWS = `#!/bin/sh
printf '%s\n' "$*" >>"$FAKE_AWS/calls"
if [ "$2" = change-resource-record-sets ]; then
  cat "$FAKE_AWS/change.out"
  exit "$(cat "$FAKE_AWS/change.code")"
fi
while [ "$#" -gt 0 ]; do
  if [ "$1" = --start-record-type ]; then
    cat "$FAKE_AWS/$2.json" || { echo "no listing for $2" >&2; exit 255; }
  fi
  shift
done
`

type awsStandIn struct{ dir string }

func newAWSStandIn(t *testing.T) awsStandIn {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "aws"), []byte(fakeAWS), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_AWS", dir)
	return awsStandIn{dir: dir}
}

func (a awsStandIn) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(a.dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (a awsStandIn) calls(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(a.dir, "calls"))
	if err != nil {
		t.Fatalf("the stand-in aws logged no call: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func TestRoute53_LookupHostReadsTheNamesAddressesAndOwnerFromTheZone(t *testing.T) {
	aws := newAWSStandIn(t)
	aws.write(t, "A.json", `{"ResourceRecordSets":[{"Name":"shop.apps.mitstack.dev.","Type":"A","ResourceRecords":[{"Value":"203.0.113.10"}]}]}`)
	aws.write(t, "TXT.json", `{"ResourceRecordSets":[{"Name":"shop.apps.mitstack.dev.","Type":"TXT","ResourceRecords":[{"Value":"\"mitstack-subscription=sub_shop\""}]}]}`)

	held, err := Route53{}.LookupHost(context.Background(), "Z0APPS", testHost)

	if err != nil {
		t.Fatalf("LookupHost: %v", err)
	}
	if want := (Held{Addresses: []string{"203.0.113.10"}, Owner: OwnerText("sub_shop")}); !reflect.DeepEqual(held, want) {
		t.Errorf("LookupHost = %+v, want %+v", held, want)
	}
	for _, call := range aws.calls(t) {
		if !strings.Contains(call, "--hosted-zone-id Z0APPS --start-record-name "+testHost) {
			t.Errorf("the lookup ran aws %s, want it to name the zone Z0APPS and the hostname %s", call, testHost)
		}
	}
}

func TestRoute53_LookupHostNamesTheCLIsErrorWhenTheListingFails(t *testing.T) {
	newAWSStandIn(t)

	_, err := Route53{}.LookupHost(context.Background(), "Z0APPS", testHost)

	if err == nil || !strings.Contains(err.Error(), "no listing for A") || !strings.Contains(err.Error(), testHost) {
		t.Errorf("LookupHost error = %v, want the CLI's error naming %s", err, testHost)
	}
}

func TestRoute53_ChangeRecordsSendsOneBatchToTheZone(t *testing.T) {
	aws := newAWSStandIn(t)
	aws.write(t, "change.out", "")
	aws.write(t, "change.code", "0")
	changes := []Change{{Action: Create, Type: TypeA, Name: testHost, Values: []string{"203.0.113.10"}, TTL: TTL}}

	if err := (Route53{}).ChangeRecords(context.Background(), "Z0APPS", changes); err != nil {
		t.Fatalf("ChangeRecords: %v", err)
	}

	calls := aws.calls(t)
	want, _ := changeBatchJSON(changes)
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "route53 change-resource-record-sets --hosted-zone-id Z0APPS --change-batch ") {
		t.Fatalf("ChangeRecords ran aws %q, want one change-resource-record-sets call to Z0APPS", calls)
	}
	var sent, wantBatch any
	_ = json.Unmarshal([]byte(strings.SplitN(calls[0], "--change-batch ", 2)[1]), &sent)
	_ = json.Unmarshal(want, &wantBatch)
	if !reflect.DeepEqual(sent, wantBatch) {
		t.Errorf("the batch sent was %s, want %s", calls[0], want)
	}
}

func TestRoute53_ChangeRecordsWrapsErrConflictOnlyForABatchRejectedOverTheRecordsItFound(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		conflict     bool
	}{
		{name: "a record that exists", conflict: true,
			output: "An error occurred (InvalidChangeBatch) when calling the ChangeResourceRecordSets operation: [Tried to create resource record set [name='shop.apps.mitstack.dev.', type='A'] but it already exists]"},
		{name: "an access denial",
			output: "An error occurred (AccessDenied) when calling the ChangeResourceRecordSets operation: User is not authorized"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aws := newAWSStandIn(t)
			aws.write(t, "change.out", tc.output)
			aws.write(t, "change.code", "254")
			changes := []Change{{Action: Create, Type: TypeA, Name: testHost, Values: []string{"203.0.113.10"}, TTL: TTL}}

			err := Route53{}.ChangeRecords(context.Background(), "Z0APPS", changes)

			if err == nil || errors.Is(err, ErrConflict) != tc.conflict || !strings.Contains(err.Error(), tc.output) {
				t.Errorf("ChangeRecords error = %v, want ErrConflict %v and the CLI's output", err, tc.conflict)
			}
		})
	}
}

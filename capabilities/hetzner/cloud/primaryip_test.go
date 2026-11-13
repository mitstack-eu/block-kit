package cloud_test

// A server block's address outlives its server: it is made and switched to auto_delete off, a new server takes it, and only removing the block deletes it.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/hetzner/cloud"
	"github.com/mitstack-eu/block-kit/capabilities/hetzner/cloud/cloudtest"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
)

const (
	testToken = "test-cloud-token"
	bound     = 1 << 20
)

var (
	ctx    = context.Background()
	ours   = map[string]string{"block": "b", "subscription": "sub_a"}
	theirs = map[string]string{"block": "b", "subscription": "sub_b"}
)

func newFake(t *testing.T) (*cloudtest.Fake, *cloud.Client) {
	t.Helper()
	f := cloudtest.New(t, testToken)
	return f, cloud.NewAt(f.URL(), testToken)
}

func spec(location string) cloud.PrimaryIPSpec {
	return cloud.PrimaryIPSpec{Name: "host-a", Location: location, Labels: ours}
}

func autoDelete(t *testing.T, f *cloudtest.Fake, id int64) bool {
	t.Helper()
	ip, ok := f.Object(cloudtest.PrimaryIPs, id)
	if !ok {
		t.Fatalf("address %d is gone", id)
	}
	return ip["auto_delete"] == true
}

func refusing(method, code string, status int) func(http.ResponseWriter, *http.Request) bool {
	return func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == method {
			cloudtest.Refuse(w, status, code, code+" message")
		}
		return r.Method == method
	}
}

func TestKeepPrimaryIPs_SwitchesOnlyAnAddressOfTheseLabelsThatWouldDieWithItsServer(t *testing.T) {
	f, c := newFake(t)
	dying := f.AddPrimaryIP(cloudtest.PrimaryIP{Location: "nbg1", AssigneeID: 7, AutoDelete: true, Labels: ours})
	f.AddPrimaryIP(cloudtest.PrimaryIP{Location: "nbg1", AssigneeID: 8, Labels: ours})
	f.AddPrimaryIP(cloudtest.PrimaryIP{Location: "nbg1", AssigneeID: 9, AutoDelete: true, Labels: theirs})

	if err := c.KeepPrimaryIPs(ctx, ours); err != nil {
		t.Fatalf("KeepPrimaryIPs = %v", err)
	}

	if want := []string{fmt.Sprintf("PUT /v1/primary_ips/%d", dying)}; !slices.Equal(f.Writes(), want) || autoDelete(t, f, dying) {
		t.Errorf("writes = %q, address %d dies with its server = %t; want only %q", f.Writes(), dying, autoDelete(t, f, dying), want)
	}
}

func TestKeepPrimaryIPs_FailsNamingTheAddressHetznerWouldNotSwitch(t *testing.T) {
	f, c := newFake(t)
	id := f.AddPrimaryIP(cloudtest.PrimaryIP{Location: "nbg1", AutoDelete: true, Labels: ours})
	f.Hook = refusing(http.MethodPut, "conflict", http.StatusConflict)

	err := c.KeepPrimaryIPs(ctx, ours)

	if err == nil || !strings.Contains(err.Error(), strconv.FormatInt(id, 10)) || cloud.ErrorCode(err) != "conflict" {
		t.Fatalf("KeepPrimaryIPs = %v, want an error naming address %d and carrying Hetzner's conflict", err, id)
	}
}

func TestPrimaryIPForNewServer_CreatesOneThatOutlivesItsServerWhenTheLabelsHoldNone(t *testing.T) {
	f, c := newFake(t)
	f.AddPrimaryIP(cloudtest.PrimaryIP{Location: "nbg1", Labels: theirs})

	ip, err := c.PrimaryIPForNewServer(ctx, spec("nbg1"))

	created := f.Created(cloudtest.PrimaryIPs)
	if err != nil || len(created) != 1 {
		t.Fatalf("PrimaryIPForNewServer = %v, created %v; want one address", err, created)
	}
	body := created[0]
	if body["auto_delete"] != false || body["type"] != "ipv4" || body["location"] != "nbg1" || body["name"] != "host-a" || body["assignee_id"] != nil || body["assignee_type"] != nil {
		t.Errorf("created %v, want an ipv4 named host-a in nbg1 with auto_delete false and no assignee, as Hetzner documents an unassigned address", body)
	}
	if got := body["labels"].(map[string]any); got["subscription"] != "sub_a" {
		t.Errorf("created with labels %v, want the subscription's", got)
	}
	if ip.ID == 0 || ip.IP == "" || ip.Location != "nbg1" || ip.AssigneeID != 0 || ip.AutoDelete {
		t.Errorf("answered %+v, want the created address", ip)
	}
}

func TestPrimaryIPForNewServer_TakesTheAddressAServerLeftAndSwitchesIt(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprintf("auto_delete %t", on), func(t *testing.T) {
			f, c := newFake(t)
			id := f.AddPrimaryIP(cloudtest.PrimaryIP{Location: "nbg1", AutoDelete: on, Labels: ours})

			ip, err := c.PrimaryIPForNewServer(ctx, spec("nbg1"))

			if err != nil || ip.ID != id || ip.AutoDelete || autoDelete(t, f, id) || len(f.Created(cloudtest.PrimaryIPs)) != 0 {
				t.Fatalf("PrimaryIPForNewServer = %+v, %v, created %v; want address %d lasting and nothing created", ip, err, f.Created(cloudtest.PrimaryIPs), id)
			}
		})
	}
}

func TestPrimaryIPForNewServer_LeavesAnAddressItCannotTake(t *testing.T) {
	cases := []struct {
		name, class string
		ip          cloudtest.PrimaryIP
		says        []string
	}{
		{"still assigned, which a retry can clear", "", cloudtest.PrimaryIP{Location: "nbg1", AssigneeID: 77, Labels: ours}, []string{"77"}},
		{"in another location", "primary-ip-elsewhere", cloudtest.PrimaryIP{Location: "fsn1", Labels: ours}, []string{"fsn1", "nbg1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, c := newFake(t)
			id := f.AddPrimaryIP(tc.ip)

			_, err := c.PrimaryIPForNewServer(ctx, spec("nbg1"))

			var refused *handler.Refused
			if err == nil || errors.As(err, &refused) != (tc.class != "") || (refused != nil && refused.Class != tc.class) {
				t.Fatalf("PrimaryIPForNewServer = %v, want class %q", err, tc.class)
			}
			for _, said := range append(tc.says, strconv.FormatInt(id, 10)) {
				if !strings.Contains(err.Error(), said) {
					t.Errorf("error %q, want it to say %s", err, said)
				}
			}
			if writes := f.Writes(); len(writes) != 0 {
				t.Errorf("writes = %q, want none", writes)
			}
		})
	}
}

func TestDeletePrimaryIPs_DeletesEveryAddressOfTheseLabelsAcrossPagesAndNoOther(t *testing.T) {
	f, c := newFake(t)
	f.PerPage = 1
	f.AddPrimaryIP(cloudtest.PrimaryIP{Location: "nbg1", Labels: ours})
	f.AddPrimaryIP(cloudtest.PrimaryIP{Location: "fsn1", AutoDelete: true, Labels: ours})
	other := f.AddPrimaryIP(cloudtest.PrimaryIP{Location: "nbg1", Labels: theirs})

	if err := c.DeletePrimaryIPs(ctx, ours); err != nil {
		t.Fatalf("DeletePrimaryIPs = %v", err)
	}

	if held := f.Held("")[cloudtest.PrimaryIPs]; !slices.Equal(held, []int64{other}) {
		t.Errorf("after delete Hetzner holds %v, want only the other subscription's %d", held, other)
	}
}

func TestDeletePrimaryIPs_FailsNamingAnAddressStillAssigned(t *testing.T) {
	f, c := newFake(t)
	id := f.AddPrimaryIP(cloudtest.PrimaryIP{Location: "nbg1", AssigneeID: 77, Labels: ours})

	err := c.DeletePrimaryIPs(ctx, ours)

	if err == nil || !strings.Contains(err.Error(), strconv.FormatInt(id, 10)) || cloud.ErrorCode(err) != "must_be_unassigned" {
		t.Fatalf("DeletePrimaryIPs = %v, want an error naming address %d and carrying must_be_unassigned", err, id)
	}
}

func TestDeletePrimaryIPs_TakesAnAddressHetznerDeletedMeanwhileAsDeleted(t *testing.T) {
	f, c := newFake(t)
	f.AddPrimaryIP(cloudtest.PrimaryIP{Location: "nbg1", Labels: ours})
	f.Hook = refusing(http.MethodDelete, "not_found", http.StatusNotFound)

	if err := c.DeletePrimaryIPs(ctx, ours); err != nil {
		t.Fatalf("DeletePrimaryIPs = %v, want nil", err)
	}
}

func TestEveryCall_RefusesNoLabelsBeforeAnyCall(t *testing.T) {
	f, c := newFake(t)
	f.AddPrimaryIP(cloudtest.PrimaryIP{Location: "nbg1", Labels: ours})
	f.Hook = func(http.ResponseWriter, *http.Request) bool {
		t.Error("a call with no labels reached Hetzner, where it would select every address in the project")
		return false
	}
	_, forServer := c.PrimaryIPForNewServer(ctx, cloud.PrimaryIPSpec{Name: "host-a", Location: "nbg1"})
	for name, err := range map[string]error{"KeepPrimaryIPs": c.KeepPrimaryIPs(ctx, nil), "DeletePrimaryIPs": c.DeletePrimaryIPs(ctx, nil), "PrimaryIPForNewServer": forServer} {
		if err == nil {
			t.Errorf("%s with no labels = nil, want an error", name)
		}
	}
}

func TestClient_FailsNamingTheCallAndTheBoundOnAnAnswerPastIt(t *testing.T) {
	f, c := newFake(t)
	f.Hook = func(w http.ResponseWriter, _ *http.Request) bool {
		_, _ = w.Write([]byte(strings.Repeat(" ", bound+1)))
		return true
	}

	err := c.KeepPrimaryIPs(ctx, ours)

	if err == nil || !strings.Contains(err.Error(), "GET /primary_ips") || !strings.Contains(err.Error(), strconv.Itoa(bound)) {
		t.Fatalf("KeepPrimaryIPs = %v, want an error naming the call and the bound", err)
	}
}

func TestClient_ReadsHetznersErrorAnswerWithoutSayingTheToken(t *testing.T) {
	f := cloudtest.New(t, testToken)

	err := cloud.NewAt(f.URL(), "another-token").KeepPrimaryIPs(ctx, ours)

	if cloud.ErrorCode(err) != "unauthorized" || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "another-token") {
		t.Fatalf("KeepPrimaryIPs = %v, want Hetzner's 401 unauthorized without the token", err)
	}
}

func TestPrimaryIPCalls_AContextMarkedReadOnlyWritesNothing(t *testing.T) {
	f, c := newFake(t)
	f.AddPrimaryIP(cloudtest.PrimaryIP{Location: "fsn1", AutoDelete: true, Labels: ours})
	marked := contract.WithReadOnly(ctx)

	for call, err := range map[string]error{
		"KeepPrimaryIPs":   c.KeepPrimaryIPs(marked, ours),
		"DeletePrimaryIPs": c.DeletePrimaryIPs(marked, ours),
	} {
		if !errors.Is(err, contract.ErrReadOnly) {
			t.Errorf("%s for a read-only context = %v, want an error wrapping contract.ErrReadOnly", call, err)
		}
	}

	if writes := f.Writes(); len(writes) != 0 {
		t.Errorf("a read-only context sent %v, want no write", writes)
	}
}

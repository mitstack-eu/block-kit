package cloud_test

// A server block's address outlives its server: it is made and switched to AutoDelete false, a new server takes it, and only removing the block deletes it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/hetzner/cloud"
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

type address struct {
	id, assignee int64
	location     string
	autoDelete   bool
	labels       map[string]string
}

func (a *address) wire() map[string]any {
	wire := map[string]any{"id": a.id, "name": "ip", "ip": "203.0.113.9", "auto_delete": a.autoDelete, "location": map[string]any{"name": a.location}}
	if a.assignee != 0 {
		wire["assignee_id"] = a.assignee
	}
	return wire
}

// fakeAPI keeps primary IPs as Hetzner documents them, serving one a page and refusing to delete one still assigned.
type fakeAPI struct {
	ips     []*address
	created []map[string]any
	writes  []string
	answer  func(w http.ResponseWriter, r *http.Request) bool
}

func newFake(t *testing.T, ips ...address) (*fakeAPI, *cloud.Client) {
	t.Helper()
	f := &fakeAPI{}
	for i := range ips {
		ips[i].id = int64(101 + i)
		f.ips = append(f.ips, &ips[i])
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, cloud.NewAt(srv.URL, testToken)
}

func refuse(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": code + " message"}})
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	if f.answer != nil && f.answer(w, r) {
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	rest, byID := strings.CutPrefix(r.URL.Path, "/primary_ips/")
	id, _ := strconv.ParseInt(rest, 10, 64)
	switch {
	case r.Header.Get("Authorization") != "Bearer "+testToken:
		refuse(w, http.StatusUnauthorized, "unauthorized")
	case r.Method == http.MethodGet:
		f.list(w, r)
	case r.Method == http.MethodPost:
		f.writes = append(f.writes, "POST")
		f.created = append(f.created, body)
		a := &address{id: 900, location: body["location"].(string), autoDelete: body["auto_delete"].(bool)}
		f.ips = append(f.ips, a)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"primary_ip": a.wire(), "action": nil})
	case byID && r.Method == http.MethodPut:
		f.writes = append(f.writes, fmt.Sprintf("PUT %d", id))
		f.ips[slices.IndexFunc(f.ips, func(a *address) bool { return a.id == id })].autoDelete = body["auto_delete"].(bool)
		_ = json.NewEncoder(w).Encode(map[string]any{})
	case byID && r.Method == http.MethodDelete:
		f.writes = append(f.writes, fmt.Sprintf("DELETE %d", id))
		i := slices.IndexFunc(f.ips, func(a *address) bool { return a.id == id })
		if f.ips[i].assignee != 0 {
			refuse(w, http.StatusUnprocessableEntity, "must_be_unassigned")
			return
		}
		f.ips = slices.Delete(f.ips, i, i+1)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (f *fakeAPI) list(w http.ResponseWriter, r *http.Request) {
	matching := slices.DeleteFunc(slices.Clone(f.ips), func(a *address) bool {
		for _, term := range strings.Split(r.URL.Query().Get("label_selector"), ",") {
			if k, v, _ := strings.Cut(term, "="); a.labels[k] != v {
				return true
			}
		}
		return false
	})
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	answer := []map[string]any{}
	var next any
	if page <= len(matching) && page >= 1 {
		answer = append(answer, matching[page-1].wire())
	}
	if page < len(matching) {
		next = page + 1
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"primary_ips": answer, "meta": map[string]any{"pagination": map[string]any{"next_page": next}}})
}

func spec(location string) cloud.PrimaryIPSpec {
	return cloud.PrimaryIPSpec{Name: "host-a", Location: location, Labels: ours}
}

func TestKeepPrimaryIPs_SwitchesOnlyAnAddressOfTheseLabelsThatWouldDieWithItsServer(t *testing.T) {
	f, c := newFake(t, address{assignee: 7, autoDelete: true, labels: ours}, address{assignee: 8, labels: ours}, address{assignee: 9, autoDelete: true, labels: theirs})

	if err := c.KeepPrimaryIPs(ctx, ours); err != nil {
		t.Fatalf("KeepPrimaryIPs = %v", err)
	}

	if want := []string{"PUT 101"}; !slices.Equal(f.writes, want) || f.ips[0].autoDelete {
		t.Errorf("writes = %q, address 101 dies with its server = %t; want only %q", f.writes, f.ips[0].autoDelete, want)
	}
}

func TestKeepPrimaryIPs_FailsNamingTheAddressHetznerWouldNotSwitch(t *testing.T) {
	f, c := newFake(t, address{autoDelete: true, labels: ours})
	f.answer = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPut {
			refuse(w, http.StatusConflict, "conflict")
		}
		return r.Method == http.MethodPut
	}

	err := c.KeepPrimaryIPs(ctx, ours)

	if err == nil || !strings.Contains(err.Error(), "101") || cloud.ErrorCode(err) != "conflict" {
		t.Fatalf("KeepPrimaryIPs = %v, want an error naming address 101 and carrying Hetzner's conflict", err)
	}
}

func TestPrimaryIPForNewServer_CreatesOneThatOutlivesItsServerWhenTheLabelsHoldNone(t *testing.T) {
	f, c := newFake(t, address{location: "nbg1", labels: theirs})

	ip, err := c.PrimaryIPForNewServer(ctx, spec("nbg1"))

	if err != nil || len(f.created) != 1 {
		t.Fatalf("PrimaryIPForNewServer = %v, created %v; want one address", err, f.created)
	}
	body := f.created[0]
	if body["auto_delete"] != false || body["type"] != "ipv4" || body["location"] != "nbg1" || body["name"] != "host-a" || body["assignee_id"] != nil {
		t.Errorf("created %v, want an unassigned ipv4 named host-a in nbg1 with auto_delete false", body)
	}
	if got := body["labels"].(map[string]any); got["subscription"] != "sub_a" {
		t.Errorf("created with labels %v, want the subscription's", got)
	}
	if ip.ID != 900 || ip.IP != "203.0.113.9" || ip.Location != "nbg1" || ip.AutoDelete {
		t.Errorf("answered %+v, want the created address", ip)
	}
}

func TestPrimaryIPForNewServer_TakesTheAddressAServerLeftAndSwitchesIt(t *testing.T) {
	for _, autoDelete := range []bool{false, true} {
		t.Run(fmt.Sprintf("auto_delete %t", autoDelete), func(t *testing.T) {
			f, c := newFake(t, address{location: "nbg1", autoDelete: autoDelete, labels: ours})

			ip, err := c.PrimaryIPForNewServer(ctx, spec("nbg1"))

			if err != nil || ip.ID != 101 || ip.AutoDelete || f.ips[0].autoDelete || len(f.created) != 0 {
				t.Fatalf("PrimaryIPForNewServer = %+v, %v, created %v; want address 101 lasting and nothing created", ip, err, f.created)
			}
		})
	}
}

func TestPrimaryIPForNewServer_LeavesAnAddressItCannotTake(t *testing.T) {
	cases := []struct {
		name, class string
		ip          address
		says        []string
	}{
		{"still assigned, which a retry can clear", "", address{location: "nbg1", assignee: 77, labels: ours}, []string{"101", "77"}},
		{"in another location", "primary-ip-elsewhere", address{location: "fsn1", labels: ours}, []string{"101", "fsn1", "nbg1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, c := newFake(t, tc.ip)

			_, err := c.PrimaryIPForNewServer(ctx, spec("nbg1"))

			var refused *handler.Refused
			if err == nil || errors.As(err, &refused) != (tc.class != "") || (refused != nil && refused.Class != tc.class) {
				t.Fatalf("PrimaryIPForNewServer = %v, want class %q", err, tc.class)
			}
			for _, said := range tc.says {
				if !strings.Contains(err.Error(), said) {
					t.Errorf("error %q, want it to say %s", err, said)
				}
			}
			if len(f.writes) != 0 {
				t.Errorf("writes = %q, want none", f.writes)
			}
		})
	}
}

func TestDeletePrimaryIPs_DeletesEveryAddressOfTheseLabelsAcrossPagesAndNoOther(t *testing.T) {
	f, c := newFake(t, address{labels: ours}, address{autoDelete: true, labels: ours}, address{labels: theirs})

	if err := c.DeletePrimaryIPs(ctx, ours); err != nil {
		t.Fatalf("DeletePrimaryIPs = %v", err)
	}

	if len(f.ips) != 1 || f.ips[0].id != 103 {
		t.Errorf("after delete Hetzner holds %d addresses, want only the other subscription's", len(f.ips))
	}
}

func TestDeletePrimaryIPs_FailsNamingAnAddressStillAssigned(t *testing.T) {
	_, c := newFake(t, address{assignee: 77, labels: ours})

	err := c.DeletePrimaryIPs(ctx, ours)

	if err == nil || !strings.Contains(err.Error(), "101") || cloud.ErrorCode(err) != "must_be_unassigned" {
		t.Fatalf("DeletePrimaryIPs = %v, want an error naming address 101 and carrying must_be_unassigned", err)
	}
}

func TestDeletePrimaryIPs_TakesAnAddressHetznerDeletedMeanwhileAsDeleted(t *testing.T) {
	f, c := newFake(t, address{labels: ours})
	f.answer = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodDelete {
			refuse(w, http.StatusNotFound, "not_found")
		}
		return r.Method == http.MethodDelete
	}

	if err := c.DeletePrimaryIPs(ctx, ours); err != nil {
		t.Fatalf("DeletePrimaryIPs = %v, want nil", err)
	}
}

func TestEveryCall_RefusesNoLabelsBeforeAnyCall(t *testing.T) {
	f, c := newFake(t, address{labels: ours})
	f.answer = func(http.ResponseWriter, *http.Request) bool {
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
	f.answer = func(w http.ResponseWriter, _ *http.Request) bool {
		_, _ = w.Write([]byte(strings.Repeat(" ", bound+1)))
		return true
	}

	err := c.KeepPrimaryIPs(ctx, ours)

	if err == nil || !strings.Contains(err.Error(), "GET /primary_ips") || !strings.Contains(err.Error(), strconv.Itoa(bound)) {
		t.Fatalf("KeepPrimaryIPs = %v, want an error naming the call and the bound", err)
	}
}

func TestClient_ReadsHetznersErrorAnswerWithoutSayingTheToken(t *testing.T) {
	f, c := newFake(t)
	f.answer = func(w http.ResponseWriter, _ *http.Request) bool {
		refuse(w, http.StatusUnauthorized, "unauthorized")
		return true
	}

	err := c.KeepPrimaryIPs(ctx, ours)

	if cloud.ErrorCode(err) != "unauthorized" || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), testToken) {
		t.Fatalf("KeepPrimaryIPs = %v, want Hetzner's 401 unauthorized without the token", err)
	}
}

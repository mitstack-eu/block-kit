// Package cloudtest is a fake of the Hetzner Cloud API's servers, firewalls and primary IPs that keeps state and answers as Hetzner documents.
package cloudtest

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Kinds a test names in Held, Object, Drop and Created.
const (
	Servers    = "servers"
	Firewalls  = "firewalls"
	PrimaryIPs = "primary_ips"
)

var singular = map[string]string{Servers: "server", Firewalls: "firewall", PrimaryIPs: "primary_ip"}

var inUseCodes = map[string]string{Firewalls: "resource_in_use", PrimaryIPs: "must_be_unassigned"}

// PrimaryIP is an address as a test seeds it, with AssigneeID 0 while no server holds it.
type PrimaryIP struct {
	Location   string
	AssigneeID int64
	AutoDelete bool
	Labels     map[string]string
}

type Fake struct {
	// Hook answers a request itself when it returns true, ahead of the fake's state, as a fault a test injects does.
	Hook func(w http.ResponseWriter, r *http.Request) bool
	// PerPage is how many resources one page of a list holds.
	PerPage int

	t       testing.TB
	srv     *httptest.Server
	token   string
	mu      sync.Mutex
	nextID  int64
	kinds   map[string]map[int64]map[string]any
	created map[string][]map[string]any
	writes  []string
}

// New serves the fake until the test ends, to a client presenting token.
func New(t testing.TB, token string) *Fake {
	t.Helper()
	f := &Fake{t: t, token: token, PerPage: 50, nextID: 1000, kinds: map[string]map[int64]map[string]any{}, created: map[string][]map[string]any{}}
	for kind := range singular {
		f.kinds[kind] = map[int64]map[string]any{}
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// URL is the API's base, which a test passes to its client.
func (f *Fake) URL() string { return f.srv.URL + "/v1" }

// Refuse answers as the API answers an error.
func Refuse(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": message}})
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func success() map[string]any {
	now := time.Now().UTC().Format(time.RFC3339)
	return map[string]any{"id": 1, "command": "x", "status": "success", "progress": 100, "resources": []any{}, "error": nil, "started": now, "finished": now}
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		Refuse(w, http.StatusUnauthorized, "unauthorized", "unable to authenticate")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/"), "/")
	kind := parts[0]
	if _, ok := singular[kind]; !ok {
		f.unmodelled(w, r)
		return
	}
	if f.Hook != nil && f.Hook(w, r) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	switch {
	case r.Method == http.MethodGet && len(parts) == 1:
		f.list(w, r, kind)
	case len(parts) == 2:
		f.byID(w, r, kind, parts[1], body)
	case r.Method == http.MethodPost && len(parts) == 1:
		f.writes = append(f.writes, r.Method+" "+r.URL.Path)
		f.created[kind] = append(f.created[kind], body)
		f.create(w, kind, body)
	default:
		f.unmodelled(w, r)
	}
}

func (f *Fake) unmodelled(w http.ResponseWriter, r *http.Request) {
	f.t.Errorf("the Hetzner fake received an unmodelled call: %s %s", r.Method, r.URL.RequestURI())
	Refuse(w, http.StatusNotImplemented, "not_implemented", "not modelled")
}

func (f *Fake) list(w http.ResponseWriter, r *http.Request, kind string) {
	matching := []any{}
	for _, id := range slices.Sorted(maps.Keys(f.kinds[kind])) {
		if labelsMatch(f.kinds[kind][id]["labels"].(map[string]any), r.URL.Query().Get("label_selector")) {
			matching = append(matching, f.kinds[kind][id])
		}
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 1)
	from, to := min((page-1)*f.PerPage, len(matching)), min(page*f.PerPage, len(matching))
	var next any
	if to < len(matching) {
		next = page + 1
	}
	pagination := map[string]any{"page": page, "per_page": f.PerPage, "last_page": max(1, (len(matching)+f.PerPage-1)/f.PerPage), "total_entries": len(matching), "next_page": next}
	reply(w, http.StatusOK, map[string]any{kind: matching[from:to], "meta": map[string]any{"pagination": pagination}})
}

func labelsMatch(labels map[string]any, selector string) bool {
	for _, term := range strings.Split(selector, ",") {
		if term == "" {
			continue
		}
		k, v, _ := strings.Cut(term, "=")
		if labels[k] != v {
			return false
		}
	}
	return true
}

func (f *Fake) byID(w http.ResponseWriter, r *http.Request, kind, rawID string, body map[string]any) {
	id, _ := strconv.ParseInt(rawID, 10, 64)
	obj, ok := f.kinds[kind][id]
	if !ok {
		Refuse(w, http.StatusNotFound, "not_found", kind+" not found")
		return
	}
	switch {
	case r.Method == http.MethodGet:
		reply(w, http.StatusOK, map[string]any{singular[kind]: obj})
	case r.Method == http.MethodDelete:
		if refusal := f.inUse(kind, id); refusal != "" {
			Refuse(w, http.StatusUnprocessableEntity, inUseCodes[kind], refusal)
			return
		}
		f.writes = append(f.writes, r.Method+" "+r.URL.Path)
		f.remove(kind, id)
		if kind == Servers {
			reply(w, http.StatusOK, map[string]any{"action": success()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut && kind == PrimaryIPs:
		f.writes = append(f.writes, r.Method+" "+r.URL.Path)
		obj["auto_delete"] = body["auto_delete"]
		reply(w, http.StatusOK, map[string]any{singular[kind]: obj})
	default:
		f.unmodelled(w, r)
	}
}

func (f *Fake) inUse(kind string, id int64) string {
	if kind == PrimaryIPs && f.kinds[kind][id]["assignee_id"] != nil {
		return "primary IP is assigned"
	}
	for _, s := range f.kinds[Servers] {
		for _, fw := range s["public_net"].(map[string]any)["firewalls"].([]any) {
			if kind == Firewalls && fw.(map[string]any)["id"] == id {
				return "firewall is applied"
			}
		}
	}
	return ""
}

// remove deletes a server as Hetzner does: its address goes with it while auto_delete is on, and is otherwise left unassigned.
func (f *Fake) remove(kind string, id int64) {
	obj := f.kinds[kind][id]
	delete(f.kinds[kind], id)
	if kind != Servers {
		return
	}
	ipID := obj["public_net"].(map[string]any)["ipv4"].(map[string]any)["id"].(int64)
	if ip, ok := f.kinds[PrimaryIPs][ipID]; ok {
		if ip["auto_delete"] == true {
			delete(f.kinds[PrimaryIPs], ipID)
		} else {
			ip["assignee_id"] = nil
		}
	}
}

func (f *Fake) create(w http.ResponseWriter, kind string, body map[string]any) {
	labels, _ := body["labels"].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	switch kind {
	case PrimaryIPs:
		autoDelete, _ := body["auto_delete"].(bool)
		id := f.addPrimaryIP(body["name"], body["location"], autoDelete, labels, now)
		reply(w, http.StatusCreated, map[string]any{"primary_ip": f.kinds[kind][id], "action": nil})
	case Firewalls:
		f.nextID++
		obj := map[string]any{"id": f.nextID, "name": body["name"], "labels": labels, "rules": body["rules"], "applied_to": []any{}, "created": now}
		f.kinds[kind][f.nextID] = obj
		reply(w, http.StatusCreated, map[string]any{"firewall": obj, "actions": []any{}})
	case Servers:
		f.nextID++
		f.createServer(w, f.nextID, body, labels, now)
	}
}

func (f *Fake) addPrimaryIP(name, location any, autoDelete bool, labels map[string]any, now string) int64 {
	f.nextID++
	id := f.nextID
	f.kinds[PrimaryIPs][id] = map[string]any{"id": id, "name": name, "type": "ipv4", "ip": fmt.Sprintf("203.0.113.%d", id%250), "labels": labels,
		"assignee_id": nil, "assignee_type": "server", "auto_delete": autoDelete, "created": now, "location": map[string]any{"name": location}}
	return id
}

func (f *Fake) createServer(w http.ResponseWriter, id int64, body, labels map[string]any, now string) {
	net, _ := body["public_net"].(map[string]any)
	ipID, _ := net["ipv4"].(float64)
	ip, ok := f.kinds[PrimaryIPs][int64(ipID)]
	if !ok || ip["assignee_id"] != nil {
		Refuse(w, http.StatusUnprocessableEntity, "primary_ip_assigned", "the primary IP is missing or assigned")
		return
	}
	ip["assignee_id"] = id
	firewalls := []any{}
	for _, fw := range body["firewalls"].([]any) {
		firewalls = append(firewalls, map[string]any{"id": int64(fw.(map[string]any)["firewall"].(float64)), "status": "applied"})
	}
	obj := map[string]any{
		"id": id, "name": body["name"], "status": "running", "created": now, "labels": labels,
		"server_type": map[string]any{"name": body["server_type"]}, "image": map[string]any{"name": body["image"]},
		"location":   map[string]any{"name": body["location"]},
		"public_net": map[string]any{"ipv4": map[string]any{"id": int64(ipID), "ip": ip["ip"]}, "firewalls": firewalls},
	}
	f.kinds[Servers][id] = obj
	reply(w, http.StatusCreated, map[string]any{"server": obj, "action": success(), "next_actions": []any{}, "root_password": "root-password-never-kept"})
}

// AddPrimaryIP seeds an address and returns its id.
func (f *Fake) AddPrimaryIP(ip PrimaryIP) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	labels := map[string]any{}
	for k, v := range ip.Labels {
		labels[k] = v
	}
	id := f.addPrimaryIP(fmt.Sprintf("ip-%d", f.nextID+1), ip.Location, ip.AutoDelete, labels, time.Now().UTC().Format(time.RFC3339))
	if ip.AssigneeID != 0 {
		f.kinds[PrimaryIPs][id]["assignee_id"] = ip.AssigneeID
	}
	return id
}

// SetAutoDelete sets an address's auto_delete as a person could in Hetzner's console.
func (f *Fake) SetAutoDelete(id int64, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kinds[PrimaryIPs][id]["auto_delete"] = on
}

// Assign marks an address as held by a server the fake need not know.
func (f *Fake) Assign(id, serverID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kinds[PrimaryIPs][id]["assignee_id"] = serverID
}

// Held gives the ids of each kind's resources the selector matches.
func (f *Fake) Held(selector string) map[string][]int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string][]int64{}
	for kind, objs := range f.kinds {
		for id, obj := range objs {
			if labelsMatch(obj["labels"].(map[string]any), selector) {
				out[kind] = append(out[kind], id)
			}
		}
	}
	return out
}

func (f *Fake) Object(kind string, id int64) (map[string]any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.kinds[kind][id]
	return obj, ok
}

// Drop deletes a resource behind the block's back, a server as Hetzner deletes one.
func (f *Fake) Drop(kind string, id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if kind == Servers {
		if _, ok := f.kinds[kind][id]; ok {
			f.remove(kind, id)
		}
		return
	}
	delete(f.kinds[kind], id)
}

// Writes lists each write the fake applied or created, as "METHOD /v1/path", leaving out one it refused.
func (f *Fake) Writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.writes)
}

// Created gives the bodies of each create of a kind, in order.
func (f *Fake) Created(kind string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.created[kind])
}

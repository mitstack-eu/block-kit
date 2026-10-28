// Package robottest is a fake of Hetzner's Robot webservice that answers in the shapes Robot answered a live trial.
package robottest

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/hetzner/robot"
)

const (
	User     = "robot-test-user"
	Password = "robot-test-password-0001"
)

// Server is a dedicated server as a test seeds it.
type Server struct {
	Number       int64
	Name, IP, DC string
	Status       string
	Cancelled    bool
	NoFirewall   bool
}

// State is where a server stands: Booted is "disk" or "rescue", and RescueKeys the keys its rescue boot admitted.
type State struct {
	Name         string
	Booted       string
	RescueActive bool
	RescueKeys   []string
}

type Request struct {
	Method, Path string
	Form         url.Values
}

type server struct {
	Server
	State
	pendingKeys []string
	firewall    firewall
}

type firewall struct {
	robot.Firewall
	settling int
}

type vswitch struct {
	robot.VSwitch
	joining, leaving map[int64]int
}

// Fake holds every server, key, firewall and vSwitch of one Robot account.
type Fake struct {
	*httptest.Server

	mu        sync.Mutex
	servers   map[int64]*server
	keys      []robot.Key
	keyData   map[string]string
	vswitches map[int64]*vswitch
	nextVS    int64
	requests  []Request
	atReset   map[int64][]robot.Firewall
	// HostKeys are the rescue host keys an activation answers, none by default as on the trial.
	HostKeys []string
}

// InternalRules are the two output rules Robot listed ahead of every rule a write sent.
var InternalRules = []robot.Rule{
	{Name: "Block mail ports", IPVersion: "ipv4", Protocol: "tcp", DstPort: "25,465", Action: "discard"},
	{Name: "Block mail ports", IPVersion: "ipv6", Protocol: "tcp", DstPort: "25,465", Action: "discard"},
}

func New(t testing.TB) *Fake {
	f := &Fake{servers: map[int64]*server{}, keyData: map[string]string{}, vswitches: map[int64]*vswitch{}, nextVS: 87417, atReset: map[int64][]robot.Firewall{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

// Client is a client logged in to the fake that polls without waiting.
func (f *Fake) Client() *robot.Client {
	c := robot.NewAt(f.URL, User, Password)
	c.PollInterval = time.Millisecond
	return c
}

func (f *Fake) AddServer(s Server) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s.Status == "" {
		s.Status = "ready"
	}
	f.servers[s.Number] = &server{Server: s, State: State{Name: s.Name, Booted: "disk"}, firewall: firewall{Firewall: robot.Firewall{
		Status: "active", WhitelistHOS: true, Input: []robot.Rule{{Name: "allow all", Action: "accept"}}, Output: []robot.Rule{{Name: "Allow all", Action: "accept"}},
	}}}
}

func (f *Fake) EditServer(number int64, edit func(*Server, *State)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.servers[number]
	edit(&s.Server, &s.State)
}

func (f *Fake) State(number int64) State {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.servers[number].State
	s.RescueKeys = slices.Clone(s.RescueKeys)
	return s
}

// SetFirewall seeds the rules a client once wrote, which a read lists after InternalRules.
func (f *Fake) SetFirewall(number int64, fw robot.Firewall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.servers[number].firewall = firewall{Firewall: fw}
}

// Firewall is the firewall as a read lists it once settled.
func (f *Fake) Firewall(number int64) robot.Firewall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.servers[number].firewall.read()
}

// FirewallsAtReset are the firewalls in force at each reset of the server.
func (f *Fake) FirewallsAtReset(number int64) []robot.Firewall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.atReset[number])
}

func (f *Fake) Keys() []robot.Key {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.keys)
}

func (f *Fake) VSwitches() []robot.VSwitch {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []robot.VSwitch
	for _, id := range slices.Sorted(maps.Keys(f.vswitches)) {
		out = append(out, f.vswitches[id].read(true))
	}
	return out
}

func (f *Fake) AddVSwitch(name string, vlan int, cancelled bool) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addVSwitch(name, vlan, cancelled).ID
}

func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// Writes are the requests that change something.
func (f *Fake) Writes() []Request {
	var out []Request
	for _, r := range f.Requests() {
		if r.Method != http.MethodGet {
			out = append(out, r)
		}
	}
	return out
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	user, password, ok := r.BasicAuth()
	form := url.Values{}
	if r.Method != http.MethodGet {
		raw, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(raw))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, Request{Method: r.Method, Path: r.URL.Path, Form: form})
	if !ok || user != User || password != Password {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED")
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch parts[0] {
	case "server":
		f.serveServer(w, r.Method, parts, form)
	case "key":
		f.serveKey(w, r.Method, parts, form)
	case "boot":
		f.serveRescue(w, r.Method, parts, form)
	case "reset":
		f.serveReset(w, r.Method, parts)
	case "firewall":
		f.serveFirewall(w, r.Method, parts, form)
	case "vswitch":
		f.serveVSwitch(w, r.Method, parts, form)
	default:
		fail(w, http.StatusNotFound, "NOT_FOUND")
	}
}

func fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"status": status, "code": code, "message": code}})
}

func answer(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (f *Fake) server(w http.ResponseWriter, parts []string) *server {
	if len(parts) < 2 {
		fail(w, http.StatusNotFound, "NOT_FOUND")
		return nil
	}
	n, _ := strconv.ParseInt(parts[1], 10, 64)
	s, ok := f.servers[n]
	if !ok {
		fail(w, http.StatusNotFound, "SERVER_NOT_FOUND")
		return nil
	}
	return s
}

func (f *Fake) serveServer(w http.ResponseWriter, method string, parts []string, form url.Values) {
	s := f.server(w, parts)
	if s == nil {
		return
	}
	if method == http.MethodPost {
		s.State.Name = form.Get("server_name")
	}
	answer(w, map[string]any{"server": map[string]any{
		"server_number": s.Number, "server_name": s.State.Name, "server_ip": s.IP, "dc": s.DC, "product": "AX42-2-LTD",
		"status": s.Status, "cancelled": s.Cancelled,
	}})
}

// fingerprint is the colon-separated MD5 of the key's wire blob, the form Robot names a key by.
func fingerprint(data string) (string, bool) {
	fields := strings.Fields(data)
	if len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return "", false
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return "", false
	}
	sum := md5.Sum(blob)
	hexes := make([]string, len(sum))
	for i, b := range sum {
		hexes[i] = fmt.Sprintf("%02x", b)
	}
	return strings.Join(hexes, ":"), true
}

func (f *Fake) serveKey(w http.ResponseWriter, method string, parts []string, form url.Values) {
	switch {
	case method == http.MethodGet && len(parts) == 1:
		if len(f.keys) == 0 {
			fail(w, http.StatusNotFound, "NOT_FOUND")
			return
		}
		var out []any
		for _, k := range f.keys {
			out = append(out, map[string]any{"key": map[string]any{"name": k.Name, "fingerprint": k.Fingerprint, "type": "ED25519", "size": 256}})
		}
		answer(w, out)
	case method == http.MethodPost && len(parts) == 1:
		fp, ok := fingerprint(form.Get("data"))
		if !ok || form.Get("name") == "" {
			fail(w, http.StatusBadRequest, "INVALID_INPUT")
			return
		}
		k := robot.Key{Name: form.Get("name"), Fingerprint: fp}
		f.keys, f.keyData[fp] = append(f.keys, k), form.Get("data")
		answer(w, map[string]any{"key": map[string]any{"name": k.Name, "fingerprint": k.Fingerprint, "type": "ED25519", "size": 256}})
	case method == http.MethodDelete && len(parts) == 2:
		fp, _ := url.PathUnescape(parts[1])
		i := slices.IndexFunc(f.keys, func(k robot.Key) bool { return k.Fingerprint == fp })
		if i < 0 {
			fail(w, http.StatusNotFound, "NOT_FOUND")
			return
		}
		f.keys = slices.Delete(f.keys, i, i+1)
	default:
		fail(w, http.StatusNotFound, "NOT_FOUND")
	}
}

func (f *Fake) serveRescue(w http.ResponseWriter, method string, parts []string, form url.Values) {
	s := f.server(w, parts)
	if s == nil {
		return
	}
	switch method {
	case http.MethodDelete:
		if !s.RescueActive {
			fail(w, http.StatusNotFound, "BOOT_NOT_AVAILABLE")
			return
		}
		s.RescueActive, s.pendingKeys = false, nil
		answer(w, map[string]any{"rescue": map[string]any{"active": false, "host_key": []any{}}})
	case http.MethodPost:
		var keys []string
		for _, fp := range form["authorized_key[]"] {
			data, ok := f.keyData[fp]
			if !ok || !slices.ContainsFunc(f.keys, func(k robot.Key) bool { return k.Fingerprint == fp }) {
				fail(w, http.StatusBadRequest, "INVALID_INPUT")
				return
			}
			keys = append(keys, data)
		}
		s.RescueActive, s.pendingKeys = true, keys
		hostKeys := []any{}
		for _, fp := range f.HostKeys {
			hostKeys = append(hostKeys, map[string]any{"key": map[string]any{"fingerprint": fp, "type": "ED25519", "size": 256}})
		}
		answer(w, map[string]any{"rescue": map[string]any{"active": true, "password": "rescue-root-password", "host_key": hostKeys}})
	default:
		fail(w, http.StatusNotFound, "NOT_FOUND")
	}
}

// serveReset clears an active rescue once it boots it, as Robot did on the trial.
func (f *Fake) serveReset(w http.ResponseWriter, method string, parts []string) {
	s := f.server(w, parts)
	if s == nil || method != http.MethodPost {
		return
	}
	f.atReset[s.Number] = append(f.atReset[s.Number], s.firewall.read())
	if s.RescueActive {
		s.Booted, s.RescueKeys, s.RescueActive, s.pendingKeys = "rescue", s.pendingKeys, false, nil
	} else {
		s.Booted, s.RescueKeys = "disk", nil
	}
	answer(w, map[string]any{"reset": map[string]any{"server_ip": s.IP, "type": "hw"}})
}

func (fw *firewall) read() robot.Firewall {
	out := fw.Firewall
	out.Input = slices.Clone(fw.Input)
	out.Output = append(slices.Clone(InternalRules), fw.Output...)
	return out
}

var ruleField = regexp.MustCompile(`^rules\[(input|output)\]\[(\d+)\]\[([a-z_]+)\]$`)

func (f *Fake) serveFirewall(w http.ResponseWriter, method string, parts []string, form url.Values) {
	s := f.server(w, parts)
	if s == nil {
		return
	}
	if s.NoFirewall {
		fail(w, http.StatusNotFound, "FIREWALL_NOT_AVAILABLE")
		return
	}
	fw := &s.firewall
	if method == http.MethodPost {
		if fw.settling > 0 {
			fail(w, http.StatusConflict, "FIREWALL_IN_PROCESS")
			return
		}
		rules := map[string]map[int]map[string]string{"input": {}, "output": {}}
		for key, values := range form {
			if m := ruleField.FindStringSubmatch(key); m != nil {
				i, _ := strconv.Atoi(m[2])
				if rules[m[1]][i] == nil {
					rules[m[1]][i] = map[string]string{}
				}
				rules[m[1]][i][m[3]] = values[0]
			}
		}
		fw.Firewall = robot.Firewall{
			Status: form.Get("status"), FilterIPv6: form.Get("filter_ipv6") == "true", WhitelistHOS: form.Get("whitelist_hos") == "true",
			Input: decodeRules(rules["input"]), Output: decodeRules(rules["output"]),
		}
		fw.settling = 1
	}
	read := fw.read()
	if fw.settling > 0 && method == http.MethodGet {
		read.Status = "in process"
		fw.settling--
	}
	answer(w, map[string]any{"firewall": map[string]any{
		"server_number": s.Number, "status": read.Status, "filter_ipv6": read.FilterIPv6, "whitelist_hos": read.WhitelistHOS, "port": "main",
		"rules": map[string]any{"input": ruleAnswers(read.Input), "output": ruleAnswers(read.Output)},
	}})
}

func decodeRules(byIndex map[int]map[string]string) []robot.Rule {
	var out []robot.Rule
	for _, i := range slices.Sorted(maps.Keys(byIndex)) {
		r := byIndex[i]
		out = append(out, robot.Rule{
			Name: r["name"], IPVersion: r["ip_version"], SrcIP: r["src_ip"], DstIP: r["dst_ip"], Protocol: r["protocol"],
			SrcPort: r["src_port"], DstPort: r["dst_port"], TCPFlags: r["tcp_flags"], Action: r["action"],
		})
	}
	return out
}

// ruleAnswers spells a field a rule leaves out as null, as Robot's answers do.
func ruleAnswers(rules []robot.Rule) []any {
	out := []any{}
	for _, r := range rules {
		rule := map[string]any{}
		for k, v := range map[string]string{
			"name": r.Name, "ip_version": r.IPVersion, "src_ip": r.SrcIP, "dst_ip": r.DstIP, "protocol": r.Protocol,
			"src_port": r.SrcPort, "dst_port": r.DstPort, "tcp_flags": r.TCPFlags, "action": r.Action,
		} {
			rule[k] = nil
			if v != "" {
				rule[k] = v
			}
		}
		out = append(out, rule)
	}
	return out
}

func (f *Fake) addVSwitch(name string, vlan int, cancelled bool) *vswitch {
	v := &vswitch{VSwitch: robot.VSwitch{ID: f.nextVS, Name: name, VLAN: vlan, Cancelled: cancelled}, joining: map[int64]int{}, leaving: map[int64]int{}}
	f.nextVS++
	f.vswitches[v.ID] = v
	return v
}

// read lists a server joining or leaving as "in process" for one read, as Robot took minutes to finish either.
func (v *vswitch) read(settle bool) robot.VSwitch {
	out := v.VSwitch
	out.Servers = nil
	for _, s := range v.Servers {
		status := "ready"
		if v.joining[s.Number] > 0 || v.leaving[s.Number] > 0 {
			status = "in process"
		}
		if settle && v.leaving[s.Number] > 0 {
			continue
		}
		out.Servers = append(out.Servers, robot.VSwitchServer{Number: s.Number, Status: status})
	}
	return out
}

func (v *vswitch) settle() {
	for n := range v.joining {
		v.joining[n]--
	}
	for n := range v.leaving {
		if v.leaving[n]--; v.leaving[n] == 0 {
			v.Servers = slices.DeleteFunc(v.Servers, func(s robot.VSwitchServer) bool { return s.Number == n })
			delete(v.leaving, n)
		}
	}
}

func vswitchAnswer(v robot.VSwitch, full bool) map[string]any {
	out := map[string]any{"id": v.ID, "name": v.Name, "vlan": v.VLAN, "cancelled": v.Cancelled}
	if full {
		servers := []any{}
		for _, s := range v.Servers {
			servers = append(servers, map[string]any{"server_number": s.Number, "status": s.Status})
		}
		out["server"], out["subnet"], out["cloud_network"] = servers, []any{}, []any{}
	}
	return out
}

func (f *Fake) serveVSwitch(w http.ResponseWriter, method string, parts []string, form url.Values) {
	if len(parts) == 1 {
		switch method {
		case http.MethodGet:
			out := []any{}
			for _, id := range slices.Sorted(maps.Keys(f.vswitches)) {
				out = append(out, vswitchAnswer(f.vswitches[id].VSwitch, false))
			}
			answer(w, out)
		case http.MethodPost:
			vlan, _ := strconv.Atoi(form.Get("vlan"))
			if slices.ContainsFunc(slices.Collect(maps.Values(f.vswitches)), func(v *vswitch) bool { return v.VLAN == vlan }) {
				fail(w, http.StatusConflict, "VSWITCH_VLAN_NOT_UNIQUE")
				return
			}
			answer(w, vswitchAnswer(f.addVSwitch(form.Get("name"), vlan, false).read(false), true))
		}
		return
	}
	id, _ := strconv.ParseInt(parts[1], 10, 64)
	v, ok := f.vswitches[id]
	if !ok {
		fail(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	switch {
	case len(parts) == 2 && method == http.MethodGet:
		answer(w, vswitchAnswer(v.read(false), true))
		v.settle()
	case len(parts) == 2 && method == http.MethodDelete:
		switch {
		case v.Cancelled:
			fail(w, http.StatusConflict, "CONFLICT")
		case len(v.Servers) > 0:
			fail(w, http.StatusConflict, "VSWITCH_IN_PROCESS")
		default:
			v.Cancelled = true
		}
	case len(parts) == 3 && parts[2] == "server":
		for _, raw := range form["server[]"] {
			n, _ := strconv.ParseInt(raw, 10, 64)
			listed := slices.ContainsFunc(v.Servers, func(s robot.VSwitchServer) bool { return s.Number == n })
			switch {
			case method == http.MethodPost && !listed:
				v.Servers, v.joining[n] = append(v.Servers, robot.VSwitchServer{Number: n}), 1
			case method == http.MethodDelete && listed:
				v.leaving[n] = 2
			}
		}
	default:
		fail(w, http.StatusNotFound, "NOT_FOUND")
	}
}

// Package signin, an optional helper, is the sign-in companion a Docker host's edge asks about every request to an
// app that opens only to its org's members, signing each member in through mitstack and re-checking them.
package signin

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ContainerName = "mitstack-signin"
	Port          = "8080"
	Upstream      = ContainerName + ":" + Port

	// The edge's route sets these on every request it sends the companion, so they describe the app the route serves.
	AppHeader        = "X-Mitstack-App"
	SignInHostHeader = "X-Mitstack-Sign-In-Host"
	HostsHeader      = "X-Mitstack-Hosts"

	SessionCookie = "__Host-mitstack-app"
	SignInCookie  = "__Host-mitstack-sign-in"

	// SignInsPerMinute bounds the sign-in requests one visitor address makes, since every code the companion redeems
	// reaches mitstack from the host's one address, where mitstack limits refused ones.
	SignInsPerMinute = 30
)

// MemberHeaders are the request headers the app receives about the member, which the edge deletes from every request.
var MemberHeaders = []string{"X-Mitstack-User", "X-Mitstack-Email", "X-Mitstack-Name", "X-Mitstack-Org", "X-Mitstack-Role"}

const (
	recheckAfter   = 5 * time.Minute
	signInLifetime = 10 * time.Minute
	ticketLifetime = 60 * time.Second
	callbackPath   = "/.mitstack/callback"
	keySize        = 32
	maxPathLength  = 2048
	maxAnswerBytes = 64 << 10
	sweepEvery     = time.Minute
)

var appIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Config is where mitstack answers, the key that seals every cookie, state and ticket, the clock, and Ended, a
// file that keeps each signed-out session ended across restarts, since mitstack learns of no sign-out.
type Config struct {
	Platform string
	Key      []byte
	Client   *http.Client
	Now      func() time.Time
	Logf     func(format string, args ...any)
	Ended    string
}

type companion struct {
	platform string
	aead     cipher.AEAD
	client   *http.Client
	now      func() time.Time
	logf     func(format string, args ...any)
	ended    string

	mu       sync.Mutex
	sessions map[string]*memo
	tickets  map[string]time.Time
	swept    time.Time
	window   time.Time
	signIns  map[string]int
}

// memo is what the companion learnt about one app session since it was set: a cookie set before a re-check cannot
// be rewritten, since the edge copies no header of a 2xx answer back to the visitor.
type memo struct {
	mu      sync.Mutex
	checked time.Time
	member  member
	ended   bool
	expires time.Time
}

type member struct {
	User  string `json:"sub"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Org   string `json:"org_id"`
	Role  string `json:"role"`
	App   string `json:"app_id"`
}

type session struct {
	ID      string `json:"id"`
	Token   string `json:"token"`
	Member  member `json:"member"`
	Checked int64  `json:"checked"`
	Expires int64  `json:"expires"`
}

type signInCookie struct {
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier,omitempty"`
	Expires  int64  `json:"expires"`
}

// target is where a visitor returns once signed in, as a hostname the app serves and a path on it, with Handoff the
// nonce of the sign-in cookie a custom hostname set in the browser that started there.
type target struct {
	Nonce   string `json:"nonce,omitempty"`
	Handoff string `json:"handoff,omitempty"`
	Host    string `json:"host"`
	Path    string `json:"path"`
	Expires int64  `json:"expires"`
}

type ticket struct {
	ID      string  `json:"id"`
	Handoff string  `json:"handoff"`
	Host    string  `json:"host"`
	Path    string  `json:"path"`
	Session session `json:"session"`
	Expires int64   `json:"expires"`
}

// route is the app the edge's route describes, and the hostname of this request, which must be one it serves.
type route struct {
	app, signInHost, host string
	hosts                 []string
}

type verdict int

const (
	admitted verdict = iota
	refused
	unavailable
)

// New is the companion's handler, refusing a key that is not 32 bytes or a mitstack address that is not https.
func New(c Config) (http.Handler, error) {
	if len(c.Key) != keySize {
		return nil, fmt.Errorf("signin: the key is %d bytes; want %d", len(c.Key), keySize)
	}
	if u, err := url.Parse(c.Platform); err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" {
		return nil, fmt.Errorf("signin: mitstack's address %q is not an https origin", c.Platform)
	}
	block, err := aes.NewCipher(c.Key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	comp := &companion{
		platform: c.Platform, aead: aead, client: c.Client, now: c.Now, logf: c.Logf, ended: c.Ended,
		sessions: map[string]*memo{}, tickets: map[string]time.Time{}, signIns: map[string]int{},
	}
	if comp.client == nil {
		comp.client = &http.Client{Timeout: 10 * time.Second}
	}
	if comp.now == nil {
		comp.now = time.Now
	}
	if comp.logf == nil {
		comp.logf = func(string, ...any) {}
	}
	if err := comp.loadEnded(); err != nil {
		return nil, err
	}
	return comp, nil
}

// loadEnded reads the ended sessions that have not expired and writes the file back with only those.
func (c *companion) loadEnded() error {
	if c.ended == "" {
		return nil
	}
	raw, err := os.ReadFile(c.ended)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var kept strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		id, at, ok := strings.Cut(line, " ")
		expires, err := strconv.ParseInt(at, 10, 64)
		if !ok || err != nil || !c.now().Before(time.Unix(expires, 0)) {
			continue
		}
		c.sessions[id] = &memo{ended: true, expires: time.Unix(expires, 0)}
		kept.WriteString(line + "\n")
	}
	return os.WriteFile(c.ended, []byte(kept.String()), 0o600)
}

func (c *companion) keepEnded(s session) {
	if c.ended == "" {
		return
	}
	f, err := os.OpenFile(c.ended, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err == nil {
		_, err = fmt.Fprintf(f, "%s %d\n", s.ID, s.Expires)
		err = errors.Join(err, f.Close())
	}
	if err != nil {
		c.logf("signin: a signed-out session was not kept ended across restarts: %v", err)
	}
}

// LoadKey reads the companion's key from path, creating it on the first start, and refuses a file of another size.
func LoadKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		key = random(keySize)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			return LoadKey(path)
		}
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(key); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return nil, err
		}
		return key, f.Close()
	}
	if err != nil {
		return nil, err
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("signin: %s holds %d bytes; want a %d-byte key", path, len(key), keySize)
	}
	return key, nil
}

func (c *companion) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	rt, ok := routeOf(r)
	if !ok {
		page(w, http.StatusInternalServerError, "The host's edge sent this request without describing the app it serves.")
		return
	}
	switch r.URL.Path {
	case "/.mitstack/check":
		c.check(w, r, rt)
	case "/.mitstack/sign-out":
		c.signOut(w, r, rt)
	case "/.mitstack/start", callbackPath, "/.mitstack/handoff":
		if !c.allowSignIn(r) {
			page(w, http.StatusTooManyRequests, "Too many sign-ins from your address. Wait a minute and open the app again.")
			return
		}
		switch r.URL.Path {
		case "/.mitstack/start":
			c.start(w, r, rt)
		case callbackPath:
			c.callback(w, r, rt)
		default:
			c.handoff(w, r, rt)
		}
	default:
		page(w, http.StatusNotFound, "Nothing is here.")
	}
}

func routeOf(r *http.Request) (route, bool) {
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	rt := route{
		app:        r.Header.Get(AppHeader),
		signInHost: r.Header.Get(SignInHostHeader),
		host:       host,
		hosts:      strings.Fields(r.Header.Get(HostsHeader)),
	}
	return rt, appIDRe.MatchString(rt.app) && rt.serves(rt.host) && rt.serves(rt.signInHost)
}

func (rt route) serves(host string) bool {
	for _, h := range rt.hosts {
		if h == host {
			return true
		}
	}
	return false
}

func (c *companion) check(w http.ResponseWriter, r *http.Request, rt route) {
	method := r.Header.Get("X-Forwarded-Method")
	if s, ok := c.session(r, rt); ok {
		m, v := c.admit(r.Context(), s, rt)
		switch v {
		case admitted:
			for name, value := range map[string]string{
				MemberHeaders[0]: m.User, MemberHeaders[1]: m.Email, MemberHeaders[2]: m.Name,
				MemberHeaders[3]: m.Org, MemberHeaders[4]: m.Role,
			} {
				w.Header().Set(name, value)
			}
			w.WriteHeader(http.StatusOK)
			return
		case unavailable:
			page(w, http.StatusServiceUnavailable, "mitstack could not confirm you are still a member of this app's org. Try again in a minute.")
			return
		}
		clearCookie(w, SessionCookie)
	}
	if method != http.MethodGet && method != http.MethodHead {
		page(w, http.StatusUnauthorized, "Sign in to this app first: open it in your browser.")
		return
	}
	redirect(w, "/.mitstack/start?return="+url.QueryEscape(r.Header.Get("X-Forwarded-Uri")))
}

func (c *companion) session(r *http.Request, rt route) (session, bool) {
	var s session
	cookie, err := r.Cookie(SessionCookie)
	if err != nil || !c.open("session", rt.app+"\x00"+rt.host, cookie.Value, &s) || !c.now().Before(time.Unix(s.Expires, 0)) {
		return session{}, false
	}
	return s, true
}

// admit re-checks the session at mitstack once five minutes have passed since the last re-check, one at a time.
func (c *companion) admit(ctx context.Context, s session, rt route) (member, verdict) {
	m := c.memoOf(s)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ended {
		return member{}, refused
	}
	if c.now().Sub(m.checked) < recheckAfter {
		return m.member, admitted
	}
	info, v := c.userinfo(ctx, s.Token, rt)
	switch v {
	case admitted:
		m.checked, m.member = c.now(), info
	case refused:
		m.ended = true
	}
	return info, v
}

func (c *companion) memoOf(s session) *memo {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if now.Sub(c.swept) >= sweepEvery {
		for id, m := range c.sessions {
			if !now.Before(m.expires) {
				delete(c.sessions, id)
			}
		}
		for id, expires := range c.tickets {
			if !now.Before(expires) {
				delete(c.tickets, id)
			}
		}
		c.swept = now
	}
	m, ok := c.sessions[s.ID]
	if !ok {
		m = &memo{checked: time.Unix(s.Checked, 0), member: s.Member, expires: time.Unix(s.Expires, 0)}
		c.sessions[s.ID] = m
	}
	return m
}

func (c *companion) start(w http.ResponseWriter, r *http.Request, rt route) {
	now := c.now()
	dest := target{Host: rt.host, Path: safePath(r.URL.Query().Get("return"))}
	if rt.host != rt.signInHost {
		dest.Handoff = encode(random(32))
		dest.Expires = now.Add(signInLifetime).Unix()
		setCookie(w, SignInCookie, c.seal("handoff", rt.app+"\x00"+rt.host, signInCookie{Nonce: dest.Handoff, Expires: dest.Expires}), int(signInLifetime/time.Second))
		redirect(w, "https://"+rt.signInHost+"/.mitstack/start?to="+url.QueryEscape(c.seal("to", rt.app, dest)))
		return
	}
	if to := r.URL.Query().Get("to"); to != "" {
		if !c.open("to", rt.app, to, &dest) || !now.Before(time.Unix(dest.Expires, 0)) || !rt.serves(dest.Host) {
			page(w, http.StatusBadRequest, "This sign-in link has expired or names a site this app does not serve. Open the app again.")
			return
		}
		dest.Path = safePath(dest.Path)
	}
	verifier := encode(random(32))
	challenge := sha256.Sum256([]byte(verifier))
	dest.Nonce = encode(random(32))
	dest.Expires = now.Add(signInLifetime).Unix()
	setCookie(w, SignInCookie, c.seal("sign-in", rt.app+"\x00"+rt.host, signInCookie{Nonce: dest.Nonce, Verifier: verifier, Expires: dest.Expires}), int(signInLifetime/time.Second))
	redirect(w, c.platform+"/oauth/authorize?"+url.Values{
		"response_type":         {"code"},
		"client_id":             {"app:" + rt.app},
		"redirect_uri":          {"https://" + rt.signInHost + callbackPath},
		"code_challenge":        {encode(challenge[:])},
		"code_challenge_method": {"S256"},
		"state":                 {c.seal("state", rt.app, dest)},
	}.Encode())
}

func (c *companion) callback(w http.ResponseWriter, r *http.Request, rt route) {
	clearCookie(w, SignInCookie)
	now := c.now()
	var started signInCookie
	var dest target
	cookie, err := r.Cookie(SignInCookie)
	if err != nil || rt.host != rt.signInHost ||
		!c.open("sign-in", rt.app+"\x00"+rt.host, cookie.Value, &started) ||
		!c.open("state", rt.app, r.URL.Query().Get("state"), &dest) ||
		subtle.ConstantTimeCompare([]byte(started.Nonce), []byte(dest.Nonce)) != 1 ||
		!now.Before(time.Unix(started.Expires, 0)) || !rt.serves(dest.Host) || r.URL.Query().Get("code") == "" {
		page(w, http.StatusBadRequest, "This sign-in did not start in this browser, or took longer than ten minutes. Open the app again.")
		return
	}
	token, lifetime, v := c.redeem(r.Context(), rt, r.URL.Query().Get("code"), started.Verifier)
	if v == admitted {
		var info member
		info, v = c.userinfo(r.Context(), token, rt)
		if v == admitted {
			c.signedIn(w, rt, dest, session{Token: token, Member: info, Checked: now.Unix(), Expires: now.Add(lifetime).Unix()})
			return
		}
		if v == refused {
			page(w, http.StatusForbidden, "mitstack did not confirm you as a member of this app's org.")
			return
		}
	}
	if v == refused {
		page(w, http.StatusBadRequest, "mitstack refused this sign-in. Open the app again.")
		return
	}
	page(w, http.StatusServiceUnavailable, "mitstack did not answer the sign-in. Try again in a minute.")
}

// signedIn sets the app session on the sign-in hostname and returns the visitor there, or hands it to the hostname
// they started at with a sealed ticket that hostname accepts once within sixty seconds.
func (c *companion) signedIn(w http.ResponseWriter, rt route, dest target, s session) {
	c.setSession(w, rt, s)
	if dest.Host == rt.host {
		redirect(w, dest.Path)
		return
	}
	t := ticket{ID: encode(random(16)), Handoff: dest.Handoff, Host: dest.Host, Path: dest.Path, Session: s, Expires: c.now().Add(ticketLifetime).Unix()}
	redirect(w, "https://"+dest.Host+"/.mitstack/handoff?ticket="+url.QueryEscape(c.seal("ticket", rt.app, t)))
}

// handoff accepts a ticket only in the browser that started the sign-in on this hostname, whose sign-in cookie
// holds the ticket's nonce, so nobody can hand a visitor a link that signs them in as someone else.
func (c *companion) handoff(w http.ResponseWriter, r *http.Request, rt route) {
	var t ticket
	var started signInCookie
	cookie, err := r.Cookie(SignInCookie)
	if err != nil || !c.open("handoff", rt.app+"\x00"+rt.host, cookie.Value, &started) ||
		!c.open("ticket", rt.app, r.URL.Query().Get("ticket"), &t) || t.Host != rt.host || t.Handoff == "" ||
		subtle.ConstantTimeCompare([]byte(started.Nonce), []byte(t.Handoff)) != 1 ||
		!c.now().Before(time.Unix(t.Expires, 0)) || !c.useTicket(t.ID, time.Unix(t.Expires, 0)) {
		page(w, http.StatusBadRequest, "This sign-in link was used already, has expired, or did not start in this browser. Open the app again.")
		return
	}
	clearCookie(w, SignInCookie)
	c.setSession(w, rt, t.Session)
	redirect(w, safePath(t.Path))
}

func (c *companion) useTicket(id string, expires time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, used := c.tickets[id]; used {
		return false
	}
	c.tickets[id] = expires
	return true
}

// setSession sets s on rt's hostname under a new id, so each hostname's session is re-checked and ended on its own.
func (c *companion) setSession(w http.ResponseWriter, rt route, s session) {
	s.ID = encode(random(16))
	c.memoOf(s)
	maxAge := int(time.Unix(s.Expires, 0).Sub(c.now()) / time.Second)
	setCookie(w, SessionCookie, c.seal("session", rt.app+"\x00"+rt.host, s), maxAge)
}

// signOut ends the session only on a POST from this hostname's own pages, and offers a form on a GET, so a link or
// an image on another site signs nobody out.
func (c *companion) signOut(w http.ResponseWriter, r *http.Request, rt route) {
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		fmt.Fprint(w, "<!doctype html><meta charset=utf-8><title>mitstack sign-in</title>"+
			`<form method=post action="/.mitstack/sign-out"><button>Sign out of this app</button></form>`+"\n")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "https://"+rt.host {
		page(w, http.StatusForbidden, "Sign out from this app's own page.")
		return
	}
	if s, ok := c.session(r, rt); ok {
		m := c.memoOf(s)
		m.mu.Lock()
		m.ended = true
		m.mu.Unlock()
		c.keepEnded(s)
	}
	clearCookie(w, SessionCookie)
	page(w, http.StatusOK, "You are signed out of this app.")
}

func (c *companion) redeem(ctx context.Context, rt route, code, verifier string) (string, time.Duration, verdict) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {"https://" + rt.signInHost + callbackPath},
		"client_id":     {"app:" + rt.app},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.platform+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, unavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var answer struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	switch v := c.ask(req, &answer); {
	case v != admitted:
		return "", 0, v
	case answer.AccessToken == "" || answer.ExpiresIn <= 0:
		c.logf("signin: mitstack's token answer for app %s holds no token or lifetime", rt.app)
		return "", 0, unavailable
	}
	return answer.AccessToken, time.Duration(answer.ExpiresIn) * time.Second, admitted
}

func (c *companion) userinfo(ctx context.Context, token string, rt route) (member, verdict) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.platform+"/oauth/userinfo", nil)
	if err != nil {
		return member{}, unavailable
	}
	req.Header.Set("Authorization", "Bearer "+token)
	var m member
	if v := c.ask(req, &m); v != admitted {
		return member{}, v
	}
	if m.App != rt.app || m.User == "" || m.Org == "" {
		c.logf("signin: mitstack's userinfo for app %s names app %q, user %q and org %q", rt.app, m.App, m.User, m.Org)
		return member{}, refused
	}
	return m, admitted
}

// ask reads a 200 answer into into; a 400 or 401 refuses, and anything else, a 429 from the host's shared address
// included, leaves the visitor where they are.
func (c *companion) ask(req *http.Request, into any) verdict {
	res, err := c.client.Do(req)
	if err != nil {
		c.logf("signin: %s %s: %v", req.Method, req.URL.Path, err)
		return unavailable
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxAnswerBytes))
	switch {
	case res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusUnauthorized:
		return refused
	case res.StatusCode != http.StatusOK || err != nil:
		c.logf("signin: %s %s answered %d", req.Method, req.URL.Path, res.StatusCode)
		return unavailable
	case json.Unmarshal(body, into) != nil:
		c.logf("signin: %s %s answered JSON it could not read", req.Method, req.URL.Path)
		return unavailable
	}
	return admitted
}

func (c *companion) allowSignIn(r *http.Request) bool {
	address := r.RemoteAddr
	if host, _, err := net.SplitHostPort(address); err == nil {
		address = host
	}
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		address = strings.TrimSpace(parts[len(parts)-1])
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if now := c.now(); now.Sub(c.window) >= time.Minute {
		c.window, c.signIns = now, map[string]int{}
	}
	c.signIns[address]++
	return c.signIns[address] <= SignInsPerMinute
}

func (c *companion) seal(purpose, binding string, v any) string {
	plain, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	nonce := random(c.aead.NonceSize())
	return encode(c.aead.Seal(nonce, nonce, plain, []byte(purpose+"\x00"+binding)))
}

func (c *companion) open(purpose, binding, sealed string, into any) bool {
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil || len(raw) < c.aead.NonceSize() {
		return false
	}
	plain, err := c.aead.Open(nil, raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():], []byte(purpose+"\x00"+binding))
	return err == nil && json.Unmarshal(plain, into) == nil
}

// safePath is p when it is a path on the same hostname, starting with a single slash, and / otherwise.
func safePath(p string) string {
	if len(p) == 0 || len(p) > maxPathLength || p[0] != '/' || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\") {
		return "/"
	}
	for i := 0; i < len(p); i++ {
		if p[i] < 0x20 || p[i] == 0x7f {
			return "/"
		}
	}
	return p
}

func random(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func encode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func redirect(w http.ResponseWriter, location string) {
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusFound)
}

func setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func clearCookie(w http.ResponseWriter, name string) { setCookie(w, name, "", -1) }

func page(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>mitstack sign-in</title><p>%s</p>\n", html.EscapeString(text))
}

package signin_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/helpers/signin"
)

const (
	appID      = "app_8d21b0c4"
	signInHost = "costs.apps.mitstack.dev"
	customHost = "shop.example.com"
	token      = "mst_app_token"
	code       = "code-1"
)

var hosts = signInHost + " " + customHost

// platform stands in for mitstack's token and userinfo routes as an app's client meets them.
type platform struct {
	*httptest.Server
	mu             sync.Mutex
	challenge      string
	tokenStatus    int
	userinfoStatus int
	role           string
	userinfoApp    string
	tokenForms     []url.Values
	userinfoCalls  int
}

func newPlatform(t *testing.T) *platform {
	p := &platform{tokenStatus: http.StatusOK, userinfoStatus: http.StatusOK, role: "member", userinfoApp: appID}
	p.Server = httptest.NewTLSServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.Close)
	return p
}

func (p *platform) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch r.URL.Path {
	case "/oauth/token":
		_ = r.ParseForm()
		p.tokenForms = append(p.tokenForms, r.PostForm)
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if p.tokenStatus != http.StatusOK {
			http.Error(w, `{"error":"invalid_grant"}`, p.tokenStatus)
			return
		}
		if r.PostForm.Get("code") != code || base64.RawURLEncoding.EncodeToString(sum[:]) != p.challenge {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 43200})
	case "/oauth/userinfo":
		p.userinfoCalls++
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if p.userinfoStatus != http.StatusOK {
			w.WriteHeader(p.userinfoStatus)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"sub": "user_3f9c2a1b", "email": "yael@example.com", "name": "Yael Tassenaar",
			"org_id": "org_8d21b0c4", "role": p.role, "app_id": p.userinfoApp,
		})
	default:
		http.NotFound(w, r)
	}
}

func (p *platform) set(f func(p *platform)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

func (p *platform) calls() (token, userinfo int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.tokenForms), p.userinfoCalls
}

type companion struct {
	t        *testing.T
	handler  http.Handler
	platform *platform
	now      time.Time
}

func newCompanion(t *testing.T) *companion {
	c := &companion{t: t, platform: newPlatform(t), now: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
	handler, err := signin.New(signin.Config{
		Platform: c.platform.URL,
		Key:      make([]byte, 32),
		Client:   c.platform.Client(),
		Now:      func() time.Time { return c.now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.handler = handler
	return c
}

// visitor is one browser, holding cookies per hostname, reaching the companion the way the edge's route sends it.
type visitor struct {
	c       *companion
	address string
	app     string
	jar     map[string]map[string]string
}

func (c *companion) visitor(address string) *visitor {
	return &visitor{c: c, address: address, app: appID, jar: map[string]map[string]string{}}
}

func (v *visitor) do(method, host, target string, header http.Header) *http.Response {
	v.c.t.Helper()
	r := httptest.NewRequest(method, target, nil)
	r.Host = host
	for name, values := range header {
		r.Header[name] = values
	}
	r.Header.Set(signin.AppHeader, v.app)
	r.Header.Set(signin.SignInHostHeader, signInHost)
	r.Header.Set(signin.HostsHeader, hosts)
	r.Header.Set("X-Forwarded-For", v.address)
	for name, value := range v.jar[host] {
		r.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	w := httptest.NewRecorder()
	v.c.handler.ServeHTTP(w, r)
	res := w.Result()
	for _, cookie := range res.Cookies() {
		if v.jar[host] == nil {
			v.jar[host] = map[string]string{}
		}
		if cookie.MaxAge < 0 {
			delete(v.jar[host], cookie.Name)
		} else {
			v.jar[host][cookie.Name] = cookie.Value
		}
	}
	return res
}

func (v *visitor) check(host, method, uri string) *http.Response {
	v.c.t.Helper()
	return v.do(http.MethodGet, host, "/.mitstack/check", http.Header{"X-Forwarded-Method": {method}, "X-Forwarded-Uri": {uri}})
}

// follow requests a same-host Location or an absolute one on a hostname the app serves.
func (v *visitor) follow(host string, res *http.Response) (string, *http.Response) {
	v.c.t.Helper()
	location, err := url.Parse(res.Header.Get("Location"))
	if err != nil || location.Path == "" {
		v.c.t.Fatalf("status %d with Location %q; want a redirect", res.StatusCode, res.Header.Get("Location"))
	}
	if location.Host != "" {
		host = location.Host
	}
	return host, v.do(http.MethodGet, host, location.RequestURI(), nil)
}

// authorize plays mitstack's authorize route for a member: it checks the request and redirects to the callback with a code.
func (v *visitor) authorize(res *http.Response) (string, *http.Response) {
	v.c.t.Helper()
	if res.StatusCode != http.StatusFound {
		v.c.t.Fatalf("start answered %d; want 302 to mitstack's authorize route", res.StatusCode)
	}
	to, _ := url.Parse(res.Header.Get("Location"))
	if got := to.Scheme + "://" + to.Host + to.Path; got != v.c.platform.URL+"/oauth/authorize" {
		v.c.t.Fatalf("start redirected to %s; want %s/oauth/authorize", got, v.c.platform.URL)
	}
	q := to.Query()
	v.c.platform.set(func(p *platform) { p.challenge = q.Get("code_challenge") })
	callback, _ := url.Parse(q.Get("redirect_uri"))
	back := url.Values{"code": {code}, "state": {q.Get("state")}}
	return callback.Host, v.do(http.MethodGet, callback.Host, callback.Path+"?"+back.Encode(), nil)
}

// signIn runs the whole sign-in from a request with no app session and returns the last answer, on the host it ends at.
func (v *visitor) signIn(host, uri string) (string, *http.Response) {
	v.c.t.Helper()
	res := v.check(host, http.MethodGet, uri)
	host, res = v.follow(host, res)
	if host != signInHost {
		host, res = v.follow(host, res)
	}
	host, res = v.authorize(res)
	if loc, _ := url.Parse(res.Header.Get("Location")); res.StatusCode == http.StatusFound && loc.Host != "" {
		host, res = v.follow(host, res)
	}
	return host, res
}

func cookie(res *http.Response, name string) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func wantRedirect(t *testing.T, res *http.Response, location string) {
	t.Helper()
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != location {
		t.Fatalf("answered %d with Location %q; want 302 to %q", res.StatusCode, res.Header.Get("Location"), location)
	}
}

func wantMember(t *testing.T, res *http.Response, role string) {
	t.Helper()
	want := map[string]string{
		"X-Mitstack-User": "user_3f9c2a1b", "X-Mitstack-Email": "yael@example.com", "X-Mitstack-Name": "Yael Tassenaar",
		"X-Mitstack-Org": "org_8d21b0c4", "X-Mitstack-Role": role,
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("check answered %d; want 200 letting the member through", res.StatusCode)
	}
	for name, value := range want {
		if got := res.Header.Get(name); got != value {
			t.Errorf("check answered %s: %q; want %q", name, got, value)
		}
	}
}

func TestCheck_SendsAGetWithNoAppSessionToSignInAndRefusesAnyOtherMethod(t *testing.T) {
	v := newCompanion(t).visitor("198.51.100.7")

	wantRedirect(t, v.check(signInHost, http.MethodGet, "/plan?week=3"), "/.mitstack/start?return=%2Fplan%3Fweek%3D3")
	wantRedirect(t, v.check(signInHost, http.MethodHead, "/plan"), "/.mitstack/start?return=%2Fplan")
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if res := v.check(signInHost, method, "/plan"); res.StatusCode != http.StatusUnauthorized {
			t.Errorf("check of a %s with no app session answered %d; want 401", method, res.StatusCode)
		}
	}
}

func TestStart_SetsASealedTenMinuteSignInCookieAndRedirectsToAuthorizeWithPKCE(t *testing.T) {
	c := newCompanion(t)
	v := c.visitor("198.51.100.7")

	res := v.do(http.MethodGet, signInHost, "/.mitstack/start?return=%2Fplan", nil)

	if res.StatusCode != http.StatusFound {
		t.Fatalf("start answered %d; want 302", res.StatusCode)
	}
	to, _ := url.Parse(res.Header.Get("Location"))
	q := to.Query()
	for name, want := range map[string]string{
		"response_type":         "code",
		"client_id":             "app:" + appID,
		"redirect_uri":          "https://" + signInHost + "/.mitstack/callback",
		"code_challenge_method": "S256",
	} {
		if got := q.Get(name); got != want {
			t.Errorf("authorize %s = %q; want %q", name, got, want)
		}
	}
	if len(q.Get("code_challenge")) != 43 || q.Get("state") == "" || strings.Contains(q.Get("state"), "/plan") {
		t.Errorf("authorize code_challenge %q, state %q; want an S256 challenge and a sealed state", q.Get("code_challenge"), q.Get("state"))
	}
	if q.Has("resource") {
		t.Errorf("authorize names resource %q; an app's client never sends one", q.Get("resource"))
	}
	signIn := cookie(res, signin.SignInCookie)
	if signIn == nil || !signIn.Secure || !signIn.HttpOnly || signIn.SameSite != http.SameSiteLaxMode || signIn.Path != "/" || signIn.Domain != "" || signIn.MaxAge != 600 {
		t.Errorf("start set sign-in cookie %+v; want a Secure, HttpOnly, SameSite=Lax host cookie lasting 600 seconds", signIn)
	}
}

func TestSignIn_ReturnsAMemberToThePathTheyAskedForWithASealedAppSession(t *testing.T) {
	c := newCompanion(t)
	v := c.visitor("198.51.100.7")

	host, res := v.signIn(signInHost, "/plan?week=3")

	if host != signInHost {
		t.Fatalf("sign-in ended on %s; want %s", host, signInHost)
	}
	wantRedirect(t, res, "/plan?week=3")
	session := cookie(res, signin.SessionCookie)
	if session == nil || !session.Secure || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode || session.Path != "/" || session.Domain != "" {
		t.Fatalf("callback set app session %+v; want a Secure, HttpOnly, SameSite=Lax host cookie", session)
	}
	if strings.Contains(session.Value, token) || strings.Contains(session.Value, "yael") {
		t.Errorf("app session %q holds the token or the member's details in the clear", session.Value)
	}
	if cleared := cookie(res, signin.SignInCookie); cleared == nil || cleared.MaxAge >= 0 {
		t.Errorf("callback left the sign-in cookie %+v; want it cleared", cleared)
	}
	form := c.platform.tokenForms[0]
	for name, want := range map[string]string{
		"grant_type": "authorization_code", "code": code, "client_id": "app:" + appID,
		"redirect_uri": "https://" + signInHost + "/.mitstack/callback",
	} {
		if got := form.Get(name); got != want {
			t.Errorf("token request %s = %q; want %q", name, got, want)
		}
	}
	wantMember(t, v.check(signInHost, http.MethodPost, "/plan"), "member")
}

func TestStart_ReturnsOnlyToAPathOnTheSameHostname(t *testing.T) {
	for _, ret := range []string{"//evil.example", "/\\evil.example", "https://evil.example/", "evil", "", "/plan\r\nX: y"} {
		t.Run(ret, func(t *testing.T) {
			v := newCompanion(t).visitor("198.51.100.7")
			res := v.do(http.MethodGet, signInHost, "/.mitstack/start?"+url.Values{"return": {ret}}.Encode(), nil)

			_, res = v.authorize(res)

			if loc := res.Header.Get("Location"); res.StatusCode != http.StatusFound || loc != "/" {
				t.Errorf("sign-in asked to return to %q answered %d with Location %q; want 302 to /", ret, res.StatusCode, loc)
			}
		})
	}
}

func TestCallback_RefusesAStateThatIsNotThisBrowsersSignIn(t *testing.T) {
	c := newCompanion(t)
	attacker, yael := c.visitor("203.0.113.66"), c.visitor("198.51.100.7")
	started := attacker.do(http.MethodGet, signInHost, "/.mitstack/start?return=%2F", nil)
	to, _ := url.Parse(started.Header.Get("Location"))
	forged := url.Values{"code": {code}, "state": {to.Query().Get("state")}}
	yael.do(http.MethodGet, signInHost, "/.mitstack/start?return=%2F", nil)

	for name, v := range map[string]*visitor{"another browser's state": yael, "no sign-in cookie": c.visitor("198.51.100.8")} {
		res := v.do(http.MethodGet, signInHost, "/.mitstack/callback?"+forged.Encode(), nil)

		if res.StatusCode != http.StatusBadRequest || cookie(res, signin.SessionCookie) != nil {
			t.Errorf("callback with %s answered %d; want 400 and no app session", name, res.StatusCode)
		}
	}
	if tokens, _ := c.platform.calls(); tokens != 0 {
		t.Errorf("a refused callback redeemed %d codes; want none", tokens)
	}
}

func TestCallback_RefusesASignInCookieOlderThanTenMinutes(t *testing.T) {
	c := newCompanion(t)
	v := c.visitor("198.51.100.7")
	res := v.do(http.MethodGet, signInHost, "/.mitstack/start?return=%2F", nil)
	c.now = c.now.Add(10*time.Minute + time.Second)

	_, res = v.authorize(res)

	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("callback after ten minutes answered %d; want 400", res.StatusCode)
	}
}

func TestCallback_ShowsAnErrorWithNoSessionWhenMitstackRefusesOrIsDown(t *testing.T) {
	for _, tc := range []struct {
		name        string
		token, info int
		want        int
	}{
		{"the code is refused", http.StatusBadRequest, http.StatusOK, http.StatusBadRequest},
		{"the token route is limited", http.StatusTooManyRequests, http.StatusOK, http.StatusServiceUnavailable},
		{"the token route is down", http.StatusBadGateway, http.StatusOK, http.StatusServiceUnavailable},
		{"userinfo refuses the token", http.StatusOK, http.StatusUnauthorized, http.StatusForbidden},
		{"userinfo is down", http.StatusOK, http.StatusServiceUnavailable, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCompanion(t)
			c.platform.set(func(p *platform) { p.tokenStatus, p.userinfoStatus = tc.token, tc.info })
			v := c.visitor("198.51.100.7")

			_, res := v.authorize(v.do(http.MethodGet, signInHost, "/.mitstack/start?return=%2F", nil))

			if res.StatusCode != tc.want || cookie(res, signin.SessionCookie) != nil {
				t.Errorf("callback answered %d; want %d and no app session", res.StatusCode, tc.want)
			}
		})
	}
}

func TestCallback_RefusesUserinfoAboutAnotherApp(t *testing.T) {
	c := newCompanion(t)
	c.platform.set(func(p *platform) { p.userinfoApp = "app_other" })
	v := c.visitor("198.51.100.7")

	_, res := v.authorize(v.do(http.MethodGet, signInHost, "/.mitstack/start?return=%2F", nil))

	if res.StatusCode != http.StatusForbidden || cookie(res, signin.SessionCookie) != nil {
		t.Errorf("callback answered %d; want 403 and no app session", res.StatusCode)
	}
}

func TestCheck_RechecksMembershipOnlyOnceFiveMinutesHavePassed(t *testing.T) {
	c := newCompanion(t)
	v := c.visitor("198.51.100.7")
	v.signIn(signInHost, "/")
	_, signedIn := c.platform.calls()

	c.now = c.now.Add(5*time.Minute - time.Second)
	wantMember(t, v.check(signInHost, http.MethodGet, "/"), "member")
	if _, calls := c.platform.calls(); calls != signedIn {
		t.Errorf("a check within five minutes asked userinfo %d times; want none", calls-signedIn)
	}

	c.platform.set(func(p *platform) { p.role = "admin" })
	c.now = c.now.Add(time.Second)
	wantMember(t, v.check(signInHost, http.MethodGet, "/"), "admin")
	wantMember(t, v.check(signInHost, http.MethodGet, "/"), "admin")
	if _, calls := c.platform.calls(); calls != signedIn+1 {
		t.Errorf("checks after five minutes asked userinfo %d times; want once", calls-signedIn)
	}
}

func TestCheck_EndsTheSessionOfAMemberRemovedFromTheOrg(t *testing.T) {
	c := newCompanion(t)
	v := c.visitor("198.51.100.7")
	v.signIn(signInHost, "/")
	session := v.jar[signInHost][signin.SessionCookie]
	c.platform.set(func(p *platform) { p.userinfoStatus = http.StatusUnauthorized })
	c.now = c.now.Add(5 * time.Minute)

	res := v.check(signInHost, http.MethodGet, "/plan")

	wantRedirect(t, res, "/.mitstack/start?return=%2Fplan")
	if cleared := cookie(res, signin.SessionCookie); cleared == nil || cleared.MaxAge >= 0 {
		t.Errorf("check after removal left the app session %+v; want it cleared", cleared)
	}
	c.platform.set(func(p *platform) { p.userinfoStatus = http.StatusOK })
	v.jar[signInHost][signin.SessionCookie] = session
	if res := v.check(signInHost, http.MethodPost, "/plan"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("the removed member's kept session answered %d; want 401", res.StatusCode)
	}
}

func TestCheck_FailsClosedWhenTheRecheckIsDueAndMitstackDoesNotAnswer(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway} {
		c := newCompanion(t)
		v := c.visitor("198.51.100.7")
		v.signIn(signInHost, "/")
		c.platform.set(func(p *platform) { p.userinfoStatus = status })
		c.now = c.now.Add(5 * time.Minute)

		if res := v.check(signInHost, http.MethodGet, "/"); res.StatusCode != http.StatusServiceUnavailable || res.Header.Get("X-Mitstack-User") != "" {
			t.Errorf("check with userinfo answering %d answered %d; want 503 and no member", status, res.StatusCode)
		}
		c.platform.set(func(p *platform) { p.userinfoStatus = http.StatusOK })
		wantMember(t, v.check(signInHost, http.MethodGet, "/"), "member")
	}
	c := newCompanion(t)
	v := c.visitor("198.51.100.7")
	v.signIn(signInHost, "/")
	c.platform.Close()
	c.now = c.now.Add(5 * time.Minute)
	if res := v.check(signInHost, http.MethodGet, "/"); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("check with mitstack unreachable answered %d; want 503", res.StatusCode)
	}
}

func TestCheck_SendsTheVisitorToSignInOnceTheAppTokenExpires(t *testing.T) {
	c := newCompanion(t)
	v := c.visitor("198.51.100.7")
	v.signIn(signInHost, "/")
	c.now = c.now.Add(12 * time.Hour)

	wantRedirect(t, v.check(signInHost, http.MethodGet, "/"), "/.mitstack/start?return=%2F")
}

func TestCheck_RefusesASessionSealedForAnotherAppOrAltered(t *testing.T) {
	c := newCompanion(t)
	v := c.visitor("198.51.100.7")
	v.signIn(signInHost, "/")
	session := v.jar[signInHost][signin.SessionCookie]

	other := c.visitor("198.51.100.7")
	other.app = "app_other"
	other.jar[signInHost] = map[string]string{signin.SessionCookie: session}
	wantRedirect(t, other.check(signInHost, http.MethodGet, "/"), "/.mitstack/start?return=%2F")

	altered := []byte(session)
	altered[len(altered)/2] ^= 1
	v.jar[signInHost][signin.SessionCookie] = string(altered)
	wantRedirect(t, v.check(signInHost, http.MethodGet, "/"), "/.mitstack/start?return=%2F")
}

func TestSignIn_OnACustomHostnameHandsTheSessionOverWithATicketUsedOnce(t *testing.T) {
	c := newCompanion(t)
	v := c.visitor("198.51.100.7")

	res := v.check(customHost, http.MethodGet, "/cart")
	wantRedirect(t, res, "/.mitstack/start?return=%2Fcart")
	_, res = v.follow(customHost, res)
	if to, _ := url.Parse(res.Header.Get("Location")); res.StatusCode != http.StatusFound || to.Host != signInHost || to.Path != "/.mitstack/start" || strings.Contains(to.RawQuery, "cart") {
		t.Fatalf("start on %s answered %d with Location %q; want 302 to %s's start with a sealed return address", customHost, res.StatusCode, res.Header.Get("Location"), signInHost)
	}
	_, res = v.follow(customHost, res)
	_, res = v.authorize(res)
	handoff, _ := url.Parse(res.Header.Get("Location"))
	if res.StatusCode != http.StatusFound || handoff.Host != customHost || handoff.Path != "/.mitstack/handoff" {
		t.Fatalf("callback answered %d with Location %q; want 302 to %s's handoff", res.StatusCode, handoff, customHost)
	}
	host, res := v.follow(signInHost, res)

	wantRedirect(t, res, "/cart")
	if host != customHost || cookie(res, signin.SessionCookie) == nil {
		t.Fatalf("handoff on %s set no app session", host)
	}
	wantMember(t, v.check(customHost, http.MethodGet, "/cart"), "member")
	wantMember(t, v.check(signInHost, http.MethodGet, "/"), "member")
	if res := c.visitor("203.0.113.66").do(http.MethodGet, customHost, handoff.RequestURI(), nil); res.StatusCode != http.StatusBadRequest || cookie(res, signin.SessionCookie) != nil {
		t.Errorf("a second use of the ticket answered %d; want 400 and no app session", res.StatusCode)
	}
}

func TestHandoff_RefusesATicketPastSixtySecondsOrForAnotherHostname(t *testing.T) {
	for _, tc := range []struct {
		name string
		wait time.Duration
		host string
	}{
		{"past sixty seconds", 61 * time.Second, customHost},
		{"at another hostname", 0, signInHost},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCompanion(t)
			v := c.visitor("198.51.100.7")
			_, res := v.follow(customHost, v.check(customHost, http.MethodGet, "/cart"))
			_, res = v.follow(customHost, res)
			_, res = v.authorize(res)
			handoff, _ := url.Parse(res.Header.Get("Location"))
			c.now = c.now.Add(tc.wait)

			res = c.visitor("198.51.100.7").do(http.MethodGet, tc.host, handoff.RequestURI(), nil)

			if res.StatusCode != http.StatusBadRequest || cookie(res, signin.SessionCookie) != nil {
				t.Errorf("handoff answered %d; want 400 and no app session", res.StatusCode)
			}
		})
	}
}

func TestStart_RefusesAReturnHostnameTheAppDoesNotServe(t *testing.T) {
	c := newCompanion(t)
	r := httptest.NewRequest(http.MethodGet, "/.mitstack/start?return=%2Fcart", nil)
	r.Host = "evil.example"
	r.Header.Set(signin.AppHeader, appID)
	r.Header.Set(signin.SignInHostHeader, signInHost)
	r.Header.Set(signin.HostsHeader, hosts)
	w := httptest.NewRecorder()

	c.handler.ServeHTTP(w, r)

	if w.Code == http.StatusFound {
		t.Errorf("start on a hostname outside the route answered 302 to %q; want a refusal", w.Header().Get("Location"))
	}
}

func TestSignOut_ClearsTheAppSessionAndTheCompanionForgetsIt(t *testing.T) {
	c := newCompanion(t)
	v := c.visitor("198.51.100.7")
	v.signIn(signInHost, "/")
	session := v.jar[signInHost][signin.SessionCookie]

	res := v.do(http.MethodPost, signInHost, "/.mitstack/sign-out", nil)

	if cleared := cookie(res, signin.SessionCookie); res.StatusCode != http.StatusOK || cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("sign-out answered %d with app session %+v; want 200 and the session cleared", res.StatusCode, cleared)
	}
	v.jar[signInHost][signin.SessionCookie] = session
	wantRedirect(t, v.check(signInHost, http.MethodGet, "/"), "/.mitstack/start?return=%2F")
}

func TestStart_LimitsSignInsPerVisitorAddress(t *testing.T) {
	c := newCompanion(t)
	stranger, member := c.visitor("203.0.113.66"), c.visitor("198.51.100.7")
	for i := 0; i < signin.SignInsPerMinute; i++ {
		if res := stranger.do(http.MethodGet, signInHost, "/.mitstack/callback?code=junk&state=junk", nil); res.StatusCode == http.StatusTooManyRequests {
			t.Fatalf("request %d from one address answered 429; want the first %d through", i+1, signin.SignInsPerMinute)
		}
	}

	if res := stranger.do(http.MethodGet, signInHost, "/.mitstack/start?return=%2F", nil); res.StatusCode != http.StatusTooManyRequests {
		t.Errorf("sign-in request %d from one address answered %d; want 429", signin.SignInsPerMinute+1, res.StatusCode)
	}
	if res := member.do(http.MethodGet, signInHost, "/.mitstack/start?return=%2F", nil); res.StatusCode != http.StatusFound {
		t.Errorf("a sign-in from another address answered %d; want 302", res.StatusCode)
	}
	c.now = c.now.Add(time.Minute)
	if res := stranger.do(http.MethodGet, signInHost, "/.mitstack/start?return=%2F", nil); res.StatusCode != http.StatusFound {
		t.Errorf("a sign-in a minute later answered %d; want 302", res.StatusCode)
	}
}

func TestCompanion_RefusesARequestTheRouteDidNotDescribe(t *testing.T) {
	c := newCompanion(t)
	for name, header := range map[string]http.Header{
		"no app":              {signin.SignInHostHeader: {signInHost}, signin.HostsHeader: {hosts}},
		"a host it is not on": {signin.AppHeader: {appID}, signin.SignInHostHeader: {signInHost}, signin.HostsHeader: {customHost}},
		"no hosts":            {signin.AppHeader: {appID}, signin.SignInHostHeader: {signInHost}},
	} {
		r := httptest.NewRequest(http.MethodGet, "/.mitstack/check", nil)
		r.Host = signInHost
		r.Header = header
		r.Header.Set("X-Forwarded-Method", http.MethodGet)
		r.Header.Set("X-Forwarded-Uri", "/")
		w := httptest.NewRecorder()

		c.handler.ServeHTTP(w, r)

		if w.Code != http.StatusInternalServerError {
			t.Errorf("check with %s answered %d; want 500", name, w.Code)
		}
	}
}

func TestCompanion_AnswersNothingItKeepsInACache(t *testing.T) {
	v := newCompanion(t).visitor("198.51.100.7")
	for _, target := range []string{"/.mitstack/check", "/.mitstack/start?return=%2F", "/.mitstack/sign-out", "/.mitstack/nothing"} {
		if res := v.do(http.MethodGet, signInHost, target, http.Header{"X-Forwarded-Method": {"GET"}, "X-Forwarded-Uri": {"/"}}); res.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s answered Cache-Control %q; want no-store", target, res.Header.Get("Cache-Control"))
		}
	}
}

func TestLoadKey_CreatesAKeyOnFirstStartAndReadsTheSameOneAfter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")

	first, err := signin.LoadKey(path)
	if err != nil || len(first) != 32 {
		t.Fatalf("LoadKey on first start = %d bytes, %v; want 32", len(first), err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode %v; want 0600", info.Mode().Perm())
	}
	second, err := signin.LoadKey(path)
	if err != nil || string(second) != string(first) {
		t.Errorf("LoadKey after = %x, %v; want the key it created", second, err)
	}
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := signin.LoadKey(path); err == nil {
		t.Errorf("LoadKey of a 5-byte key file succeeded; want a refusal")
	}
}

func TestNew_RefusesAKeyThatIsNot32Bytes(t *testing.T) {
	if _, err := signin.New(signin.Config{Platform: "https://api.mitstack.dev", Key: make([]byte, 16)}); err == nil {
		t.Errorf("New with a 16-byte key succeeded; want a refusal")
	}
}

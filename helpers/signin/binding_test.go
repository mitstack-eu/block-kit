package signin_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/helpers/signin"
)

// restart replaces the companion's handler with a new one on the same key and ended-sessions file, as a new process.
func (c *companion) restart(ended string) {
	c.t.Helper()
	handler, err := signin.New(signin.Config{
		Platform: c.platform.URL,
		Key:      make([]byte, 32),
		Client:   c.platform.Client(),
		Now:      func() time.Time { return c.now },
		Ended:    ended,
	})
	if err != nil {
		c.t.Fatalf("New: %v", err)
	}
	c.handler = handler
}

func TestSignOut_EndsTheSessionForGoodAcrossARestart(t *testing.T) {
	c := newCompanion(t)
	ended := filepath.Join(t.TempDir(), "ended")
	c.restart(ended)
	v := c.visitor("198.51.100.7")
	v.signIn(signInHost, "/")
	session := v.jar[signInHost][signin.SessionCookie]
	v.do(http.MethodPost, signInHost, "/.mitstack/sign-out", nil)

	c.restart(ended)
	c.now = c.now.Add(5 * time.Minute)
	v.jar[signInHost][signin.SessionCookie] = session

	wantRedirect(t, v.check(signInHost, http.MethodGet, "/"), "/.mitstack/start?return=%2F")
	if res := v.check(signInHost, http.MethodPost, "/"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a signed-out session after a restart answered %d; want 401", res.StatusCode)
	}
}

func TestSignOut_SignsOutOnlyOnAPost(t *testing.T) {
	c := newCompanion(t)
	v := c.visitor("198.51.100.7")
	v.signIn(signInHost, "/")

	res := v.do(http.MethodGet, signInHost, "/.mitstack/sign-out", nil)

	if res.StatusCode != http.StatusOK || cookie(res, signin.SessionCookie) != nil {
		t.Errorf("a GET of sign-out answered %d and set %+v; want 200 with a form and the session kept", res.StatusCode, cookie(res, signin.SessionCookie))
	}
	wantMember(t, v.check(signInHost, http.MethodGet, "/"), "member")
}

// handoffLink runs a sign-in that starts on the custom hostname as far as the handoff link the callback returns.
func handoffLink(t *testing.T, v *visitor) *url.URL {
	t.Helper()
	_, res := v.follow(customHost, v.check(customHost, http.MethodGet, "/cart"))
	_, res = v.follow(customHost, res)
	_, res = v.authorize(res)
	link, _ := url.Parse(res.Header.Get("Location"))
	if res.StatusCode != http.StatusFound || link.Host != customHost || link.Path != "/.mitstack/handoff" {
		t.Fatalf("callback answered %d with Location %q; want 302 to %s's handoff", res.StatusCode, link, customHost)
	}
	return link
}

func TestHandoff_RefusesATicketInABrowserThatDidNotStartTheSignIn(t *testing.T) {
	c := newCompanion(t)
	member, victim := c.visitor("198.51.100.7"), c.visitor("203.0.113.66")
	link := handoffLink(t, member)

	res := victim.do(http.MethodGet, customHost, link.RequestURI(), nil)

	if res.StatusCode != http.StatusBadRequest || cookie(res, signin.SessionCookie) != nil {
		t.Errorf("another browser's first use of the ticket answered %d; want 400 and no app session", res.StatusCode)
	}
	if res := member.do(http.MethodGet, customHost, link.RequestURI(), nil); res.StatusCode != http.StatusFound || cookie(res, signin.SessionCookie) == nil {
		t.Errorf("the browser that started the sign-in answered %d; want 302 with an app session", res.StatusCode)
	}
}

func TestStart_LimitsSignInsPerAddressWhateverPortTheyComeFrom(t *testing.T) {
	c := newCompanion(t)
	limited := false
	for i := 0; i <= signin.SignInsPerMinute; i++ {
		r := httptest.NewRequest(http.MethodGet, "/.mitstack/start?return=%2F", nil)
		r.Host = signInHost
		r.RemoteAddr = "198.51.100.9:" + strings.Repeat("1", 1+i%4)
		r.Header.Set(signin.AppHeader, appID)
		r.Header.Set(signin.SignInHostHeader, signInHost)
		r.Header.Set(signin.HostsHeader, hosts)
		w := httptest.NewRecorder()
		c.handler.ServeHTTP(w, r)
		limited = w.Code == http.StatusTooManyRequests
	}

	if !limited {
		t.Errorf("sign-in request %d from one address on several ports was let through; want 429", signin.SignInsPerMinute+1)
	}
}

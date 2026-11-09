package dockerhost

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/mitstack-eu/block-kit/helpers/signin"
)

// SignIn opens a route only to its app's org members: SignInHostname is the app's apps-zone hostname mitstack returns
// a member to, and Hostnames every hostname the app's route serves, the only ones a sign-in returns to.
type SignIn struct {
	AppID          string
	SignInHostname string
	Hostnames      []string
}

var appIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (e Edge) signInSite(r Route) string {
	told := []string{
		"header_up " + signin.AppHeader + " " + r.SignIn.AppID,
		"header_up " + signin.SignInHostHeader + " " + r.SignIn.SignInHostname,
		"header_up " + signin.HostsHeader + ` "` + strings.Join(r.SignIn.Hostnames, " ") + `"`,
	}
	directives := e.encoding()
	for _, name := range signin.MemberHeaders {
		directives = append(directives, "request_header -"+name)
	}
	ask := append(append([]string{"uri /.mitstack/check"}, told...), "copy_headers "+strings.Join(signin.MemberHeaders, " "))
	directives = append(directives,
		nest("handle /.mitstack/* {", nest("reverse_proxy "+signin.Upstream+" {", told...)),
		nest("handle {", nest("forward_auth "+signin.Upstream+" {", ask...), "reverse_proxy "+r.Upstream),
	)
	return block(r.Hostname+" {", directives...)
}

func nest(opening string, directives ...string) string {
	return strings.ReplaceAll(strings.TrimSuffix(block(opening, directives...), "\n"), "\n", "\n  ")
}

func checkSignIn(r Route) error {
	s := r.SignIn
	if !appIDRe.MatchString(s.AppID) {
		return fmt.Errorf("edge: app id %q is not 1 to 64 letters, digits, - or _", s.AppID)
	}
	for _, h := range s.Hostnames {
		if !hostnameRe.MatchString(h) {
			return fmt.Errorf("edge: sign-in hostname %q is not a lowercase hostname of two or more labels", h)
		}
	}
	if !slices.Contains(s.Hostnames, r.Hostname) || !slices.Contains(s.Hostnames, s.SignInHostname) {
		return fmt.Errorf("edge: sign-in hostnames %v hold neither or not both of the route's %s and the sign-in hostname %q",
			s.Hostnames, r.Hostname, s.SignInHostname)
	}
	return nil
}

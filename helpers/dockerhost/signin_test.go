package dockerhost_test

import (
	"os"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
)

var costs = dockerhost.Route{
	Hostname: "costs.apps.mitstack.dev",
	Upstream: "costs-app:8080",
	SignIn: &dockerhost.SignIn{
		AppID:          "app_8d21b0c4",
		SignInHostname: "costs.apps.mitstack.dev",
		Hostnames:      []string{"costs.apps.mitstack.dev", "shop.example.com"},
	},
}

func TestSite_WithSignInDeletesTheMemberHeadersAndAsksTheCompanionFirst(t *testing.T) {
	want, err := os.ReadFile("testdata/sign-in.caddy")
	if err != nil {
		t.Fatal(err)
	}
	edge := dockerhost.Edge{PublicIP: publicIP, Compress: true, RouteDir: routeDir}

	site, err := edge.Site(costs)

	if err != nil || site != string(want) {
		t.Errorf("Site(%+v) = %v\n%s\nwant testdata/sign-in.caddy:\n%s", costs, err, site, want)
	}
}

func TestSite_WithSignInOffRendersTheRouteItRendersToday(t *testing.T) {
	edge := dockerhost.Edge{PublicIP: publicIP, Compress: true, RouteDir: routeDir}
	off := costs
	off.SignIn = nil

	site, err := edge.Site(off)

	if want := "costs.apps.mitstack.dev {\n  encode zstd gzip\n  reverse_proxy costs-app:8080\n}\n"; err != nil || site != want {
		t.Errorf("Site(%+v) = %q, %v; want %q", off, site, err, want)
	}
}

func TestCaddyfile_RendersASignInRouteAsSiteDoes(t *testing.T) {
	edge := dockerhost.Edge{PublicIP: publicIP, Routes: []dockerhost.Route{costs}, Compress: true}
	site, _ := edge.Site(costs)

	file, err := edge.Caddyfile()

	if err != nil || !strings.Contains(file.Content, site) {
		t.Errorf("Caddyfile(%+v) = %v\n%s\nwant it to hold the route's site:\n%s", edge, err, file.Content, site)
	}
}

func TestSite_RefusesASignInThatCouldBreakOutOfItsFileOrSendVisitorsElsewhere(t *testing.T) {
	edge := dockerhost.Edge{PublicIP: publicIP, RouteDir: routeDir}
	for name, change := range map[string]func(s *dockerhost.SignIn){
		"no app id":                            func(s *dockerhost.SignIn) { s.AppID = "" },
		"an app id with a space":               func(s *dockerhost.SignIn) { s.AppID = "app 1" },
		"an app id with a newline":             func(s *dockerhost.SignIn) { s.AppID = "app_1\n  respond 200" },
		"an app id with a quote":               func(s *dockerhost.SignIn) { s.AppID = `app_1"` },
		"no hostnames":                         func(s *dockerhost.SignIn) { s.Hostnames = nil },
		"a hostname with a quote":              func(s *dockerhost.SignIn) { s.Hostnames = append(s.Hostnames, `x.example.com"`) },
		"a hostname with a brace":              func(s *dockerhost.SignIn) { s.Hostnames = append(s.Hostnames, "x.example.com {") },
		"a sign-in hostname it does not serve": func(s *dockerhost.SignIn) { s.SignInHostname = "other.apps.mitstack.dev" },
		"hostnames without the route's own": func(s *dockerhost.SignIn) {
			s.Hostnames = []string{"costs.apps.mitstack.dev"}
			s.SignInHostname = "costs.apps.mitstack.dev"
		},
	} {
		t.Run(name, func(t *testing.T) {
			signIn := *costs.SignIn
			signIn.Hostnames = append([]string(nil), signIn.Hostnames...)
			route := dockerhost.Route{Hostname: "shop.example.com", Upstream: costs.Upstream, SignIn: &signIn}
			change(&signIn)

			if site, err := edge.Site(route); err == nil {
				t.Errorf("Site(%+v) = %q with no error; want a refusal", route, site)
			}
		})
	}
}

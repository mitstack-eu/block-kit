package dockerhost_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
)

const routeDir = "/opt/app/routes"

func TestCaddyfile_CompressesEverySiteImportsTheRouteDirectoryAndAnswersTheIPWithNoUpstream(t *testing.T) {
	edge := dockerhost.Edge{PublicIP: publicIP, Routes: []dockerhost.Route{shop}, Compress: true, RouteDir: routeDir}
	want, err := os.ReadFile(filepath.Join("testdata", "compressed-route-dir.Caddyfile"))
	if err != nil {
		t.Fatalf("reading the golden Caddyfile: %v", err)
	}

	file, err := edge.Caddyfile()

	if err != nil {
		t.Fatalf("Caddyfile(%+v): %v", edge, err)
	}
	if file.Content != string(want) {
		t.Errorf("Caddyfile(%+v) =\n%s\nwant testdata/compressed-route-dir.Caddyfile:\n%s", edge, file.Content, want)
	}
}

func TestSite_RendersOneRouteAsAFileTheRouteDirectoryHolds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		compress bool
		want     string
	}{
		{"without compression", false, "shop.apps.example.com {\n  reverse_proxy shop-app:8080\n}\n"},
		{"with compression", true, "shop.apps.example.com {\n  encode zstd gzip\n  reverse_proxy shop-app:8080\n}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edge := dockerhost.Edge{PublicIP: publicIP, Compress: tc.compress, RouteDir: routeDir}

			site, err := edge.Site(shop)

			if err != nil || site != tc.want {
				t.Errorf("Site(%+v) = %q, %v; want %q", shop, site, err, tc.want)
			}
		})
	}
}

func TestSite_RefusesARouteThatCouldBreakOutOfItsFile(t *testing.T) {
	edge := dockerhost.Edge{PublicIP: publicIP, RouteDir: routeDir}
	for _, route := range []dockerhost.Route{
		{Hostname: "shop.example.com {", Upstream: shop.Upstream},
		{Hostname: "shop.example.com\nevil.example.com", Upstream: shop.Upstream},
		{Hostname: shop.Hostname, Upstream: "shop-app:8080\n  respond 200"},
	} {
		if site, err := edge.Site(route); err == nil {
			t.Errorf("Site(%+v) = %q with no error; want a refusal", route, site)
		}
	}
}

func TestCaddyfile_RefusesARouteDirectoryThatCouldBreakOutOfTheCaddyfile(t *testing.T) {
	for _, dir := range []string{"routes", "/opt/app/routes {", "/opt/app/routes\nrespond 200", "/opt/app/../routes", "/opt/app/routes/"} {
		edge := dockerhost.Edge{PublicIP: publicIP, IPUpstream: shop.Upstream, RouteDir: dir}

		if file, err := edge.Caddyfile(); err == nil {
			t.Errorf("Caddyfile(%+v) = %q with no error; want a refusal", edge, file.Content)
		}
	}
}

func TestSetup_OfAnEdgeWithARouteDirectoryMountsItIntoTheEdgeReadOnly(t *testing.T) {
	edge := dockerhost.Edge{PublicIP: publicIP, RouteDir: routeDir}
	mounts := "-v /opt/app/Caddyfile:/etc/caddy/Caddyfile:ro"
	want := strings.Replace(dockerhost.Setup().Content, mounts, mounts+" -v "+routeDir+":/etc/caddy/routes:ro", 1)

	setup := edge.Setup()

	if setup.Content != want || setup.Path != dockerhost.Setup().Path || setup.Mode != dockerhost.Setup().Mode {
		t.Errorf("Setup() of %+v =\n%s\nwant the package's setup with the route directory mounted:\n%s", edge, setup.Content, want)
	}
	if plain := (dockerhost.Edge{PublicIP: publicIP}).Setup(); plain != dockerhost.Setup() {
		t.Errorf("Setup() of an edge with no route directory differs from the package's Setup():\n%s", plain.Content)
	}
}

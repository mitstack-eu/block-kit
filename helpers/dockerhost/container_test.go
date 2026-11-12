package dockerhost_test

// A container a Docker host reports reads as one answer for every reader: unhealthy, naming only the container and what it reads, while it is not running or its health reads unhealthy, waiting while it starts, and healthy once it runs healthy.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
)

var appCauses = dockerhost.ContainerCauses{NotRunning: "the app's container is not running on the host", Unhealthy: "the app's container reads unhealthy on the host"}

func TestContainer_ReadsItsStateAndHealthAsOneAnswer(t *testing.T) {
	logs := "2026-11-12T12:01:02Z starting the app\n2026-11-12T12:01:03Z NEEDED_KEY is not set\n"
	for _, tc := range []struct {
		name, state, health string
		reading             dockerhost.ContainerReading
		want                *contract.Unhealthy
	}{
		{"restarting reads not running, naming the container and its state alone", "restarting", "unhealthy", dockerhost.ContainerNotRunning,
			&contract.Unhealthy{Cause: appCauses.NotRunning, Detail: "shop-web reads restarting"}},
		{"exited reads not running", "exited", "none", dockerhost.ContainerNotRunning, &contract.Unhealthy{Cause: appCauses.NotRunning, Detail: "shop-web reads exited"}},
		{"dead reads not running", "dead", "", dockerhost.ContainerNotRunning, &contract.Unhealthy{Cause: appCauses.NotRunning, Detail: "shop-web reads dead"}},
		{"running with its health unhealthy reads unhealthy, naming the health alone", "running", "unhealthy", dockerhost.ContainerUnhealthy,
			&contract.Unhealthy{Cause: appCauses.Unhealthy, Detail: "shop-web reads unhealthy"}},
		{"running healthy reads healthy", "running", "healthy", dockerhost.ContainerHealthy, nil},
		{"running with its health still starting, as a new image during a deploy, reads neither", "running", "starting", dockerhost.ContainerStarting, nil},
		{"running with no health check reads by its state alone", "running", "none", dockerhost.ContainerHealthy, nil},
		{"running on a host whose report carries no health reads by its state alone", "running", "", dockerhost.ContainerHealthy, nil},
		{"created, as compose makes a new container, reads neither", "created", "", dockerhost.ContainerStarting, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := dockerhost.Container{Name: "shop-web", State: tc.state, Health: tc.health, Logs: &logs}

			reading, unhealthy := c.Read(), c.Unhealthy(appCauses)

			if reading != tc.reading || reading.Failed() != (tc.want != nil) || !reflect.DeepEqual(unhealthy, tc.want) {
				t.Errorf("Container{%s, %s}: Read = %v, Failed = %v, Unhealthy = %+v; want %v, %v, %+v",
					tc.state, tc.health, reading, reading.Failed(), unhealthy, tc.reading, tc.want != nil, tc.want)
			}
		})
	}
}

func appReport(t *testing.T, containers ...string) dockerhost.Report {
	t.Helper()
	return newest(t, received(1, `{"host":{"apps":{"shop":[`+strings.Join(containers, ",")+`]}}}`))
}

func shopWeb(sub, state, health string) string {
	return `{"container":"shop-web","subscription":"` + sub + `","state":"` + state + `","health":"` + health + `","logs":"12:01:03 NEEDED_KEY is not set\n"}`
}

func TestReport_AppHealthJudgesTheSubscriptionsOwnContainers(t *testing.T) {
	down := &contract.Unhealthy{Cause: appCauses.NotRunning, Detail: "shop-web reads restarting"}
	const starting = "shop-web is still starting, so whether it serves cannot be read until its health check passes"
	for _, tc := range []struct {
		name       string
		containers []string
		unhealthy  *contract.Unhealthy
		waits      string
	}{
		{"its restarting container names it", []string{shopWeb("sub_shop", "restarting", "unhealthy")}, down, ""},
		{"its container still starting waits, so the verdict before it stands", []string{shopWeb("sub_shop", "running", "starting")}, nil, starting},
		{"its running, healthy container reads healthy", []string{shopWeb("sub_shop", "running", "healthy")}, nil, ""},
		{"its running container with no health check reads healthy and never waits", []string{shopWeb("sub_shop", "running", "none")}, nil, ""},
		{"another subscription's restarting container leaves it healthy", []string{shopWeb("sub_shop", "running", "healthy"), shopWeb("sub_db_shop", "restarting", "unhealthy")}, nil, ""},
		{"an app the report lists no container of reads healthy, as before its first deploy", nil, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unhealthy, waits, err := appReport(t, tc.containers...).AppHealth("shop", "sub_shop", appCauses)

			if err != nil || !reflect.DeepEqual(unhealthy, tc.unhealthy) || waits != tc.waits {
				t.Errorf("AppHealth = %+v, %q, %v; want %+v, %q", unhealthy, waits, err, tc.unhealthy, tc.waits)
			}
		})
	}
}

func TestReport_AppHealthOfACrashLoopNamesOneUnchangedFailureAndASavedKeyClearsIt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		states [][2]string
		last   *contract.Unhealthy
	}{
		{"a crash loop alternating restarting and starting", [][2]string{{"restarting", "unhealthy"}, {"running", "starting"}, {"restarting", "starting"}, {"running", "starting"}, {"restarting", "unhealthy"}},
			&contract.Unhealthy{Cause: appCauses.NotRunning, Detail: "shop-web reads restarting"}},
		{"a first healthy start after the key is saved", [][2]string{{"restarting", "unhealthy"}, {"running", "starting"}, {"running", "healthy"}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var failures []contract.Unhealthy
			var answer *contract.Unhealthy
			for _, s := range tc.states {
				unhealthy, waits, err := appReport(t, shopWeb("sub_shop", s[0], s[1])).AppHealth("shop", "sub_shop", appCauses)
				if err != nil || (unhealthy == nil && waits == "" && s[1] != "healthy") {
					t.Fatalf("AppHealth(%v) = %+v, %q, %v; want a failure or a wait, never healthy", s, unhealthy, waits, err)
				}
				if unhealthy != nil {
					failures = append(failures, *unhealthy)
				}
				if waits == "" {
					answer = unhealthy
				}
			}

			if !reflect.DeepEqual(answer, tc.last) || (len(failures) > 1 && !reflect.DeepEqual(failures[0], failures[len(failures)-1])) {
				t.Errorf("the last answer that is not a wait = %+v, and the failures read %+v; want %+v, every failure the same line", answer, failures, tc.last)
			}
		})
	}
}

func TestReport_AppContainersReadsTheContainersListedUnderOneApp(t *testing.T) {
	report := newest(t, received(1, `{"host":{"apps":{
		"shop":[{"container":"shop-web","subscription":"sub_shop","image":"ghcr.io/acme/shop@sha256:ab12","digest":"sha256:ab12","state":"restarting","health":"unhealthy","logs":"boom\n"}],
		"blog":[{"container":"blog-web","subscription":"sub_blog","state":"running","health":"healthy"}]}}}`))
	logs := "boom\n"

	shop, errShop := report.AppContainers("shop")
	none, errNone := report.AppContainers("wiki")

	want := []dockerhost.Container{{Name: "shop-web", Subscription: "sub_shop", Image: "ghcr.io/acme/shop@sha256:ab12", Digest: "sha256:ab12", State: "restarting", Health: "unhealthy", Logs: &logs}}
	if errShop != nil || !reflect.DeepEqual(shop, want) || errNone != nil || len(none) != 0 {
		t.Errorf("AppContainers(shop) = %+v, %v and AppContainers(wiki) = %+v, %v; want %+v and none", shop, errShop, none, errNone, want)
	}
}

func TestReport_AppContainersFailsAListItCannotRead(t *testing.T) {
	report := newest(t, received(1, `{"host":{"apps":{"shop":"not a list"}}}`))

	_, err := report.AppContainers("shop")

	if err == nil {
		t.Error("AppContainers(shop) = nil error; want the decoding failure")
	}
}

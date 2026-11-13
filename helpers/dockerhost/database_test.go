package dockerhost_test

// A Database lands each app's database in the app's own directory on the app's own network with no published port,
// keeps its password on the host, refuses a host without room with nothing written, reads the database through the
// host's node report, and removes it only once its final backup is uploaded.

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
	"github.com/mitstack-eu/block-kit/helpers/dockerhost/dockerhosttest"
)

const (
	dbBlock    = "docker-store"
	storeImage = "example.org/store:2.1@sha256:0f9e"
)

var (
	_ handler.Resource         = dockerhost.Database{}
	_ handler.ResourceReporter = dockerhost.Database{}
	_ handler.Observer         = dockerhost.Database{}
)

func storeDB() dockerhost.Database {
	return dockerhost.Database{Block: dbBlock, Type: "example.store-database", Engine: storeEngine()}
}

func newDBEnv(t *testing.T) *dockerhosttest.Database {
	t.Helper()
	return dockerhosttest.NewDatabase(t, storeDB())
}

func storeEngine() dockerhost.Engine {
	return dockerhost.Engine{
		Service: "store", Image: storeImage, Port: "7000", DataDir: "/data",
		Entrypoint:  []string{"sh", "-c", `exec store --user=app --password="$` + dockerhost.PasswordVariable + `"`},
		Command:     []string{"serve"},
		Healthcheck: []string{"CMD", "store", "ping"},
		Dump:        "store dump --db=" + dockerhost.DatabaseName,
		Restore:     "store load --db=" + dockerhost.DatabaseName,
		Trouble:     regexp.MustCompile(`FATAL`),
	}
}

func refused(err error) string {
	var r *handler.Refused
	if errors.As(err, &r) {
		return r.Class
	}
	return ""
}

func TestDatabase_HoldsEveryDatabaseScenario(t *testing.T) {
	dockerhosttest.AssertDatabase(t, storeDB())
}

func TestDatabase_ReservesAndLimitsTheContainerToItsSizesWithNoSwapBeyondItsMemory(t *testing.T) {
	e := newDBEnv(t)

	e.Land(t, e.Desired("shop", map[string]string{"dbCpu": "250m", "dbMemory": "1Gi", "dbDisk": "2Gi"}))

	var got dockerhost.Reservation
	if err := json.Unmarshal([]byte(e.Main()["shop/reservations/"+dbBlock+".json"]), &got); err != nil || got != (dockerhost.Reservation{CPUMillis: 250, MemoryBytes: gib, DiskBytes: 2 * gib}) {
		t.Errorf("shop's reservation reads %+v, %v; want 250 millicores, 1 GiB and 2 GiB of disk", got, err)
	}
	if s := e.Compose(t, "shop").Services["store"]; s.CPUs != "0.25" || s.MemLimit != gib || s.MemswapLimit != gib {
		t.Errorf("shop's limits are %s CPUs and %d bytes, swap %d; want 0.25 and 1 GiB with no swap beyond", s.CPUs, s.MemLimit, s.MemswapLimit)
	}
}

func TestDatabase_LeavesTheAppsOwnVariablesAloneAndEscapesItsEntrypointForCompose(t *testing.T) {
	e := newDBEnv(t)
	e.Set(map[string]string{"shop/env.names": "DB_HOST=secret:db-host\n"})

	e.Land(t, e.Desired("shop", nil))

	if got := e.Main()["shop/env.names"]; got != "DB_HOST=secret:db-host\n" {
		t.Errorf("the app's own env.names reads %q after the database landed; want it as the app wrote it", got)
	}
	if got := e.Compose(t, "shop").Services["store"].Entrypoint; !slices.Equal(got, []string{"sh", "-c", `exec store --user=app --password="$$DB_PASSWORD"`}) {
		t.Errorf("the entrypoint is %q; want the engine's with each $ escaped for Compose", got)
	}
}

func TestDatabase_RefusesWhatItCannotLandAndWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name, class, says string
		params            map[string]string
		before            func(*testing.T, *dockerhosttest.Database)
		edit              func(*contract.Desired)
	}{
		{name: "a disk that does not parse", class: "invalid-param", says: `params["dbDisk"] is "lots"`, params: map[string]string{"dbDisk": "lots"}},
		{name: "memory past what the host has free", class: "host-full", says: "the host has 2.5 vCPU, 512 MiB and 69 GiB of disk free",
			params: map[string]string{"dbMemory": "1Gi"},
			before: func(_ *testing.T, e *dockerhosttest.Database) {
				e.Set(map[string]string{"blog/reservations/docker-app-slice.json": `{"cpuMillis":1000,"memoryBytes":6979321856,"diskBytes":1073741824}`})
			}},
		{name: "a host that takes no backups", class: "host-takes-no-backups", says: "the host holds no backup public key",
			before: func(_ *testing.T, e *dockerhosttest.Database) { e.BackupError = "the host holds no backup public key" }},
		{name: "a host that has sent no report", says: "no node report giving its allocatable capacity",
			before: func(_ *testing.T, e *dockerhosttest.Database) { e.Silent = true }},
		{name: "an app whose database another subscription holds", class: "app-taken", says: "belongs to the subscription sub_db_other",
			before: func(t *testing.T, e *dockerhosttest.Database) {
				other := e.Desired("shop", nil)
				other.SubscriptionID = "sub_db_other"
				e.Land(t, other)
			}},
		{name: "a request without the GitHub connection", class: "missing-github-access", says: "the GitHub connection is missing",
			edit: func(d *contract.Desired) { d.Secrets = nil }},
		{name: "a host repository outside the connection's account", class: "host-binding", says: "is not a repository of the GitHub connection's account acme",
			edit: func(d *contract.Desired) {
				d.Bindings["host"]["clusterRepoUrl"] = "https://github.com/other/docker-host"
			}},
		{name: "a host repository outside the account the request's GitHub config names", class: "host-binding", says: "is not a repository of the GitHub connection's account other-account",
			edit: func(d *contract.Desired) {
				d.Instances = map[string]contract.InstanceConfig{"github": {Config: map[string]string{"owner": "other-account"}}}
			}},
		{name: "a GitHub API base off the allowlist", class: "untrusted-api-base", says: "github api base rejected",
			edit: func(d *contract.Desired) {
				d.Instances = map[string]contract.InstanceConfig{"github": {Endpoint: "https://api.github.example.org"}}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newDBEnv(t)
			if tc.before != nil {
				tc.before(t, e)
			}
			held := e.Under("shop")
			d := e.Desired("shop", tc.params)
			if tc.edit != nil {
				tc.edit(&d)
			}

			var resource handler.Resource = e.DB
			err := resource.Ensure(context.Background(), d)

			if err == nil || refused(err) != tc.class || !strings.Contains(err.Error(), tc.says) {
				t.Errorf("Ensure = %v; want a %q refusal naming %q", err, tc.class, tc.says)
			}
			if got := e.Under("shop"); !maps.Equal(got, held) {
				t.Errorf("a refused subscribe left shop holding %v; want it as it was, %v", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(held)))
			}
		})
	}
}

func TestDatabase_LandsOnTheHostRepositoryOfAnOwnerTheRequestCarriesOnlyInTheGitHubConfig(t *testing.T) {
	e := newDBEnv(t)
	d := e.Desired("shop", nil)
	d.Secrets = map[string]map[string]string{"github": {"token": dockerhosttest.Token}}
	d.Instances = map[string]contract.InstanceConfig{"github": {Config: map[string]string{"owner": dockerhosttest.Owner}}}

	_, err := e.DB.EnsureResources(context.Background(), d)

	if err != nil {
		t.Fatalf("EnsureResources with the owner %q only in the GitHub config = %v; want the database landed on %s", dockerhosttest.Owner, err, dockerhosttest.Repo)
	}
	if _, landed := e.Main()["shop/"+dbBlock+".compose.yaml"]; !landed {
		t.Errorf("%s holds %v; want shop's compose file landed on it", dockerhosttest.Repo, slices.Sorted(maps.Keys(e.Main())))
	}
	for _, r := range e.Writes() {
		if !strings.HasPrefix(r.Path, "/repos/"+dockerhosttest.Repo+"/") {
			t.Errorf("%s %s wrote outside the host repository; want every write under /repos/%s/", r.Method, r.Path, dockerhosttest.Repo)
		}
	}
}

func TestDatabase_WaitsForTheHostsNextReportWhileItGivesNoCapacityAndWritesNothing(t *testing.T) {
	e := newDBEnv(t)
	e.Silent = true

	_, err := e.DB.EnsureResources(context.Background(), e.Desired("shop", nil))

	if !waitsForSignal(err) || !strings.Contains(err.Error(), "shop's database waits for that capacity report") {
		t.Errorf("EnsureResources on a host that has sent no report = %v; want a wait for its next report naming the capacity report", err)
	}
	if got := e.Under("shop"); len(got) != 0 {
		t.Errorf("a waiting subscribe left shop holding %v; want nothing", slices.Sorted(maps.Keys(got)))
	}
}

func TestDatabase_UpgradeKeepsTheVolumesDiskAndAnUnchangedOneWritesNothing(t *testing.T) {
	e := newDBEnv(t)
	e.Land(t, e.Desired("shop", map[string]string{"dbDisk": "2Gi"}))
	writes := len(e.Writes())

	e.Land(t, e.Desired("shop", map[string]string{"dbDisk": "2Gi"}))
	_, err := e.DB.EnsureResources(context.Background(), e.Desired("shop", map[string]string{"dbDisk": "3Gi"}))

	if got := len(e.Writes()); got != writes {
		t.Errorf("an unchanged upgrade made %d writes; want none", got-writes)
	}
	if refused(err) != "disk-fixed" || !strings.Contains(err.Error(), `keep params["dbDisk"] at 2Gi`) {
		t.Errorf("an upgrade to 3Gi = %v; want a disk-fixed refusal keeping 2Gi", err)
	}
}

func TestDatabase_GivesUpWhenMainMovesUnderEachOfThreeAttempts(t *testing.T) {
	e := newDBEnv(t)
	moves := 0
	e.BeforeWrite(func(method, _ string) error {
		if method == "PATCH" {
			moves++
			e.Set(map[string]string{"other/compose.yaml": strings.Repeat("x", moves)})
		}
		return nil
	})

	_, err := e.DB.EnsureResources(context.Background(), e.Desired("shop", nil))

	if !errors.Is(err, ghapi.ErrNotFastForward) || moves != 3 {
		t.Errorf("EnsureResources under a main moving at each commit = %v after %d attempts; want it to give up after 3", err, moves)
	}
}

func TestDatabase_UnsubscribeAsksForTheFinalBackupByItsTimeNamesAFailureAndKeepsTheAppsOwnFiles(t *testing.T) {
	ctx := context.Background()
	e := newDBEnv(t)
	e.Land(t, e.Desired("shop", nil))
	e.Set(map[string]string{"shop/compose.yaml": "services: {}\n"})
	e.Volumes["shop"] = true
	e.BackupResult = ""

	_, _ = e.DB.Delete(ctx, e.Desired("shop", nil))
	if got := strings.TrimSpace(e.Main()["shop/"+dbBlock+".backup-request"]); got != "final-1793620800" {
		t.Fatalf("the backup request reads %q; want final-1793620800, named for the clock's time", got)
	}
	e.BackupResult = "failed: the store answered 403"
	if _, err := e.DB.Delete(ctx, e.Desired("shop", nil)); err == nil || !strings.Contains(err.Error(), "the store answered 403") {
		t.Errorf("Delete after a failed final backup = %v; want a failure naming the host's reason", err)
	}
	e.BackupResult = dockerhosttest.Uploaded
	if _, err := e.DB.Delete(ctx, e.Desired("shop", nil)); err != nil || !slices.Equal(slices.Collect(maps.Keys(e.Under("shop"))), []string{"shop/compose.yaml"}) {
		t.Errorf("Delete once the backup is uploaded = %v leaving %v; want only the app's own compose file", err, slices.Collect(maps.Keys(e.Under("shop"))))
	}
}

func TestDatabase_UnsubscribeRemovesADatabaseTheHostNeverMadeAVolumeForAndLeavesAnotherSubscriptions(t *testing.T) {
	ctx := context.Background()
	e := newDBEnv(t)
	e.Land(t, e.Desired("shop", nil))
	other := e.Desired("shop", nil)
	other.SubscriptionID = "sub_db_other"

	if _, err := e.DB.Delete(ctx, other); err != nil || !slices.Equal(slices.Sorted(maps.Keys(e.Under("shop"))), landedFiles("shop")) {
		t.Errorf("another subscription's Delete = %v leaving %v; want shop's database left whole", err, slices.Sorted(maps.Keys(e.Under("shop"))))
	}
	if _, err := e.DB.Delete(ctx, e.Desired("shop", nil)); err != nil || len(e.Under("shop")) != 0 {
		t.Errorf("Delete of a database with no volume = %v leaving %v; want it removed with no backup", err, e.Under("shop"))
	}
}

func landedFiles(app string) []string {
	return slices.Sorted(slices.Values([]string{app + "/" + dbBlock + ".backup", app + "/" + dbBlock + ".compose.yaml", app + "/" + dbBlock + ".env.names", app + "/reservations/" + dbBlock + ".json"}))
}

func TestDatabase_UnsubscribeKeepsTheDatabaseWhileTheHostCannotShowAFinalBackup(t *testing.T) {
	for name, host := range map[string]func(*dockerhosttest.Host){
		"a host that has sent no report": func(h *dockerhosttest.Host) { h.Silent = true },
		"a host that takes no backups":   func(h *dockerhosttest.Host) { h.BackupError = "the host holds no backup public key" },
	} {
		t.Run(name, func(t *testing.T) {
			e := newDBEnv(t)
			e.Land(t, e.Desired("shop", nil))
			e.Volumes["shop"] = true
			host(e.Host)

			_, err := e.DB.Delete(context.Background(), e.Desired("shop", nil))

			if err == nil || !slices.Equal(slices.Sorted(maps.Keys(e.Under("shop"))), landedFiles("shop")) {
				t.Errorf("Delete = %v leaving %v; want an error or a wait with shop's database kept whole", err, slices.Sorted(maps.Keys(e.Under("shop"))))
			}
		})
	}
}

func TestDatabase_SubscriptionLogsReadsTheDatabaseContainersOwnLines(t *testing.T) {
	e := newDBEnv(t)
	e.Land(t, e.Desired("shop", nil))
	e.Containers["shop"] = map[string]any{"subscription": "sub_db_shop", "logs": "LOG: ready\n"}

	lines, err := e.DB.SubscriptionLogs(context.Background(), e.Desired("shop", nil))

	if err != nil || !slices.Equal(lines, []string{"shop-store | LOG: ready"}) {
		t.Errorf("SubscriptionLogs = %q, %v; want the container's line led by its name", lines, err)
	}
}

func TestDatabase_ObserveReadsTheDatabasePresentOnlyOnceTheHostRunsItsFilesHealthy(t *testing.T) {
	logs := "LOG: ready\nFATAL: password authentication failed for user \"app\"\n"
	for _, tc := range []struct {
		name                                 string
		land, other                          bool
		host                                 func(*dockerhosttest.Host)
		engine                               func(*dockerhost.Engine)
		state                                contract.State
		says, cause                          string
		unwell, neverActive, recorded, whole bool
	}{
		{name: "nothing landed", state: contract.Absent},
		{name: "a database another subscription holds", land: true, other: true, state: contract.Absent},
		{name: "landed and running", land: true, state: contract.Present, says: "shop-store runs"},
		{name: "a silent host", land: true, host: func(h *dockerhosttest.Host) { h.Silent = true }, state: contract.Unknown, says: "no node report"},
		{name: "a report naming no synced commit", land: true, host: func(h *dockerhosttest.Host) { h.Unsynced = true }, state: contract.Unknown,
			says: "carries no host.agent.syncedCommit"},
		{name: "an agent behind on the database's files", land: true, state: contract.Unknown, says: "whose " + dbBlock + ".env.names differs from main's head",
			host: func(h *dockerhosttest.Host) {
				synced := h.Head()
				h.Set(map[string]string{"shop/" + dbBlock + ".env.names": "DB_PASSWORD=generated:elsewhere:password\n"})
				h.Lagging = synced
			}},
		{name: "an agent behind only on another app's files", land: true, state: contract.Present, says: "shop-store runs",
			host: func(h *dockerhosttest.Host) {
				synced := h.Head()
				h.Set(map[string]string{"web/README.md": "Another app.\n"})
				h.Lagging = synced
			}},
		{name: "an agent that synced a commit the repository does not hold", land: true, host: func(h *dockerhosttest.Host) { h.Lagging = "0000000" },
			state: contract.Unknown, says: "whether the database's files changed since cannot be read"},
		{name: "a container still starting", land: true, host: func(h *dockerhosttest.Host) { h.Containers["shop"] = map[string]any{"health": "starting"} },
			state: contract.Unknown, says: "still starting"},
		{name: "a database missing its env.names", land: true, host: func(h *dockerhosttest.Host) { h.Set(map[string]string{"shop/" + dbBlock + ".env.names": ""}) },
			state: contract.Unknown, says: "without " + dbBlock + ".env.names"},
		{name: "a report naming no container of the database", land: true, host: func(h *dockerhosttest.Host) { h.Containers["shop"] = map[string]any{"container": "shop-web"} },
			state: contract.Unknown, says: "shows no shop-store"},
		{name: "a container that exited before it ever read active", land: true, neverActive: true, state: contract.Unknown, says: "reads exited: FATAL: password authentication failed",
			host: func(h *dockerhosttest.Host) { h.Containers["shop"] = map[string]any{"state": "exited", "logs": logs} }},
		{name: "a container that exited on a host that sends no output before it ever read active", land: true, neverActive: true, state: contract.Unknown,
			says: "reads exited; this host's node report carries no container output", host: func(h *dockerhosttest.Host) { h.Containers["shop"] = map[string]any{"state": "exited"} }},
		{name: "a container restarting before it ever read active", land: true, neverActive: true, state: contract.Unknown, says: "reads restarting: FATAL",
			host: func(h *dockerhosttest.Host) {
				h.Containers["shop"] = map[string]any{"state": "restarting", "health": "unhealthy", "logs": logs}
			}},
		{name: "a container restarting after it read active", land: true, state: contract.Present, unwell: true, cause: dockerhost.DatabaseNotRunning,
			whole: true, says: "shop-store reads restarting",
			host: func(h *dockerhosttest.Host) {
				h.Containers["shop"] = map[string]any{"state": "restarting", "health": "unhealthy", "logs": logs}
			}},
		{name: "an unhealthy container of an engine naming no trouble", land: true, state: contract.Present, unwell: true, whole: true, says: "shop-store reads unhealthy",
			engine: func(en *dockerhost.Engine) { en.Trouble = nil },
			host: func(h *dockerhosttest.Host) {
				h.Containers["shop"] = map[string]any{"health": "unhealthy", "logs": logs}
			}},
		{name: "an unhealthy container that never read active", land: true, neverActive: true, state: contract.Unknown, says: "before it ever read active",
			host: func(h *dockerhosttest.Host) {
				h.Containers["shop"] = map[string]any{"health": "unhealthy", "logs": logs}
			}},
		{name: "a request naming its repository only in the recorded identity", land: true, recorded: true, state: contract.Present, says: "shop-store runs"},
		{name: "an unhealthy container names its health and no output line", land: true, state: contract.Present, unwell: true, cause: dockerhost.DatabaseUnhealthy,
			whole: true, says: "shop-store reads unhealthy",
			host: func(h *dockerhosttest.Host) {
				h.Containers["shop"] = map[string]any{"health": "unhealthy", "logs": logs}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newDBEnv(t)
			if tc.land {
				landed := e.Desired("shop", nil)
				if tc.other {
					landed.SubscriptionID = "sub_db_other"
				}
				e.Land(t, landed)
			}
			if tc.host != nil {
				tc.host(e.Host)
			}
			if tc.engine != nil {
				tc.engine(&e.DB.Engine)
			}

			d := e.Desired("shop", nil)
			d.NeverActive = tc.neverActive
			if tc.recorded {
				d.Bindings["host"]["clusterRepoUrl"] = ""
				d.RecordedResources = map[string]contract.RecordedResource{dockerhost.DatabaseResource: {Identity: map[string]string{"repo": dockerhosttest.Repo}}}
			}

			obs := e.Observe(t, d)

			detail := obs.StatusDetail
			if obs.Unhealthy != nil {
				detail = obs.Unhealthy.Detail
			}
			if obs.State != tc.state || (obs.Unhealthy != nil) != tc.unwell || !strings.Contains(detail, tc.says) || (tc.whole && detail != tc.says) ||
				(tc.cause != "" && obs.Unhealthy.Cause != tc.cause) {
				t.Errorf("ObserveResource = %+v; want %s, unhealthy %v %q, naming %q", obs, tc.state, tc.unwell, tc.cause, tc.says)
			}
			if obs.State == contract.Present && (obs.Outputs["host"] != "shop-store" || obs.Outputs["port"] != "7000") {
				t.Errorf("the outputs are %v; want host shop-store on port 7000", obs.Outputs)
			}
		})
	}
}

func TestDatabase_AContextMarkedReadOnlyLandsNothingOnTheHostRepository(t *testing.T) {
	e := newDBEnv(t)
	before := len(e.Requests())

	_, err := e.DB.EnsureResources(contract.WithReadOnly(context.Background()), e.Desired("shop", nil))

	if !errors.Is(err, contract.ErrReadOnly) {
		t.Errorf("EnsureResources for a read-only context = %v, want an error wrapping contract.ErrReadOnly", err)
	}
	for _, r := range e.Requests()[before:] {
		if r.Method != "GET" {
			t.Errorf("a read-only context sent %s %s to the host repository, want nothing but reads", r.Method, r.Path)
		}
	}
}

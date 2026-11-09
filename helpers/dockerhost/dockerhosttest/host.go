// Package dockerhosttest fakes a Docker host's repository and node report for a per-app block's tests.
package dockerhosttest

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/capabilities/github/landing/landingtest"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
	"github.com/mitstack-eu/block-kit/observe"
)

const Uploaded = "uploaded"

// Host is the fake host. Its fields say what its next node report holds, and each zero value reports a healthy host.
type Host struct {
	*landingtest.Fake
	Sub string
	// Block and Service name the compose file the host runs, <app>/<Block>.compose.yaml, as <app>-<Service>.
	Block, Service   string
	Allocatable      dockerhost.Reservation
	Silent, Unsynced bool
	// Lagging is the commit the agent last synced, main's head when empty.
	Lagging      string
	BackupError  string
	Volumes      map[string]bool
	BackupResult string
	// RunRequest is the request the host's backup run answers, each app's own request when empty.
	RunRequest string
	// Containers replaces an app's running container with the fields it holds, such as state, health and logs.
	Containers map[string]map[string]any
}

// New is a host with room for a few small apps whose repository holds a README alone.
func New(t testing.TB, repo, sub, block, service string) *Host {
	t.Helper()
	h := &Host{
		Fake: landingtest.New(t, repo), Sub: sub, Block: block, Service: service,
		Allocatable:  dockerhost.Reservation{CPUMillis: 3500, MemoryBytes: 7 << 30, DiskBytes: 70 << 30},
		Volumes:      map[string]bool{},
		BackupResult: Uploaded,
		Containers:   map[string]map[string]any{},
	}
	h.Seed(map[string]string{"README.md": "The host's repository.\n"})
	return h
}

func (h *Host) GitHub(_, token string) *ghapi.Client {
	return ghapi.NewClient(h.URL, token, h.Client())
}

// Set commits changes onto main, an empty content deleting its path.
func (h *Host) Set(changes map[string]string) {
	files, gone := map[string]string{}, []string{}
	for path, content := range changes {
		if content == "" {
			gone = append(gone, path)
			continue
		}
		files[path] = content
	}
	if len(files) > 0 {
		h.Seed(files)
	}
	if len(gone) > 0 {
		h.Remove(gone...)
	}
}

func (h *Host) Under(app string) map[string]string {
	out := map[string]string{}
	for path, content := range h.Main() {
		if strings.HasPrefix(path, app+"/") {
			out[path] = content
		}
	}
	return out
}

func (h *Host) NodeReports(_ context.Context, _ contract.Desired, subID string) ([]observe.NodeReport, error) {
	if subID != h.Sub || h.Silent {
		return nil, nil
	}
	main := h.Main()
	apps, volumes, runs := map[string]any{}, []any{}, []any{}
	for path := range main {
		app, ok := strings.CutSuffix(path, "/"+h.Block+".compose.yaml")
		if !ok {
			continue
		}
		c := map[string]any{"container": app + "-" + h.Service, "digest": "sha256:5f1c", "state": "running", "health": "healthy"}
		maps.Copy(c, h.Containers[app])
		apps[app] = []any{c}
		if !h.Volumes[app] {
			continue
		}
		volumes = append(volumes, map[string]any{"app": app, "block": h.Block, "volume": h.Service + "-data", "mounted": true})
		request := strings.TrimSpace(main[app+"/"+h.Block+".backup-request"])
		if h.RunRequest != "" {
			request = h.RunRequest
		}
		if request != "" && h.BackupResult != "" {
			runs = append(runs, map[string]any{"app": app, "block": h.Block, "object": app + "/20261102T120000Z-5f1c.age", "result": h.BackupResult, "request": request})
		}
	}
	var backupErr any
	if h.BackupError != "" {
		backupErr = h.BackupError
	}
	synced := h.Head()
	if h.Lagging != "" {
		synced = h.Lagging
	}
	agent := map[string]any{"exitCode": 0, "output": "", "syncedCommit": synced}
	if h.Unsynced {
		delete(agent, "syncedCommit")
	}
	body, err := json.Marshal(map[string]any{"host": map[string]any{
		"capacity": map[string]any{"allocatable": h.Allocatable},
		"agent":    agent,
		"apps":     apps, "volumes": volumes,
		"backups": map[string]any{"runs": runs, "error": backupErr},
	}})
	return []observe.NodeReport{{ReceivedAt: time.Now(), Body: body}}, err
}

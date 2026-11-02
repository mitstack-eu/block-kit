package dockerhost_test

// A Docker host's newest node report answers its allocatable capacity and the commit its agent last synced from host.agent.syncedCommit, never from the agent's log, and an explicit reason naming the host's release when it cannot.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
	"github.com/mitstack-eu/block-kit/observe"
)

const (
	hostSub = "sub_host"
	synced  = "6e40c8f03e5b2a7c9d1f4e8a0b3c6d9e2f5a8b1c"
	older   = "0123456789abcdef0123456789abcdef01234567"
)

type hostReports struct {
	reports []observe.NodeReport
	err     error
	asked   []string
}

func (h *hostReports) NodeReports(_ context.Context, _ contract.Desired, sub string) ([]observe.NodeReport, error) {
	h.asked = append(h.asked, sub)
	return h.reports, h.err
}

func received(minute int, body string) observe.NodeReport {
	return observe.NodeReport{ReceivedAt: time.Date(2026, 11, 2, 17, minute, 0, 0, time.UTC), Body: []byte(body)}
}

func newest(t *testing.T, reports ...observe.NodeReport) dockerhost.Report {
	t.Helper()
	api := &hostReports{reports: reports}
	r, found, err := dockerhost.NewestReport(context.Background(), api, contract.Desired{}, hostSub)
	if err != nil || !found {
		t.Fatalf("NewestReport = found %v, %v; want the newest report", found, err)
	}
	if len(api.asked) != 1 || api.asked[0] != hostSub {
		t.Errorf("NewestReport asked for %v, want the host subscription once", api.asked)
	}
	return r
}

func TestNewestReport_AnswersTheCommitAndCapacityOfTheNewestReport(t *testing.T) {
	r := newest(t,
		received(39, `{"host":{"capacity":{"allocatable":{"cpuMillis":3500,"memoryBytes":7516192768}},"agent":{"output":"Pulling fs layer\n","syncedCommit":"`+synced+`"}}}`),
		received(38, `{"host":{"agent":{"output":"","syncedCommit":"`+older+`"}}}`),
	)

	commit, err := r.SyncedCommit()

	if err != nil || commit != synced {
		t.Errorf("SyncedCommit = %q, %v; want the newest report's %s", commit, err, synced)
	}
	if r.Allocatable == nil || *r.Allocatable != (dockerhost.Reservation{CPUMillis: 3500, MemoryBytes: 7516192768}) {
		t.Errorf("Allocatable = %+v, want the newest report's", r.Allocatable)
	}
	var body struct {
		Host struct {
			Agent struct {
				Output string `json:"output"`
			} `json:"agent"`
		} `json:"host"`
	}
	if err := r.Decode(&body); err != nil || body.Host.Agent.Output != "Pulling fs layer\n" {
		t.Errorf("Decode = %+v, %v; want the newest report's own fields", body, err)
	}
}

func TestNewestReport_AnswersNoCommitWhenTheReportCannotNameOne(t *testing.T) {
	for _, tc := range []struct {
		name, agent string
		want        []string
	}{
		{"a host on a release without the field, whatever its log says", `{"exitCode":0,"output":"agent: synced ` + synced + `\n"}`,
			[]string{"host.agent.syncedCommit", "hcloud-docker-host release before 0.8.0", "upgrade the host"}},
		{"an agent that has not synced on this release", `{"output":"","syncedCommit":null}`,
			[]string{"has not synced", "next sync"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newest(t, received(39, `{"host":{"agent":`+tc.agent+`}}`))

			commit, err := r.SyncedCommit()

			if commit != "" || err == nil {
				t.Fatalf("SyncedCommit = %q, %v; want no commit and the reason", commit, err)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("SyncedCommit's reason = %q, want it to name %q", err, w)
				}
			}
		})
	}
}

func TestNewestReport_FindsNoneWhileTheHostHasSentNoReport(t *testing.T) {
	_, found, err := dockerhost.NewestReport(context.Background(), &hostReports{}, contract.Desired{}, hostSub)

	if found || err != nil {
		t.Errorf("NewestReport = found %v, %v; want none found and no error", found, err)
	}
}

func TestNewestReport_FailsAReadItCannotMake(t *testing.T) {
	for _, tc := range []struct {
		name, sub, class string
		api              *hostReports
	}{
		{"a host binding with no subscription", "", "host-binding", &hostReports{}},
		{"a platform that does not answer", hostSub, "", &hostReports{err: errors.New("the platform answered 503")}},
		{"a report that is not JSON", hostSub, "", &hostReports{reports: []observe.NodeReport{received(39, `{"host":`)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := dockerhost.NewestReport(context.Background(), tc.api, contract.Desired{}, tc.sub)

			var refused *handler.Refused
			if err == nil || errors.As(err, &refused) != (tc.class != "") || (refused != nil && refused.Class != tc.class) {
				t.Errorf("NewestReport error = %v, want an error refused as %q", err, tc.class)
			}
		})
	}
}

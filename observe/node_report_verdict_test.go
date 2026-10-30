package observe_test

// The client keeps the verdict the platform stored beside each report, so a block can tell a signed report from an unsigned one.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/observe"
)

func TestNodeReportsClient_KeepsTheVerdictStoredWithEachReport(t *testing.T) {
	verdict := `{"reportSignature":{"status":"signed","provesAbsence":true}}`
	listing := `{"reports":[{"receivedAt":"2026-10-30T12:00:00Z","body":{"madeAt":"2026-10-30T11:59:58Z"},"verdict":` + verdict + `}]}`
	platform := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, listing) }))
	defer platform.Close()
	client := observe.NodeReportsClient{HTTPClient: platform.Client()}

	got, err := client.NodeReports(withCallback(platform.URL+"/v1/webhooks/operator-events", callbackToken), contract.Desired{}, "sub-host")

	if err != nil || len(got) != 1 {
		t.Fatalf("NodeReports = %+v, %v; want the one report the platform serves", got, err)
	}
	var want, kept any
	if err := json.Unmarshal([]byte(verdict), &want); err != nil {
		t.Fatalf("decoding the served verdict: %v", err)
	}
	if err := json.Unmarshal(got[0].Verdict, &kept); err != nil {
		t.Fatalf("NodeReports kept verdict %q, which is not JSON: %v", got[0].Verdict, err)
	}
	if !jsonEqual(want, kept) {
		t.Errorf("NodeReports kept verdict %s, want %s", got[0].Verdict, verdict)
	}
}

func jsonEqual(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

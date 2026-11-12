// A node asks the platform's lease route once for a credential, and each answer that is no usable lease comes back as an error naming it.
package lease_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/helpers/lease"
)

const (
	reportToken = "report-token-sentinel-9b2e"
	resource    = "scaleway.snapshot-bucket"
	leasedLink  = "https://store.example/app-data/1?X-Amz-Signature=sig-secret-7f3"
)

var now = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

type ask struct {
	method, path, bearer, contentType string
	body                              struct {
		Resource string          `json:"resource"`
		Want     json.RawMessage `json:"want"`
	}
}

// route is the platform's lease route, answering every ask with status, header and body.
type route struct {
	srv    *httptest.Server
	status int
	header http.Header
	body   string
	asks   []ask
}

func newRoute(t *testing.T, status int, body string) *route {
	t.Helper()
	r := &route{status: status, body: body, header: http.Header{}}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got := ask{method: req.Method, path: req.URL.Path, bearer: req.Header.Get("Authorization"), contentType: req.Header.Get("Content-Type")}
		raw, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(raw, &got.body)
		r.asks = append(r.asks, got)
		for name, values := range r.header {
			w.Header()[name] = values
		}
		w.WriteHeader(r.status)
		_, _ = io.WriteString(w, r.body)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *route) platform() lease.Platform {
	return lease.Platform{BaseURL: r.srv.URL, Token: reportToken, Clock: func() time.Time { return now }}
}

func leaseBody(expiresAt string) string {
	return `{"credential":{"url":"` + leasedLink + `"},"expiresAt":"` + expiresAt + `"}`
}

func TestAsk_SendsTheBearerTheResourceAndTheWantAndReturnsTheCredentialUnread(t *testing.T) {
	r := newRoute(t, http.StatusOK, leaseBody("2026-11-12T14:00:00Z"))

	got, err := r.platform().Ask(context.Background(), resource, map[string]string{"unit": "app-data"})

	if err != nil || string(got.Credential) != `{"url":"`+leasedLink+`"}` || !got.ExpiresAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("Ask = %s expiring %v, %v; want the credential the route answered, expiring two hours after %v", got.Credential, got.ExpiresAt, err, now)
	}
	if len(r.asks) != 1 {
		t.Fatalf("the route received %d asks, want one", len(r.asks))
	}
	sent := r.asks[0]
	if sent.method != http.MethodPost || sent.path != "/v1/cluster-leases" || sent.bearer != "Bearer "+reportToken || sent.contentType != "application/json" {
		t.Errorf("the ask was %s %s with Authorization %q and Content-Type %q; want a JSON POST to /v1/cluster-leases with the bearer", sent.method, sent.path, sent.bearer, sent.contentType)
	}
	if sent.body.Resource != resource || string(sent.body.Want) != `{"unit":"app-data"}` {
		t.Errorf("the ask named resource %q and want %s; want %q and the want it was handed", sent.body.Resource, sent.body.Want, resource)
	}
}

func TestAsk_NamesTheRoutesAnswerForEachRefusalAndAsksNoSecondTime(t *testing.T) {
	cases := []struct {
		label      string
		status     int
		retryAfter string
		message    string
		wantWait   time.Duration
	}{
		{"a token whose subscription is removed is refused with 401", http.StatusUnauthorized, "", "report token not recognised", 0},
		{"a resource the block neither requires nor provides is refused with 404", http.StatusNotFound, "", "the node's block neither requires nor provides " + resource, 0},
		{"a provider whose manifest lists no lease is refused with 409", http.StatusConflict, "", "the provider's block snapshot-store does not list lease", 0},
		{"a provider that fails to mint is answered 502 with the fixed message", http.StatusBadGateway, "", "the lease could not be minted", 0},
		{"a provider with no outputs yet is answered 503 with its Retry-After", http.StatusServiceUnavailable, "30", "lease is not available yet; retry later", 30 * time.Second},
		{"a refusal that repeats the bearer reaches the caller without it", http.StatusBadRequest, "", "no lease for " + reportToken, 0},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			r := newRoute(t, tc.status, `{"error":"`+tc.message+`"}`)
			if tc.retryAfter != "" {
				r.header.Set("Retry-After", tc.retryAfter)
			}

			got, err := r.platform().Ask(context.Background(), resource, nil)

			var refusal *lease.Refusal
			if !errors.As(err, &refusal) || got.Credential != nil {
				t.Fatalf("Ask = %s, %v; want no credential and a *lease.Refusal", got.Credential, err)
			}
			if refusal.Status != tc.status || refusal.RetryAfter != tc.wantWait || refusal.Resource != resource {
				t.Errorf("the refusal holds status %d, Retry-After %v and resource %q; want %d, %v and %q", refusal.Status, refusal.RetryAfter, refusal.Resource, tc.status, tc.wantWait, resource)
			}
			text := err.Error()
			named := strings.ReplaceAll(tc.message, reportToken, "")
			if !strings.Contains(text, strconv.Itoa(tc.status)) || !strings.Contains(text, strings.TrimSpace(named)) || !strings.Contains(text, resource) {
				t.Errorf("the error %q does not name the status %d, the route's message %q and the resource", text, tc.status, named)
			}
			if tc.retryAfter != "" && !strings.Contains(text, "Retry-After "+tc.wantWait.String()) {
				t.Errorf("the error %q does not carry the answer's Retry-After of %v", text, tc.wantWait)
			}
			if strings.Contains(text, reportToken) {
				t.Errorf("the error carries the bearer: %q", text)
			}
			if len(r.asks) != 1 {
				t.Errorf("Ask sent %d requests after a %d, want one, since the caller decides when to ask again", len(r.asks), tc.status)
			}
		})
	}
}

func TestAsk_RefusesAnAnswerThatIsNoUsableLease(t *testing.T) {
	cases := []struct{ label, body, want string }{
		{"an answer whose expiry has passed", leaseBody("2026-11-12T11:59:59Z"), "expired"},
		{"an answer with no expiry", `{"credential":{"url":"` + leasedLink + `"}}`, "expiresAt"},
		{"an answer with no credential", `{"expiresAt":"2026-11-12T14:00:00Z"}`, "credential"},
		{"an answer that is not JSON", `<html>` + leasedLink + `</html>`, "JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			r := newRoute(t, http.StatusOK, tc.body)

			got, err := r.platform().Ask(context.Background(), resource, nil)

			if err == nil || got.Credential != nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Ask of %s = %s, %v; want no credential and an error naming %q", tc.label, got.Credential, err, tc.want)
			}
			if text := err.Error(); strings.Contains(text, "X-Amz") || strings.Contains(text, "store.example") || strings.Contains(text, reportToken) {
				t.Errorf("the error carries the link or the bearer: %q", text)
			}
		})
	}
}

func TestAsk_SendsNothingWithoutAnAddressOrABearer(t *testing.T) {
	r := newRoute(t, http.StatusOK, leaseBody("2026-11-12T14:00:00Z"))
	for label, p := range map[string]lease.Platform{
		"no bearer":  {BaseURL: r.srv.URL},
		"no address": {Token: reportToken},
	} {
		_, err := p.Ask(context.Background(), resource, nil)

		if err == nil || len(r.asks) != 0 {
			t.Errorf("Ask with %s = %v after %d requests; want an error and no request", label, err, len(r.asks))
		}
	}
}

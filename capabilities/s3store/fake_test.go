package s3store_test

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testBucket    = "backups"
	testRegion    = "fr-par"
	testAccessKey = "SCWTESTACCESSKEY0001"
	testSecretKey = "test-secret-that-never-leaves"
	// otherAccessKey is a second key the store accepts, whose calls no pause on testAccessKey holds.
	otherAccessKey = "SCWOTHERACCESSKEY002"
	pageSize       = 1000
)

var writtenAt = time.Date(2026, time.November, 27, 8, 0, 0, 0, time.UTC)

type storedObject struct {
	key  string
	size int64
	at   time.Time
}

// store is a fake S3-compatible endpoint that answers ListObjectsV2 by path-style address, a page of at most pageSize entries at a time, and refuses a request signed with neither of its access keys.
type store struct {
	srv     *httptest.Server
	mu      sync.Mutex
	objects []storedObject
	// refuse, when set, answers a request in place of the listing and reports true.
	refuse func(w http.ResponseWriter) bool
	sent   atomic.Int64
}

func newStore(t *testing.T) *store {
	t.Helper()
	s := &store{}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *store) put(key string, size int64, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects = append(s.objects, storedObject{key: key, size: size, at: at})
}

func (s *store) refuseWith(refuse func(w http.ResponseWriter) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuse = refuse
}

type listResult struct {
	XMLName               xml.Name       `xml:"ListBucketResult"`
	Name                  string         `xml:"Name"`
	Prefix                string         `xml:"Prefix"`
	KeyCount              int            `xml:"KeyCount"`
	MaxKeys               int            `xml:"MaxKeys"`
	IsTruncated           bool           `xml:"IsTruncated"`
	Contents              []listContent  `xml:"Contents"`
	CommonPrefixes        []commonPrefix `xml:"CommonPrefixes"`
	NextContinuationToken string         `xml:"NextContinuationToken,omitempty"`
}

type listContent struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	Size         int64  `xml:"Size"`
}

type commonPrefix struct {
	Prefix string `xml:"Prefix"`
}

// entry is one line of a listing, an object or a folder, ordered by its name as S3 orders both.
type entry struct {
	name   string
	object *storedObject
}

func (s *store) serve(w http.ResponseWriter, r *http.Request) {
	s.sent.Add(1)
	s.mu.Lock()
	refuse := s.refuse
	objects := slices.Clone(s.objects)
	s.mu.Unlock()
	if refuse != nil && refuse(w) {
		return
	}
	if !signedWithAKey(r.Header.Get("Authorization")) {
		answerError(w, http.StatusForbidden, "SignatureDoesNotMatch", "The request was not signed with the store's key.")
		return
	}
	q := r.URL.Query()
	if r.Method != http.MethodGet || r.URL.Path != "/"+testBucket || q.Get("list-type") != "2" {
		answerError(w, http.StatusBadRequest, "InvalidRequest", "Only a path-style ListObjectsV2 of the one bucket is answered.")
		return
	}
	writeXML(w, http.StatusOK, page(objects, q.Get("prefix"), q.Get("delimiter"), q.Get("start-after"), q.Get("continuation-token")))
}

func signedWithAKey(authorization string) bool {
	for _, key := range []string{testAccessKey, otherAccessKey} {
		if strings.HasPrefix(authorization, "AWS4-HMAC-SHA256 Credential="+key+"/") {
			return true
		}
	}
	return false
}

func page(objects []storedObject, prefix, delimiter, startAfter, token string) listResult {
	entries := listEntries(objects, prefix, delimiter)
	after := max(startAfter, token)
	entries = slices.DeleteFunc(entries, func(e entry) bool { return e.name <= after })
	result := listResult{Name: testBucket, Prefix: prefix, MaxKeys: pageSize}
	if len(entries) > pageSize {
		entries = entries[:pageSize]
		result.IsTruncated = true
		result.NextContinuationToken = entries[pageSize-1].name
	}
	for _, e := range entries {
		if e.object == nil {
			result.CommonPrefixes = append(result.CommonPrefixes, commonPrefix{Prefix: e.name})
			continue
		}
		result.Contents = append(result.Contents, listContent{Key: e.name, LastModified: e.object.at.Format(time.RFC3339Nano), Size: e.object.size})
	}
	result.KeyCount = len(entries)
	return result
}

func listEntries(objects []storedObject, prefix, delimiter string) []entry {
	var entries []entry
	seen := map[string]bool{}
	for i := range objects {
		o := &objects[i]
		rest, ok := strings.CutPrefix(o.key, prefix)
		if !ok {
			continue
		}
		if cut := strings.Index(rest, delimiter); delimiter != "" && cut >= 0 {
			folder := prefix + rest[:cut+len(delimiter)]
			if !seen[folder] {
				seen[folder] = true
				entries = append(entries, entry{name: folder})
			}
			continue
		}
		entries = append(entries, entry{name: o.key, object: o})
	}
	slices.SortFunc(entries, func(a, b entry) int { return strings.Compare(a.name, b.name) })
	return entries
}

type errorBody struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

func answerError(w http.ResponseWriter, status int, code, message string) {
	writeXML(w, status, errorBody{Code: code, Message: message})
}

func writeXML(w http.ResponseWriter, status int, body any) {
	raw, err := xml.Marshal(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Length", strconv.Itoa(len(xml.Header)+len(raw)))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(raw)
}

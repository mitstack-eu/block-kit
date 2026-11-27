package s3store_test

// The client lists what an S3-compatible bucket holds under a prefix, follows the store's pages, logs each call with its subscription, and never lets a key reach a log line or an error.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/s3store"
	"github.com/mitstack-eu/block-kit/helpers/calllog"
	"github.com/mitstack-eu/block-kit/helpers/reqcache"
)

func testConfig(s *store) s3store.Config {
	return s3store.Config{Endpoint: s.srv.URL, Region: testRegion, Bucket: testBucket, AccessKey: testAccessKey, SecretKey: testSecretKey}
}

func newClient(t *testing.T, s *store, holds *s3store.Holds) *s3store.Client {
	t.Helper()
	c, err := s3store.NewThrough(testConfig(s), holds, s.srv.Client().Transport)
	if err != nil {
		t.Fatalf("NewThrough(%s) = %v; want a client", s.srv.URL, err)
	}
	return c
}

func fillPrefix(s *store, prefix string, n int) {
	for i := range n {
		s.put(fmt.Sprintf("%sobject-%05d", prefix, i), int64(100+i), writtenAt.Add(time.Duration(i)*time.Second))
	}
}

func keysOf(objects []s3store.Object) []string {
	keys := []string{}
	for _, o := range objects {
		keys = append(keys, o.Key)
	}
	return keys
}

// captureLog sends the standard logger, which calllog writes to, into a buffer for the test's length.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	flags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(flags)
	})
	return &buf
}

// callLines parses each calllog line in out into its fields.
func callLines(out string) []map[string]string {
	var lines []map[string]string
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(line, "calllog: ")
		if !ok {
			continue
		}
		fields := map[string]string{}
		for _, field := range strings.Fields(rest) {
			name, value, _ := strings.Cut(field, "=")
			fields[name] = value
		}
		lines = append(lines, fields)
	}
	return lines
}

func assertNoKey(t *testing.T, what, text string) {
	t.Helper()
	for _, key := range []string{testAccessKey, testSecretKey} {
		if strings.Contains(text, key) {
			t.Errorf("%s carries a key: %q", what, text)
		}
	}
}

func TestList_ABucketHolding1500ObjectsUnderOnePrefixListsAllOfThemOverTwoPages(t *testing.T) {
	s := newStore(t)
	fillPrefix(s, "db-1/base/", 1500)
	s.put("db-2/base/object-00000", 1, writtenAt)
	c := newClient(t, s, s3store.NewHolds(time.Now))

	got, err := c.List(context.Background(), s3store.Query{Prefix: "db-1/base/"})

	if err != nil {
		t.Fatalf("List(db-1/base/) = %v; want the objects", err)
	}
	if len(got) != 1500 {
		t.Fatalf("List(db-1/base/) returned %d objects; want 1500", len(got))
	}
	for i, o := range got {
		want := s3store.Object{Key: fmt.Sprintf("db-1/base/object-%05d", i), Size: int64(100 + i), LastModified: writtenAt.Add(time.Duration(i) * time.Second)}
		if o.Key != want.Key || o.Size != want.Size || !o.LastModified.Equal(want.LastModified) {
			t.Errorf("object %d = %+v; want %+v", i, o, want)
		}
	}
	if sent := s.sent.Load(); sent != 2 {
		t.Errorf("the store answered %d calls; want 2 pages", sent)
	}
}

func TestListFolders_APrefixHoldingThreeFoldersListsTheThreeFoldersAndNoObjectInsideThem(t *testing.T) {
	s := newStore(t)
	for _, folder := range []string{"base/20261125/", "base/20261126/", "base/20261127/"} {
		fillPrefix(s, "db-1/"+folder, 3)
	}
	c := newClient(t, s, s3store.NewHolds(time.Now))

	got, err := c.ListFolders(context.Background(), "db-1/base/", "/")

	want := []string{"db-1/base/20261125/", "db-1/base/20261126/", "db-1/base/20261127/"}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("ListFolders(db-1/base/, /) = %q, %v; want %q", got, err, want)
	}
}

func TestList_APrefixWhoseNewestKnownKeyTheCallerHoldsListsOnlyTheObjectsWrittenAfterIt(t *testing.T) {
	s := newStore(t)
	fillPrefix(s, "db-1/wal/", 5)
	c := newClient(t, s, s3store.NewHolds(time.Now))

	got, err := c.List(context.Background(), s3store.Query{Prefix: "db-1/wal/", StartAfter: "db-1/wal/object-00002"})

	want := []string{"db-1/wal/object-00003", "db-1/wal/object-00004"}
	if err != nil || !slices.Equal(keysOf(got), want) {
		t.Errorf("List after object-00002 = %q, %v; want %q", keysOf(got), err, want)
	}
}

func TestList_AnEmptyPrefixListsAnEmptyListWithNoError(t *testing.T) {
	s := newStore(t)
	fillPrefix(s, "db-1/base/", 2)
	c := newClient(t, s, s3store.NewHolds(time.Now))

	got, err := c.List(context.Background(), s3store.Query{Prefix: "db-9/base/"})

	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("List(db-9/base/) = %v (nil %t), %v; want an empty, non-nil list and no error, unlike a failed read", got, got == nil, err)
	}
}

func TestList_EachCallIsLoggedWithTheSubscriptionItServesAndNoKey(t *testing.T) {
	s := newStore(t)
	fillPrefix(s, "db-1/base/", 1500)
	c := newClient(t, s, s3store.NewHolds(time.Now))
	logged := captureLog(t)
	ctx := calllog.WithSubscription(context.Background(), "sub-7038")

	_, err := c.List(ctx, s3store.Query{Prefix: "db-1/base/"})

	if err != nil {
		t.Fatalf("List(db-1/base/) = %v; want the objects", err)
	}
	lines := callLines(logged.String())
	if len(lines) != 2 {
		t.Fatalf("%d calllog lines for a two-page listing; want 2. Log:\n%s", len(lines), logged)
	}
	for i, line := range lines {
		if line["subscription"] != "sub-7038" || line["method"] != "GET" || line["answer"] != "200" {
			t.Errorf("calllog line %d = %v; want subscription=sub-7038 method=GET answer=200", i, line)
		}
	}
	assertNoKey(t, "the log", logged.String())
}

func TestList_AStoreThatAnswers403WithABodyEchoingTheKeysNamesTheRefusalAndCarriesNoKey(t *testing.T) {
	s := newStore(t)
	s.refuseWith(func(w http.ResponseWriter) bool {
		answerError(w, http.StatusForbidden, "AccessDenied", "Access key "+testAccessKey+" with secret "+testSecretKey+" may not list this bucket.")
		return true
	})
	c := newClient(t, s, s3store.NewHolds(time.Now))
	logged := captureLog(t)

	got, err := c.List(context.Background(), s3store.Query{Prefix: "db-1/base/"})

	var refused *s3store.Error
	if !errors.As(err, &refused) || got != nil {
		t.Fatalf("List = %v, %v; want no objects and an *s3store.Error", got, err)
	}
	if refused.Status != http.StatusForbidden || refused.Code != "AccessDenied" {
		t.Errorf("refusal = %d %s; want 403 AccessDenied", refused.Status, refused.Code)
	}
	assertNoKey(t, "the error", err.Error())
	assertNoKey(t, "the refusal's message", refused.Message)
	assertNoKey(t, "the log", logged.String())
}

func TestList_OneRequestListsEachPrefixOnce(t *testing.T) {
	s := newStore(t)
	fillPrefix(s, "db-1/base/", 3)
	c := newClient(t, s, s3store.NewHolds(time.Now))
	ctx := reqcache.Snapshot(context.Background())

	first, firstErr := c.List(ctx, s3store.Query{Prefix: "db-1/base/"})
	second, secondErr := c.List(ctx, s3store.Query{Prefix: "db-1/base/"})

	if firstErr != nil || secondErr != nil || !slices.Equal(keysOf(first), keysOf(second)) {
		t.Errorf("two listings = %q, %v and %q, %v; want the same objects twice", keysOf(first), firstErr, keysOf(second), secondErr)
	}
	if sent := s.sent.Load(); sent != 1 {
		t.Errorf("the store answered %d calls for one prefix listed twice in one request; want 1", sent)
	}
}

func TestNew_RefusesAConfigItCannotUse(t *testing.T) {
	good := s3store.Config{Endpoint: "https://s3.example.test", Region: testRegion, Bucket: testBucket, AccessKey: testAccessKey, SecretKey: testSecretKey}
	cases := []struct {
		name   string
		change func(*s3store.Config)
	}{
		{"an endpoint over plain http is refused, since the keys would cross the wire unprotected", func(c *s3store.Config) { c.Endpoint = "http://s3.example.test" }},
		{"an endpoint with no host is refused", func(c *s3store.Config) { c.Endpoint = "https://" }},
		{"an endpoint carrying a path is refused, since the bucket is addressed by path", func(c *s3store.Config) { c.Endpoint = "https://s3.example.test/other" }},
		{"an endpoint carrying a user is refused", func(c *s3store.Config) { c.Endpoint = "https://" + testAccessKey + "@s3.example.test" }},
		{"an empty region is refused", func(c *s3store.Config) { c.Region = "" }},
		{"an empty bucket is refused", func(c *s3store.Config) { c.Bucket = "" }},
		{"a bucket name S3 does not allow is refused", func(c *s3store.Config) { c.Bucket = "Backups/../x" }},
		{"an empty access key is refused", func(c *s3store.Config) { c.AccessKey = "" }},
		{"an empty secret key is refused", func(c *s3store.Config) { c.SecretKey = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := good
			tc.change(&cfg)

			c, err := s3store.New(cfg)

			if err == nil {
				t.Fatalf("New = %v, nil; want a refusal", c)
			}
			assertNoKey(t, "the refusal", err.Error())
		})
	}
	if _, err := s3store.New(good); err != nil {
		t.Errorf("New(a usable config) = %v; want a client", err)
	}
}

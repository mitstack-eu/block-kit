package kittest_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/guardedhttp"
	"github.com/mitstack-eu/block-kit/verify/kittest"
)

func guarded(hosts []string) *http.Client {
	c, err := guardedhttp.New(guardedhttp.Config{Hosts: hosts})
	if err != nil {
		panic(err)
	}
	return c
}

func TestCheckGuardedClient_PassesTheKitsOwnClient(t *testing.T) {
	if problems := kittest.CheckGuardedClient(guarded); len(problems) != 0 {
		t.Errorf("CheckGuardedClient(guardedhttp client) = %q, want no problem", problems)
	}
}

func TestCheckGuardedClient_FailsAPlainClientNamingEachGuardItLacks(t *testing.T) {
	plain := func([]string) *http.Client { return &http.Client{} }

	problems := strings.Join(kittest.CheckGuardedClient(plain), "\n")

	for _, want := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "fe80::1", "ff02::1", "0.0.0.0", "did not declare", "over http", "CheckRedirect"} {
		if !strings.Contains(problems, want) {
			t.Errorf("CheckGuardedClient(http.Client{}) problems =\n%s\nwant one naming %q", problems, want)
		}
	}
}

func TestCheckGuardedClient_FailsAClientThatFollowsARedirectAndNothingElse(t *testing.T) {
	followsRedirects := func(hosts []string) *http.Client {
		c := guarded(hosts)
		c.CheckRedirect = nil
		return c
	}

	problems := kittest.CheckGuardedClient(followsRedirects)

	if len(problems) != 1 || !strings.Contains(problems[0], "CheckRedirect") {
		t.Errorf("CheckGuardedClient(client following redirects) = %q, want one problem naming CheckRedirect", problems)
	}
}

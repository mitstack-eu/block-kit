// Command mitstack-probe is the health check an app's container runs from a copy its host bind-mounts, so it needs no tool of the app's image: it exits 0 when its URL answers below 400, a redirect never followed, and 1 otherwise.
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// answerTimeout stays below the five seconds a Docker host's health check allows, so the probe names a slow answer itself.
const answerTimeout = 4 * time.Second

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, answerTimeout))
}

func run(args []string, out io.Writer, timeout time.Duration) int {
	if len(args) != 1 {
		fmt.Fprintln(out, "usage: mitstack-probe <url>")
		return 2
	}
	url := args[0]
	client := &http.Client{
		Timeout:       timeout,
		Transport:     &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(out, "GET %s: %v\n", url, err)
		return 1
	}
	resp.Body.Close()
	fmt.Fprintf(out, "GET %s answered %s\n", url, resp.Status)
	if resp.StatusCode >= http.StatusBadRequest {
		return 1
	}
	return 0
}

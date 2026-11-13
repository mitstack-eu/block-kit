package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"
)

const (
	// blockReleasesPath is the platform's record route, which trusts the posted record's signature and no caller.
	blockReleasesPath   = "/v1/block-releases"
	defaultPostTimeout  = time.Minute
	answerMaxBytes      = 8 << 10
	actionStored        = "stored"
	actionAlreadyStored = "already-stored"
)

// platformAnswer is what the record route answered a post with.
type platformAnswer struct {
	status   int
	location string
	body     []byte
}

func runPost(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("post", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	recordPath := fs.String("record", "", "path to the record.json to post")
	platform := fs.String("platform", "", "the platform's address, as https://api.mitstack.dev")
	summaryPath := fs.String("summary", "", "file the platform's answer is appended to, as $GITHUB_STEP_SUMMARY")
	timeout := fs.Duration("timeout", defaultPostTimeout, "how long to wait for the platform's answer")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}
	if *recordPath == "" || *platform == "" {
		fmt.Fprintln(stdout, "blockprov post: --record and --platform are both required")
		return 2
	}
	route, err := recordRoute(*platform)
	if err != nil {
		fmt.Fprintf(stdout, "blockprov post: %v\n", err)
		return 2
	}
	record, err := os.ReadFile(*recordPath)
	if err != nil {
		fmt.Fprintf(stdout, "blockprov post: reading record: %v\n", err)
		return 1
	}

	sentence, held := postRecord(recordClient(*timeout), *platform, route, record)
	sentence = oneLine(sentence)
	fmt.Fprintf(stdout, "blockprov post: %s\n", sentence)
	if err := appendLine(*summaryPath, sentence); err != nil {
		fmt.Fprintf(stdout, "blockprov post: writing the summary: %v\n", err)
		return 1
	}
	if !held {
		return 1
	}
	return 0
}

// recordRoute refuses a user or password, which Go would send as a credential beside the record, and a query, which the log would print.
func recordRoute(platform string) (string, error) {
	address, err := url.Parse(platform)
	if err != nil || (address.Scheme != "http" && address.Scheme != "https") || address.Host == "" {
		return "", errors.New("--platform must be an http or https address, as https://api.mitstack.dev")
	}
	if address.User != nil {
		return "", errors.New("--platform carries a user or a password, and the record is the only credential the post sends")
	}
	if address.RawQuery != "" || address.ForceQuery || address.Fragment != "" {
		return "", errors.New("--platform carries a query or a fragment, where an address is a scheme, a host and at most a path")
	}
	return address.JoinPath(blockReleasesPath).String(), nil
}

// recordClient follows no redirect, so the record reaches the named address alone, and holds no cookie jar.
func recordClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// postRecord sends record once and says in one sentence how the platform answered, and whether it holds the record.
func postRecord(client *http.Client, platform, route string, record []byte) (sentence string, held bool) {
	answer, err := sendRecord(client, route, record)
	if err != nil {
		return fmt.Sprintf("%s gave no answer, so the release record is not known to be stored: %v", platform, err), false
	}
	return answer.outcome(platform)
}

func sendRecord(client *http.Client, route string, record []byte) (platformAnswer, error) {
	req, err := http.NewRequest(http.MethodPost, route, bytes.NewReader(record))
	if err != nil {
		return platformAnswer{}, fmt.Errorf("building the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return platformAnswer{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, answerMaxBytes))
	if err != nil {
		return platformAnswer{}, fmt.Errorf("reading the answer of HTTP %d: %w", resp.StatusCode, err)
	}
	return platformAnswer{status: resp.StatusCode, location: resp.Header.Get("Location"), body: body}, nil
}

// outcome says in one sentence what the answer means for the record, and whether the platform holds it.
func (a platformAnswer) outcome(platform string) (sentence string, held bool) {
	const refused = "%s answered HTTP %d and stored no release record: %s"
	switch a.status / 100 {
	case 2:
		return a.admission(platform)
	case 3:
		return fmt.Sprintf(refused, platform, a.status, "a redirect to "+a.location+", which is not followed"), false
	}
	return fmt.Sprintf(refused, platform, a.status, a.refusal()), false
}

// admission reads a 2xx answer, which holds the record only where its action says so.
func (a platformAnswer) admission(platform string) (sentence string, held bool) {
	var admitted struct {
		Action  string `json:"action"`
		BlockID string `json:"blockId"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(a.body, &admitted); err == nil {
		switch admitted.Action {
		case actionStored:
			return fmt.Sprintf("%s stored the release record of %s %s.", platform, admitted.BlockID, admitted.Version), true
		case actionAlreadyStored:
			return fmt.Sprintf("%s already holds the release record of %s %s and stored nothing new.", platform, admitted.BlockID, admitted.Version), true
		}
	}
	return fmt.Sprintf("%s answered HTTP %d without saying the release record is stored: %s", platform, a.status, a.body), false
}

// refusal returns the platform's own error text, which opens with the check it refused, or the whole body where that is no such answer.
func (a platformAnswer) refusal() string {
	var refused struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(a.body, &refused); err != nil || refused.Error == "" {
		return string(a.body)
	}
	return refused.Error
}

// oneLine keeps an answer from starting a line of its own, where the runner would read a workflow command.
func oneLine(text string) string {
	spaced := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	return strings.Join(strings.Fields(spaced), " ")
}

func appendLine(path, line string) error {
	if path == "" {
		return nil
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(file, line); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

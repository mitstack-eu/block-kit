package cloud_test

// The action-poll schedule waits one second before hcloud-go's first ask about an action and doubles up to fifteen seconds, so a one-minute action costs seven asks.

import (
	"slices"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/hetzner/cloud"
)

// hcloudDefaultPoll is hcloud-go v2.49.0's own wait between asks, ConstantBackoff(500 * time.Millisecond), which a block without PollBackoff keeps.
const hcloudDefaultPoll = 500 * time.Millisecond

// mostAsks stops askTimes on a schedule that never reaches the action's end, so a wait of zero fails the test instead of hanging it.
const mostAsks = 1000

// askTimes lists when hcloud-go's action wait asks about an action that finishes after done, since WaitForFunc waits backoff(retries) before each ask, counting retries from zero, and stops at the first ask that finds the action finished.
func askTimes(backoff func(retries int) time.Duration, done time.Duration) []time.Duration {
	var asks []time.Duration
	var at time.Duration
	for retries := 0; at < done && retries < mostAsks; retries++ {
		at += backoff(retries)
		asks = append(asks, at)
	}
	return asks
}

func TestPollBackoff_GrowsFromOneSecondToFifteen(t *testing.T) {
	cases := []struct {
		name    string
		retries int
		want    time.Duration
	}{
		{"the first ask after an action starts waits one second, so an action that finishes within it is asked about once", 0, time.Second},
		{"the second ask waits two seconds", 1, 2 * time.Second},
		{"the third ask waits four seconds", 2, 4 * time.Second},
		{"the fourth ask waits eight seconds", 3, 8 * time.Second},
		{"the fifth ask waits fifteen seconds, since doubling to sixteen would pass the ceiling", 4, 15 * time.Second},
		{"the sixth ask waits fifteen seconds", 5, 15 * time.Second},
		{"the seventh ask waits fifteen seconds", 6, 15 * time.Second},
		{"an ask about an hour into the wait still waits fifteen seconds, never longer", 240, 15 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cloud.PollBackoff(tc.retries)

			if got != tc.want {
				t.Errorf("PollBackoff(%d) = %s; want %s", tc.retries, got, tc.want)
			}
		})
	}
}

func TestPollBackoff_AsksAboutAOneMinuteActionSevenTimes(t *testing.T) {
	if n := len(askTimes(func(int) time.Duration { return hcloudDefaultPoll }, time.Minute)); n != 120 {
		t.Fatalf("hcloud-go's default asks about a one-minute action %d times in this model; want 120", n)
	}
	want := []time.Duration{time.Second, 3 * time.Second, 7 * time.Second, 15 * time.Second, 30 * time.Second, 45 * time.Second, time.Minute}

	got := askTimes(cloud.PollBackoff, time.Minute)

	if !slices.Equal(got, want) {
		t.Errorf("asks about an action that finishes after a minute = %v; want %v, seven asks", got, want)
	}
}

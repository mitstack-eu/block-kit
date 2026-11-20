package cloud

import "time"

// The wait before hcloud-go's first ask about an action is firstPoll, doubled before each later ask up to longestPoll, since a short first wait sees a quick action soon and every ask spends a call of the budget all operators on one address share.
const (
	firstPoll   = time.Second
	longestPoll = 15 * time.Second
)

// PollBackoff is the wait before each ask hcloud-go's action waiter makes, which a block hands it with hcloud.WithPollOpts(hcloud.PollOpts{BackoffFunc: cloud.PollBackoff}).
func PollBackoff(retries int) time.Duration { return grow(firstPoll, longestPoll, retries) }

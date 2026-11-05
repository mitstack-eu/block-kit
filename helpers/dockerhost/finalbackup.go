package dockerhost

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/observe"
)

const (
	finalRequestPrefix = "final-"
	backupUploaded     = "uploaded"
)

// FinalBackup is a per-app block's wait, on an unsubscribe, for the host to upload a last backup of what the block keeps there.
type FinalBackup struct {
	App, Block string
	// Kept names what stays on the host until the upload, such as "shop's database".
	Kept string
	Now  time.Time
	// Held reads the backup request the app's directory holds, "" when none; Ask commits a new one there.
	Held func(context.Context) (string, error)
	Ask  func(context.Context, string) error
}

type finalBackupParts struct {
	Host struct {
		Volumes []struct {
			App   string `json:"app"`
			Block string `json:"block"`
		} `json:"volumes"`
		Backups struct {
			Runs []struct {
				App     string `json:"app"`
				Block   string `json:"block"`
				Result  string `json:"result"`
				Request string `json:"request"`
			} `json:"runs"`
			Error *string `json:"error"`
		} `json:"backups"`
	} `json:"host"`
}

// Done reports true once the host's newest report names the final backup uploaded, asking for one first, and at once when the host never made a volume of this block, since that holds nothing. A Waiting error holds the removal back, and any other error names why nothing may be removed.
func (f FinalBackup) Done(ctx context.Context, api observe.NodeReportsAPI, d contract.Desired, hostSubscription string) (bool, error) {
	report, found, err := NewestReport(ctx, api, d, hostSubscription)
	if err != nil {
		return false, err
	}
	if !found {
		return false, handler.Wait("the host has sent no node report, so %s is kept until one shows its final backup", f.Kept)
	}
	var parts finalBackupParts
	if err := report.Decode(&parts); err != nil {
		return false, fmt.Errorf("decoding the host's newest node report: %w", err)
	}
	if !f.holdsData(parts) {
		return true, nil
	}
	if reason := parts.Host.Backups.Error; reason != nil {
		return false, fmt.Errorf("the host reports %q, so %s cannot be backed up before its removal, and nothing was removed", *reason, f.Kept)
	}
	request, err := f.request(ctx)
	if err != nil || request == "" {
		return false, err
	}
	return f.judge(parts, request)
}

func (f FinalBackup) holdsData(parts finalBackupParts) bool {
	for _, v := range parts.Host.Volumes {
		if v.App == f.App && v.Block == f.Block {
			return true
		}
	}
	return false
}

// request is the final backup the directory already asks for, or "" with a Waiting error once it has asked for a new one.
func (f FinalBackup) request(ctx context.Context) (string, error) {
	held, err := f.Held(ctx)
	if err != nil {
		return "", err
	}
	if request := strings.TrimSpace(held); strings.HasPrefix(request, finalRequestPrefix) {
		return request, nil
	}
	request := finalRequestPrefix + strconv.FormatInt(f.Now.Unix(), 10)
	if err := f.Ask(ctx, request); err != nil {
		return "", err
	}
	return "", handler.Wait("asked the host for the final backup %s of %s, which is kept until the host uploads it", request, f.Kept)
}

func (f FinalBackup) judge(parts finalBackupParts, request string) (bool, error) {
	for _, run := range parts.Host.Backups.Runs {
		if run.App != f.App || run.Block != f.Block || run.Request != request {
			continue
		}
		if run.Result != backupUploaded {
			return false, fmt.Errorf("the host's final backup %s of %s %s, so nothing was removed; the host retries it, and a later unsubscribe removes it once it uploads", request, f.Kept, run.Result)
		}
		return true, nil
	}
	return false, handler.Wait("the host has not yet uploaded the final backup %s of %s, which is kept until it does", request, f.Kept)
}

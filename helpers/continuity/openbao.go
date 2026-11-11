package continuity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	snapshotPath      = "/v1/sys/storage/raft/snapshot"
	snapshotForcePath = "/v1/sys/storage/raft/snapshot-force"
	healthPath        = "/v1/sys/health"
	tokenHeader       = "X-Vault-Token"
)

// Endpoint is one OpenBao server and the token that may read or restore its raft snapshot.
type Endpoint struct {
	Addr   string
	Token  string
	Client *http.Client
}

// do names only the fixed path in its errors, since the token and address are not worth echoing.
func (e Endpoint) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(e.Addr, "/")+path, body)
	if err != nil {
		return nil, fmt.Errorf("openbao %s %s: the address is not a URL", method, path)
	}
	req.Header.Set(tokenHeader, e.Token)
	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: transferTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openbao %s %s: %w", method, path, scrub(err))
	}
	return resp, nil
}

func (e Endpoint) doOK(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	resp, err := e.do(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, fmt.Errorf("openbao %s %s answered %s", method, path, resp.Status)
	}
	return resp, nil
}

// OpenBao snapshots Live's raft storage, checks a snapshot by restoring it into Scratch, and restarts every member of Live through Restart after a force restore.
type OpenBao struct {
	Live, Scratch Endpoint
	Restart       func(ctx context.Context) error
}

func (o OpenBao) Export(ctx context.Context, w io.Writer) error {
	resp, err := o.Live.doOK(ctx, http.MethodGet, snapshotPath, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(w, resp.Body)
	return err
}

func (o OpenBao) Verify(ctx context.Context, r io.Reader) error {
	resp, err := o.Scratch.doOK(ctx, http.MethodPost, snapshotForcePath, r)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return o.Scratch.checkInitialized(ctx)
}

// checkInitialized reads the body of every health answer, since a standby or sealed server answers a non-2xx status with the same fields.
func (e Endpoint) checkInitialized(ctx context.Context) error {
	resp, err := e.do(ctx, http.MethodGet, healthPath, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var health struct {
		Initialized bool `json:"initialized"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return fmt.Errorf("openbao GET %s answered %s with no readable health", healthPath, resp.Status)
	}
	if !health.Initialized {
		return errors.New("the scratch OpenBao reports itself uninitialized after the restore")
	}
	return nil
}

func (o OpenBao) Import(ctx context.Context, r io.Reader) error {
	if o.Restart == nil {
		return errors.New("OpenBao.Restart is not set, and every member must restart after a force restore")
	}
	resp, err := o.Live.doOK(ctx, http.MethodPost, snapshotForcePath, r)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if err := o.Restart(ctx); err != nil {
		return fmt.Errorf("restarting the members after the restore: %w", err)
	}
	return nil
}

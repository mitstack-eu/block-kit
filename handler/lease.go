package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
)

const classLeaseNotListed = "lease-not-listed"

// lease serves a block whose manifest lists the operation, and refuses every other block. A request with no lease id only retires, so the credential the block returns for it is dropped.
func (h *Handler) lease(ctx context.Context, req contract.Request, secrets []string) (int, contract.Response) {
	if h.leaser == nil {
		return h.errorResponse(Refuse(classLeaseNotListed,
			"this block's manifest does not list %s, so it mints and retires no lease", contract.OperationLease), secrets)
	}
	asked := req.Lease()
	minted, err := h.leaser.Lease(ctx, req.Desired(), asked)
	if err != nil {
		return h.errorResponse(err, secrets)
	}
	if asked.ID == "" {
		return http.StatusOK, contract.Response{}
	}
	if err := checkLeased(minted, h.now(), secrets); err != nil {
		return h.errorResponse(fmt.Errorf("handler: the block's lease %q of %q: %w", asked.ID, asked.Resource, err), secrets)
	}
	return http.StatusOK, contract.Response{Lease: &minted}
}

// checkLeased refuses a lease the caller could not use or that lives past contract.MaxLeaseLife, and one repeating a secret the request carried: a value equal to a secret is that secret, while a signed link that merely names the key id that signed it is not.
func checkLeased(l contract.Lease, now time.Time, secrets []string) error {
	if len(l.Credential) == 0 {
		return errors.New("it holds no credential")
	}
	var credential map[string]any
	if err := json.Unmarshal(l.Credential, &credential); err != nil || len(credential) == 0 {
		return errors.New("its credential is not a non-empty JSON object")
	}
	switch {
	case l.ExpiresAt.IsZero():
		return errors.New("it has no expiry")
	case !l.ExpiresAt.After(now):
		return fmt.Errorf("its expiry %s has already passed", l.ExpiresAt.Format(time.RFC3339))
	case l.ExpiresAt.After(now.Add(contract.MaxLeaseLife)):
		return fmt.Errorf("its expiry %s is more than %s ahead", l.ExpiresAt.Format(time.RFC3339), contract.MaxLeaseLife)
	}
	if path, repeated := secretValueIn(credential, "credential", secrets); repeated {
		return fmt.Errorf("its value at %s repeats a secret the request carried", path)
	}
	return nil
}

// secretValueIn walks a decoded JSON value and names the path of the first string equal to one of secrets, skipping the empty secret of an unset optional input.
func secretValueIn(value any, path string, secrets []string) (string, bool) {
	switch v := value.(type) {
	case string:
		return path, v != "" && slices.Contains(secrets, v)
	case map[string]any:
		for key, child := range v {
			if found, ok := secretValueIn(child, path+"."+key, secrets); ok {
				return found, true
			}
		}
	case []any:
		for i, child := range v {
			if found, ok := secretValueIn(child, fmt.Sprintf("%s[%d]", path, i), secrets); ok {
				return found, true
			}
		}
	}
	return "", false
}

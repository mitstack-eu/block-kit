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
	// A retire-only request has no lease to return, so whatever the block answers for it is dropped.
	if asked.ID == "" {
		return http.StatusOK, contract.Response{}
	}
	if err := checkLeased(minted, h.now(), secrets); err != nil {
		err = fmt.Errorf("handler: the block's lease %q of %q: %w", asked.ID, asked.Resource, err)
		return h.errorResponse(errors.Join(err, h.retireRefused(ctx, req, minted)), secrets)
	}
	return http.StatusOK, contract.Response{Lease: &minted}
}

// retireRefused deletes a credential the kit refuses to pass on, since the platform records only what a 200 carries and could otherwise never find it.
func (h *Handler) retireRefused(ctx context.Context, req contract.Request, refused contract.Lease) error {
	if refused.ID == "" {
		return nil
	}
	retire := req.Lease()
	retire.ID, retire.Want, retire.Retire = "", nil, []string{refused.ID}
	if _, err := h.leaser.Lease(ctx, req.Desired(), retire); err != nil {
		// %v keeps a Refused the retire returns from deciding the status of the refusal being answered.
		return fmt.Errorf("handler: retiring the refused credential %q: %v", refused.ID, err)
	}
	return nil
}

// checkLeased matches a secret by exact value, because a signed link that merely names the key id that signed it is not that key.
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
	if !holdsAValue(credential) {
		return errors.New("its credential holds no value")
	}
	if path, repeated := secretValueIn(credential, "credential", secrets); repeated {
		return fmt.Errorf("its value at %s repeats a secret the request carried", path)
	}
	if l.ID != "" && slices.Contains(secrets, l.ID) {
		return errors.New("its id repeats a secret the request carried")
	}
	return nil
}

// holdsAValue is true when a decoded JSON value has a string that is not empty somewhere inside it.
func holdsAValue(value any) bool {
	switch v := value.(type) {
	case string:
		return v != ""
	case map[string]any:
		for _, child := range v {
			if holdsAValue(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if holdsAValue(child) {
				return true
			}
		}
	}
	return false
}

// secretValueIn skips an empty string because an unset optional input leaves an empty secret.
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

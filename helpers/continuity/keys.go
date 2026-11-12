package continuity

import (
	"context"
	"fmt"

	"github.com/mitstack-eu/block-kit/helpers/age"
)

// KeyName is the own secret a unit's age identity is kept under.
func KeyName(unit string) string { return "continuityIdentity." + unit }

// MintKeys returns, for a block to answer as own secrets, an identity for each unit that holds none in held, and never replaces a held one, since a snapshot taken on it would no longer decrypt.
func MintKeys(held map[string]string, units ...string) (map[string]string, error) {
	minted := map[string]string{}
	for _, unit := range units {
		if held[KeyName(unit)] != "" {
			continue
		}
		identity, err := age.GenerateIdentity()
		if err != nil {
			return nil, fmt.Errorf("minting the key of %q: %w", unit, err)
		}
		minted[KeyName(unit)] = identity.String()
	}
	return minted, nil
}

// OwnSecretKeys reads each unit's key from the block's own secrets, as a request's Desired.OwnSecrets carries them.
type OwnSecretKeys struct {
	OwnSecrets map[string]string
}

func (k OwnSecretKeys) Identity(_ context.Context, unit string) (string, error) {
	identity := k.OwnSecrets[KeyName(unit)]
	if identity == "" {
		return "", fmt.Errorf("the block's own secrets hold no key under %q", KeyName(unit))
	}
	return identity, nil
}

func (k OwnSecretKeys) Recipient(ctx context.Context, unit string) (string, error) {
	raw, err := k.Identity(ctx, unit)
	if err != nil {
		return "", err
	}
	identity, err := age.ParseIdentity(raw)
	if err != nil {
		return "", fmt.Errorf("the own secret %q: %w", KeyName(unit), err)
	}
	return identity.Recipient().String(), nil
}

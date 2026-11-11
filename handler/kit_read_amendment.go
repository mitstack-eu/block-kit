package handler

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/mitstack-eu/block-kit/contract"
)

// KitReadAmender, when a block-level Resource implements it, is handed each answer the kit observer gives, probe or full read, and its amendment may add what the kit observer cannot read but never turn any answer absent.
type KitReadAmender interface {
	AmendKitRead(ctx context.Context, r contract.ResourceRef, obs contract.Observation) (KitReadAmendment, error)
}

// KitReadAmendment adds outputs its type declares to a present answer, or holds any answer unknown for the reason Hold gives, and the handler refuses outputs beside a hold or beside an answer that is not present.
type KitReadAmendment struct {
	Outputs       map[string]string
	SecretOutputs map[string]string
	Hold          string
}

// amendKitRead applies the block's amendment to each kit observer answer that did not error.
func (r *declaredRun) amendKitRead(ctx context.Context, name string, t contract.SnapshotType, ref contract.ResourceRef, obs contract.Observation, readErr error) (contract.Observation, error) {
	if readErr != nil || r.h.kitReadAmender == nil {
		return obs, readErr
	}
	handed := obs
	handed.Outputs, handed.SecretOutputs, handed.Identity = maps.Clone(obs.Outputs), maps.Clone(obs.SecretOutputs), maps.Clone(obs.Identity)
	a, err := r.h.kitReadAmender.AmendKitRead(ctx, ref, handed)
	if err != nil {
		return contract.Observation{}, err
	}
	if len(a.Outputs) == 0 && len(a.SecretOutputs) == 0 {
		return hold(name, obs, a.Hold), nil
	}
	if obs.State != contract.Present || a.Hold != "" {
		return contract.Observation{}, fmt.Errorf("handler: the block's kit read amendment adds outputs to resource %q, which the kit observer read %s, and outputs join a present answer alone that the block does not hold", name, obs.State)
	}
	if err := checkAddedOutputs(name, t, obs, a); err != nil {
		return contract.Observation{}, err
	}
	obs.Outputs = mergeOutputs(obs.Outputs, a.Outputs)
	obs.SecretOutputs = mergeOutputs(obs.SecretOutputs, a.SecretOutputs)
	return obs, nil
}

// hold leaves scrubbing the detail to observed, which every answer passes through.
func hold(name string, obs contract.Observation, reason string) contract.Observation {
	switch {
	case reason == "":
		return obs
	case obs.State == contract.Unknown:
		obs.StatusDetail = fmt.Sprintf("%s; resource %q is held: %s", obs.StatusDetail, name, reason)
		return obs
	}
	return contract.Observation{State: contract.Unknown, Identity: obs.Identity, StatusDetail: fmt.Sprintf("resource %q reads %s, but %s", name, obs.State, reason)}
}

func checkAddedOutputs(name string, t contract.SnapshotType, obs contract.Observation, a KitReadAmendment) error {
	for _, added := range []struct {
		values map[string]string
		secret bool
	}{{a.Outputs, false}, {a.SecretOutputs, true}} {
		for _, key := range slices.Sorted(maps.Keys(added.values)) {
			declared, ok := t.Output(key)
			_, read := obs.Outputs[key]
			_, readSecret := obs.SecretOutputs[key]
			switch {
			case !ok:
				return fmt.Errorf("handler: the block's kit read amendment adds output %q to resource %q, which its type %s does not declare", key, name, t.ID)
			case declared.Secret != added.secret:
				return fmt.Errorf("handler: the block's kit read amendment adds output %q to resource %q as %s, and its type %s declares it %s", key, name, secrecy(added.secret), t.ID, secrecy(declared.Secret))
			case read || readSecret:
				return fmt.Errorf("handler: the block's kit read amendment adds output %q to resource %q, which the kit observer already reads", key, name)
			}
		}
	}
	return nil
}

func secrecy(secret bool) string {
	if secret {
		return "secret"
	}
	return "not secret"
}

package main

import (
	"context"
	"fmt"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/observe"
	"github.com/mitstack-eu/block-kit/webhook"
)

func newConfig(cloud *hetzner, logs *webhook.LogBuffer) (handler.Config, error) {
	types, err := contract.ParseTypeSnapshot(typeSnapshot)
	if err != nil {
		return handler.Config{}, fmt.Errorf("reading type-snapshot.json: %w", err)
	}
	return handler.Config{
		Resource:  block{cloud: cloud},
		Observers: map[string]handler.Observer{firewallResource: firewallObserver{HetznerFirewall: observe.HetznerFirewall{API: cloud}, cloud: cloud}},
		Manifest:  manifest,
		Types:     types,
		Logs:      logs,
	}, nil
}

// firewallObserver reads the firewall by its id, and by the subscription's label when an unsubscribe finds no id, as a failed create leaves it.
type firewallObserver struct {
	observe.HetznerFirewall
	cloud *hetzner
}

func (o firewallObserver) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	if !r.Unsubscribing || r.Identity["firewallId"] != "" {
		return o.HetznerFirewall.ObserveResource(ctx, r)
	}
	held, err := o.cloud.ListByLabel(ctx, r.Desired, subscriptionSelector(r.Desired))
	switch {
	case err != nil:
		return contract.Observation{}, err
	case len(held) == 0:
		return contract.Observation{State: contract.Absent}, nil
	}
	return contract.Observation{State: contract.Present, Identity: map[string]string{"firewallId": held[0].ID}}, nil
}

package main

import (
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
		Observers: map[string]handler.Observer{firewallResource: observe.HetznerFirewall{API: cloud}},
		Manifest:  manifest,
		Types:     types,
		Logs:      logs,
	}, nil
}

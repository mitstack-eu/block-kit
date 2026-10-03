// Command catalogue-inputs generates the input definitions of every catalogue
// type that maps one to one onto an OpenTofu provider resource, from the JSON
// `tofu providers schema -json` prints. The schema gives each input's name,
// whether it is required and whether it is sensitive; it carries no
// replacement rule, so each type's ProviderResource.ReplaceOn names the inputs
// whose change replaces the resource.
//
// Usage:
//
//	go run ./cmd/catalogue-inputs -schema <schema.json> > contract/provider-inputs.json
//
// It exists so the inputs of a one-to-one type come from its provider's schema
// rather than being written by hand. It prints a JSON object mapping each such
// type ID to its inputs, which the contract package embeds.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"

	"github.com/mitstack-eu/operator-kit/contract"
)

func main() {
	os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr))
}

// Run parses args, reads the schema file -schema names, and prints the
// generated inputs of every one-to-one type to stdout. It returns 2 for a
// usage error, 1 when the schema cannot be read or lacks a mapped resource,
// and 0 otherwise. Nothing reaches stdout unless every type generates.
func Run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("catalogue-inputs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	schemaPath := fs.String("schema", "", "path to the JSON `tofu providers schema -json` prints")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *schemaPath == "" {
		fmt.Fprintln(stderr, "usage: catalogue-inputs -schema <schema.json>")
		return 2
	}
	raw, err := os.ReadFile(*schemaPath)
	if err != nil {
		fmt.Fprintf(stderr, "catalogue-inputs: %v\n", err)
		return 1
	}
	generated := map[string][]contract.Input{}
	for _, rt := range contract.ResourceTypes() {
		pr := rt.ProviderResource
		if pr == nil {
			continue
		}
		inputs, err := GenerateInputs(raw, *pr)
		if err != nil {
			fmt.Fprintf(stderr, "catalogue-inputs: %s maps onto %s %s: %v\n", rt.ID, pr.Provider, pr.Resource, err)
			return 1
		}
		generated[rt.ID] = inputs
	}
	out, err := json.MarshalIndent(generated, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "catalogue-inputs: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s\n", out)
	return 0
}

// providerSchemas is the part of `tofu providers schema -json` this command
// reads.
type providerSchemas struct {
	ProviderSchemas map[string]struct {
		ResourceSchemas map[string]struct {
			Block schemaBlock `json:"block"`
		} `json:"resource_schemas"`
	} `json:"provider_schemas"`
}

// schemaBlock is one block of a resource schema: its attributes and its
// nested block types.
type schemaBlock struct {
	Attributes map[string]schemaAttribute `json:"attributes"`
	BlockTypes map[string]schemaBlockType `json:"block_types"`
}

// schemaAttribute is what this command reads of one attribute.
type schemaAttribute struct {
	Required   bool `json:"required"`
	Optional   bool `json:"optional"`
	Sensitive  bool `json:"sensitive"`
	Deprecated bool `json:"deprecated"`
}

// schemaBlockType is one nested block type.
type schemaBlockType struct {
	MinItems int         `json:"min_items"`
	Block    schemaBlock `json:"block"`
}

// sensitive reports whether any attribute of b, or of a block nested in it,
// is sensitive.
func (b schemaBlock) sensitive() bool {
	for _, a := range b.Attributes {
		if a.Sensitive {
			return true
		}
	}
	for _, bt := range b.BlockTypes {
		if bt.Block.sensitive() {
			return true
		}
	}
	return false
}

// GenerateInputs returns the inputs of the provider resource pr names, read
// from schemaJSON and sorted by name. An input is every attribute marked
// required or optional except id and a deprecated attribute, and every block
// type except timeouts; a block is required when it needs at least one item
// and secret when any attribute in it is sensitive. Each input accepts a
// value, and replaces the resource exactly when pr.ReplaceOn names it.
func GenerateInputs(schemaJSON []byte, pr contract.ProviderResource) ([]contract.Input, error) {
	var doc providerSchemas
	if err := json.Unmarshal(schemaJSON, &doc); err != nil {
		return nil, fmt.Errorf("reading the provider schema: %w", err)
	}
	if doc.ProviderSchemas == nil {
		return nil, errors.New("the document has no provider_schemas, so it is not what tofu providers schema -json prints")
	}
	provider, ok := doc.ProviderSchemas[pr.Provider]
	if !ok {
		return nil, fmt.Errorf("the schema holds no provider %q", pr.Provider)
	}
	resource, ok := provider.ResourceSchemas[pr.Resource]
	if !ok {
		return nil, fmt.Errorf("provider %q holds no resource %q", pr.Provider, pr.Resource)
	}

	byName := map[string]contract.Input{}
	for name, a := range resource.Block.Attributes {
		if name == "id" || a.Deprecated || !(a.Required || a.Optional) {
			continue
		}
		byName[name] = contract.Input{Name: name, Required: a.Required, Secret: a.Sensitive}
	}
	for name, bt := range resource.Block.BlockTypes {
		if name == "timeouts" {
			continue
		}
		byName[name] = contract.Input{Name: name, Required: bt.MinItems >= 1, Secret: bt.Block.sensitive()}
	}
	for _, name := range pr.ReplaceOn {
		if _, ok := byName[name]; !ok {
			return nil, fmt.Errorf("%s: replaceOn names %q, which is no input of the resource, so nothing can replace on it", pr.Resource, name)
		}
	}

	inputs := make([]contract.Input, 0, len(byName))
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		in := byName[name]
		in.Accepts = "value"
		in.Replacement = "update"
		if slices.Contains(pr.ReplaceOn, name) {
			in.Replacement = "replace"
		}
		inputs = append(inputs, in)
	}
	return inputs, nil
}

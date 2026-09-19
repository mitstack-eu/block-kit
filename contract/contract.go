// Package contract holds the wire contract between the engine and a block:
// the envelope schema a block receives, the response schema it must answer
// with, and the golden fixtures both halves are checked against. This
// module owns the contract; the engine and every block consume it from
// here rather than keeping their own copy.
package contract

import (
	"embed"
	"fmt"
	"strings"
)

// EnvelopeSchema is the JSON Schema for a lifecycle webhook envelope the
// engine sends to a block.
//
//go:embed envelope.schema.json
var EnvelopeSchema []byte

// ResponseSchema is the JSON Schema for the response a block must return
// from a lifecycle webhook.
//
//go:embed response.schema.json
var ResponseSchema []byte

// GoldensDir is the directory inside Goldens holding the committed request
// envelope fixtures.
const GoldensDir = "goldens"

// Goldens embeds the committed request envelope fixtures: well-formed
// envelopes a block must accept, and envelopes prefixed InvalidPrefix that
// it must reject.
//
//go:embed goldens/*.json
var Goldens embed.FS

// ResponseGoldensDir is the directory inside ResponseGoldens holding the
// committed response fixtures.
const ResponseGoldensDir = "response-goldens"

// ResponseGoldens embeds the committed response fixtures: well-formed
// responses the engine must decode intact, and responses prefixed
// InvalidPrefix that its decoder must reject.
//
//go:embed response-goldens/*.json
var ResponseGoldens embed.FS

// InvalidPrefix marks a golden file name as belonging to the malformed
// half of a fixture set.
const InvalidPrefix = "invalid-"

// splitGoldens reads every ".json" file in dir from fsys and splits it into
// a valid half (names without InvalidPrefix) and an invalid half (names
// with it), each keyed by file name without the ".json" suffix.
func splitGoldens(fsys embed.FS, dir string) (valid, invalid map[string][]byte, err error) {
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("contract: reading %s: %w", dir, err)
	}

	valid = map[string][]byte{}
	invalid = map[string][]byte{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		raw, err := fsys.ReadFile(dir + "/" + name)
		if err != nil {
			return nil, nil, fmt.Errorf("contract: reading %s/%s: %w", dir, name, err)
		}
		key := strings.TrimSuffix(name, ".json")
		if strings.HasPrefix(key, InvalidPrefix) {
			invalid[key] = raw
		} else {
			valid[key] = raw
		}
	}
	return valid, invalid, nil
}

// ValidGoldens returns the request envelope goldens a block must accept,
// keyed by file name without the ".json" suffix. It errors rather than
// returning an empty map if the valid half is empty, since a vacuous
// acceptance set proves nothing.
func ValidGoldens() (map[string][]byte, error) {
	valid, _, err := splitGoldens(Goldens, GoldensDir)
	if err != nil {
		return nil, err
	}
	if len(valid) == 0 {
		return nil, fmt.Errorf("contract: no valid goldens found under %s", GoldensDir)
	}
	return valid, nil
}

// InvalidGoldens returns the request envelope goldens a block must reject,
// keyed by file name without the ".json" suffix. It errors rather than
// returning an empty map if the invalid half is empty, since a vacuous
// rejection set proves nothing.
func InvalidGoldens() (map[string][]byte, error) {
	_, invalid, err := splitGoldens(Goldens, GoldensDir)
	if err != nil {
		return nil, err
	}
	if len(invalid) == 0 {
		return nil, fmt.Errorf("contract: no invalid goldens found under %s", GoldensDir)
	}
	return invalid, nil
}

// ValidResponseGoldens returns the response goldens the engine must decode
// intact, keyed by file name without the ".json" suffix. It errors rather
// than returning an empty map if the valid half is empty, since a vacuous
// acceptance set proves nothing.
func ValidResponseGoldens() (map[string][]byte, error) {
	valid, _, err := splitGoldens(ResponseGoldens, ResponseGoldensDir)
	if err != nil {
		return nil, err
	}
	if len(valid) == 0 {
		return nil, fmt.Errorf("contract: no valid response goldens found under %s", ResponseGoldensDir)
	}
	return valid, nil
}

// InvalidResponseGoldens returns the response goldens the engine's decoder
// must reject, keyed by file name without the ".json" suffix. It errors
// rather than returning an empty map if the invalid half is empty, since a
// vacuous rejection set proves nothing.
func InvalidResponseGoldens() (map[string][]byte, error) {
	_, invalid, err := splitGoldens(ResponseGoldens, ResponseGoldensDir)
	if err != nil {
		return nil, err
	}
	if len(invalid) == 0 {
		return nil, fmt.Errorf("contract: no invalid response goldens found under %s", ResponseGoldensDir)
	}
	return invalid, nil
}

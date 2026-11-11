// Package delivery_test pins the Record to the goldens the platform's CI dispatch tests also assert, so editing either side alone breaks a test.
package delivery_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/delivery"
)

func TestRecord_EachGoldenReadsAsItsOutcomeAndRoundTripsByteForByte(t *testing.T) {
	cases := []struct{ golden, conclusion, phase string }{
		{"delivery.golden.json", delivery.ConclusionSuccess, delivery.PhaseHealthy},
		{"delivery-mitstack-failure.golden.json", delivery.ConclusionMitstackFailure, delivery.PhaseBuilding},
	}
	for _, c := range cases {
		t.Run(c.golden, func(t *testing.T) {
			golden, err := os.ReadFile(filepath.Join("testdata", c.golden))
			if err != nil {
				t.Fatalf("read golden fixture %s: %v", c.golden, err)
			}

			var got delivery.Record
			decodeErr := json.Unmarshal(golden, &got)
			out, encodeErr := json.Marshal(got)

			if decodeErr != nil || encodeErr != nil || got.BuildResult == nil {
				t.Fatalf("decode %s = %v, encode = %v, buildResult = %v; want no error and a build result", c.golden, decodeErr, encodeErr, got.BuildResult)
			}
			if got.BuildResult.Conclusion != c.conclusion || got.Phase != c.phase {
				t.Errorf("%s reads as conclusion %q, phase %q; want %q, %q", c.golden, got.BuildResult.Conclusion, got.Phase, c.conclusion, c.phase)
			}
			if want := bytes.TrimRight(golden, "\n"); !bytes.Equal(out, want) {
				t.Errorf("%s round trip = %s\nwant        = %s", c.golden, out, want)
			}
		})
	}
}

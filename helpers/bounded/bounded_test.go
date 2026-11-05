package bounded_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/bounded"
)

func TestRead(t *testing.T) {
	cases := []struct {
		name, body, want, errHas string
		status                   int
	}{
		{"a 2xx answer at its bound reads whole", "12345678", "12345678", "", http.StatusOK},
		{"a 2xx answer a byte past its bound is refused naming the call and the bound", "123456789", "", "GET /thing: the answer is longer than 8 bytes", http.StatusOK},
		{"a refusal past the bound is cut to the bound, so its status still reaches the caller", "123456789", "12345678", "", http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := bounded.Read(&http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, "GET /thing", 8)

			if string(got) != tc.want || (tc.errHas == "") != (err == nil) || (err != nil && err.Error() != tc.errHas) {
				t.Errorf("Read = %q, %v; want %q and error %q", got, err, tc.want, tc.errHas)
			}
		})
	}
}

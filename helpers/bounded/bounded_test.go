package bounded_test

// Read returns an answer at its bound whole, refuses a 2xx answer a byte past it by naming the call and the bound, and cuts a refusal's text to the bound.

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/mitstack-eu/block-kit/helpers/bounded"
)

func TestRead(t *testing.T) {
	const limit = 8
	cases := []struct {
		name         string
		status       int
		body         string
		want, errHas string
	}{
		{"a 2xx answer at its bound reads whole", http.StatusOK, "12345678", "12345678", ""},
		{"a 2xx answer a byte past its bound is refused naming the call and the bound", http.StatusOK, "123456789", "", "GET /thing: the answer is longer than 8 bytes"},
		{"a refusal past the bound is cut to the bound, so its status still reaches the caller", http.StatusInternalServerError, "123456789", "12345678", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}

			got, err := bounded.Read(resp, "GET /thing", limit)

			if string(got) != tc.want || (tc.errHas == "") != (err == nil) || (err != nil && err.Error() != tc.errHas) {
				t.Errorf("Read = %q, %v; want %q and error %q", got, err, tc.want, tc.errHas)
			}
		})
	}
}

func TestRead_NamesTheCallWhenTheReadFails(t *testing.T) {
	broken := errors.New("connection reset")
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(iotest.ErrReader(broken))}

	_, err := bounded.Read(resp, "GET /thing", 8)

	if !errors.Is(err, broken) || !strings.Contains(err.Error(), "GET /thing") {
		t.Errorf("Read = %v; want an error wrapping %v and naming GET /thing", err, broken)
	}
}

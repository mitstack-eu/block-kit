package cloud

import "net/http"

// HoldTransport is h's transport sending through next, so a test can hand the hold a call that panics.
func HoldTransport(h *Holds, next http.RoundTripper) http.RoundTripper {
	return &holdTransport{holds: h, next: next}
}

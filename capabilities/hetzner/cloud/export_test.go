package cloud

import "net/http"

func HoldTransport(h *Holds, next http.RoundTripper) http.RoundTripper {
	return &holdTransport{holds: h, next: next}
}

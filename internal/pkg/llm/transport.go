package llm

import (
	"crypto/tls"
	"net/http"
)

// NewHTTPTransport creates a provider-scoped transport without changing the
// process-wide TLS policy. insecure is an explicit administrator opt-in for
// trusted networks that use self-signed certificates.
func NewHTTPTransport(insecure bool) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- explicit per-provider administrator opt-in
	}
	return transport
}

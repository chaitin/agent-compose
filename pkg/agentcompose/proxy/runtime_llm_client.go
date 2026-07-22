package proxy

import (
	"net/http"
	"time"
)

// NewRuntimeLLMHTTPClient creates an upstream client that bounds the wait for
// response headers without imposing a total deadline on streaming responses.
func NewRuntimeLLMHTTPClient(responseHeaderTimeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	return &http.Client{Transport: transport}
}

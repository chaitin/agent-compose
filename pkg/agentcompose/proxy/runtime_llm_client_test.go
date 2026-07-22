package proxy

import (
	"net/http"
	"testing"
	"time"
)

func TestNewRuntimeLLMHTTPClientDoesNotLimitStreamingResponseDuration(t *testing.T) {
	timeout := 45 * time.Second
	client := NewRuntimeLLMHTTPClient(timeout)

	if client.Timeout != 0 {
		t.Fatalf("client timeout = %s, want no total request timeout", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.ResponseHeaderTimeout != timeout {
		t.Fatalf("response header timeout = %s, want %s", transport.ResponseHeaderTimeout, timeout)
	}
}

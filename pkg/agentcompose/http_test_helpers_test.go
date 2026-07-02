package agentcompose

import (
	"io"
	"net/http"
	"testing"
)

func readRequestBodyForTest(t *testing.T, r *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("ReadAll request body returned error: %v", err)
	}
	return string(body)
}

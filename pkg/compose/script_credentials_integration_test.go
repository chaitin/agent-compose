package compose

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIntegrationSchedulerScriptCredentialsResolveOnCLI(t *testing.T) {
	t.Setenv("SCRIPT_CLI_TOKEN", "different-process-value")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cli-dotenv-token" {
			t.Error("script fetch did not use the explicit CLI environment")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if _, err := fmt.Fprint(w, "function main() {}"); err != nil {
			t.Errorf("write script: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	spec := mustParseCompose(t, fmt.Sprintf(`
name: cli-credentials
agents:
  reviewer:
    scheduler:
      script:
        provider: http
        url: %s
        token: ${SCRIPT_CLI_TOKEN}
`, server.URL))
	normalized, err := Normalize(spec, NormalizeOptions{
		Context:           t.Context(),
		ResolveScriptURLs: true,
		Env:               map[string]string{"SCRIPT_CLI_TOKEN": "cli-dotenv-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := normalized.Agents[0].Scheduler.Script; got != "function main() {}" {
		t.Fatalf("script snapshot = %q", got)
	}
}

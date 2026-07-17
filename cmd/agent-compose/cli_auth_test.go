package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"google.golang.org/protobuf/proto"
)

func TestCLIAuthLoginPersistsAndReusesToken(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yml")
	t.Setenv("AGENT_COMPOSE_CONFIG", configPath)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == agentcomposev2connect.AuthServiceWhoAmIProcedure {
			body, err := proto.Marshal(&agentcomposev2.WhoAmIResponse{
				Token: &agentcomposev2.AuthToken{Name: "test-admin"}, Role: "admin", Origin: "issued", AuthenticationInitialized: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			w.Header().Set("Content-Type", "application/proto")
			_, _ = w.Write(body)
			return
		}
		_, _ = io.WriteString(w, `{"err":null,"msg":"OK","data":{"timestamp":1783501631,"version":"test"}}`)
	}))
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("auth", "login", "--host", server.URL, "--token", "secret-token")
	if exitCode != 0 || stderr != "" || !strings.Contains(stdout, "Authenticated "+server.URL) {
		t.Fatalf("login stdout/stderr/code = %q / %q / %d", stdout, stderr, exitCode)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o, want 600", info.Mode().Perm())
	}

	stdout, stderr, _, exitCode = executeCLICommand("status", "--host", server.URL)
	if exitCode != 0 || stderr != "" || !strings.Contains(stdout, "test") {
		t.Fatalf("status stdout/stderr/code = %q / %q / %d", stdout, stderr, exitCode)
	}

	stdout, stderr, _, exitCode = executeCLICommand("auth", "ls")
	wantList := "Authenticated Agent-Compose sites:\n- " + server.URL + "\n"
	if exitCode != 0 || stderr != "" || stdout != wantList {
		t.Fatalf("list stdout/stderr/code = %q / %q / %d", stdout, stderr, exitCode)
	}

	stdout, stderr, _, exitCode = executeCLICommand("auth", "logout", "--host", server.URL)
	if exitCode != 0 || stderr != "" || !strings.Contains(stdout, "Logged out "+server.URL) {
		t.Fatalf("logout stdout/stderr/code = %q / %q / %d", stdout, stderr, exitCode)
	}
}

func TestCLIAuthListReportsWhenNoSitesAreAuthenticated(t *testing.T) {
	t.Setenv("AGENT_COMPOSE_CONFIG", filepath.Join(t.TempDir(), "config.yml"))

	stdout, stderr, _, exitCode := executeCLICommand("auth", "ls")
	want := "No authenticated Agent-Compose sites.\n"
	if exitCode != 0 || stderr != "" || stdout != want {
		t.Fatalf("list stdout/stderr/code = %q / %q / %d, want %q / empty / 0", stdout, stderr, exitCode, want)
	}
}

func TestCLIAuthLoginFailureDoesNotPersistToken(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yml")
	t.Setenv("AGENT_COMPOSE_CONFIG", configPath)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"err":"Unknown","msg":"daemon authentication required, code=401, message=daemon authentication required"}`)
	}))
	defer server.Close()

	_, stderr, _, exitCode := executeCLICommand("auth", "login", "--host", server.URL, "--token", "wrong")
	if exitCode == 0 {
		t.Fatal("login exit code = 0, want failure")
	}
	wantError := "authentication failed for " + server.URL + ": token was rejected\n"
	if stderr != wantError {
		t.Fatalf("login stderr = %q, want %q", stderr, wantError)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config stat error = %v, want not exist", err)
	}
}

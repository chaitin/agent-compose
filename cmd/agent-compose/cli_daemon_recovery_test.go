package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samber/do/v2"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
)

func TestAPIVersionContractUnchangedWhileDeletionRecoveryIsBlocked(t *testing.T) {
	root := t.TempDir()
	const sandboxID = "blocked"
	if err := sandboxes.WriteOwnershipRecord(root, sandboxes.OwnershipRecord{
		SandboxID: sandboxID, SandboxPath: filepath.Join(root, sandboxID), LifecycleState: "deleting",
	}); err != nil {
		t.Fatalf("write deletion journal: %v", err)
	}
	runtime := &daemonRecoveryRuntime{started: make(chan struct{}, 1)}
	recovery := sandboxes.NewDeletionRecovery(&sandboxes.RemovalCoordinator{
		SandboxRoot: root,
		Store:       daemonRecoveryStore{},
		Runtime:     runtime,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recoveryCtx, cancelRecovery := context.WithCancel(context.Background())
	defer cancelRecovery()
	if err := recovery.Start(recoveryCtx); err != nil {
		t.Fatalf("start deletion recovery: %v", err)
	}
	select {
	case <-runtime.started:
	case <-time.After(10 * time.Second):
		t.Fatal("blocked deletion did not start")
	}

	di := do.New()
	do.ProvideValue(di, &appconfig.Config{Version: "recovery-test"})
	do.ProvideValue(di, recovery)
	app, err := NewEcho(di)
	if err != nil {
		t.Fatalf("NewEcho returned error: %v", err)
	}
	server := httptest.NewServer(app)
	defer server.Close()
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(server.URL + "/api/version")
	if err != nil {
		t.Fatalf("GET /api/version while recovery blocked: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/version status = %d, want 200", response.StatusCode)
	}
	var status struct {
		Msg  string                     `json:"msg"`
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := decodeJSONBody(response.Body, &status); err != nil {
		t.Fatalf("decode /api/version: %v", err)
	}
	if status.Msg != "OK" {
		t.Fatalf("/api/version message = %q, want compatibility value OK", status.Msg)
	}
	if _, exists := status.Data["deletion_recovery"]; exists {
		t.Fatal("/api/version unexpectedly exposes deletion recovery state")
	}
	wantKeys := []string{"version", "os", "arch", "compiled_drivers", "timestamp", "timezone", "timezone_offset"}
	for _, key := range wantKeys {
		if _, exists := status.Data[key]; !exists {
			t.Errorf("/api/version data is missing legacy field %q", key)
		}
	}
	if len(status.Data) != len(wantKeys) {
		t.Fatalf("/api/version data keys = %v, want legacy seven-field contract", status.Data)
	}

	cancelRecovery()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := recovery.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown deletion recovery: %v", err)
	}
}

func TestFetchDaemonVersionHasBoundedStatusTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()

	const timeout = 20 * time.Millisecond
	_, err := fetchDaemonVersionWithTimeout(context.Background(), cliClientConfig{
		BaseURL: server.URL, Source: "--host", SourceValue: server.URL,
	}, timeout)
	var exitErr commandExitError
	if !errors.As(err, &exitErr) || exitErr.Code != exitCodeUnavailable {
		t.Fatalf("status timeout error = %v, want unavailable command exit error", err)
	}
	for _, want := range []string{"daemon status request", "timed out after 20ms", server.URL} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("status timeout error = %q, want %q", err, want)
		}
	}
	if daemonStatusTimeout != 5*time.Second {
		t.Fatalf("daemon status timeout = %v, want 5s", daemonStatusTimeout)
	}
}

func decodeJSONBody(reader io.Reader, target any) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

type daemonRecoveryStore struct{}

func (daemonRecoveryStore) GetSandbox(_ context.Context, id string) (*domain.Sandbox, error) {
	return &domain.Sandbox{Summary: domain.SandboxSummary{ID: id}}, nil
}

func (daemonRecoveryStore) ListSandboxes(context.Context, domain.SandboxListOptions) (domain.SandboxListResult, error) {
	return domain.SandboxListResult{}, nil
}

func (daemonRecoveryStore) UpdateSandbox(context.Context, *domain.Sandbox) error { return nil }
func (daemonRecoveryStore) RemoveSandbox(context.Context, string) error          { return nil }

type daemonRecoveryRuntime struct {
	started chan struct{}
}

func (r *daemonRecoveryRuntime) StopSandboxVM(context.Context, *domain.Sandbox) error { return nil }

func (r *daemonRecoveryRuntime) RemoveSandboxVM(ctx context.Context, _ *domain.Sandbox) error {
	r.started <- struct{}{}
	<-ctx.Done()
	return ctx.Err()
}

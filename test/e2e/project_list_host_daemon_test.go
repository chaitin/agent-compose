package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func TestE2EProjectListSQLitePagination(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	repoRoot := e2eRepoRoot(t)
	testRoot, err := os.MkdirTemp(repoRoot, ".project-list-e2e-")
	if err != nil {
		t.Fatalf("create project list E2E root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(testRoot) })
	seedE2EProjectListDatabase(t, ctx, filepath.Join(testRoot, "data.db"), 205)
	binary := e2eDaemonBinary(t, ctx, repoRoot, testRoot)

	firstAddress := unusedLoopbackAddress(t)
	firstDaemon := startE2EDaemon(t, binary, repoRoot, testRoot, firstAddress, "debian:bookworm-slim")
	firstBaseURL := "http://" + firstAddress
	waitForE2EDaemon(t, ctx, firstDaemon, firstBaseURL)
	assertE2EProjectListResponses(t, ctx, binary, firstBaseURL)
	firstDaemon.stop(t)
	assertE2EDaemonReleased(t, firstDaemon, filepath.Join(testRoot, "agent-compose.sock"), firstAddress)

	secondAddress := unusedLoopbackAddress(t)
	secondDaemon := startE2EDaemon(t, binary, repoRoot, testRoot, secondAddress, "debian:bookworm-slim")
	secondBaseURL := "http://" + secondAddress
	waitForE2EDaemon(t, ctx, secondDaemon, secondBaseURL)
	client := newE2EHTTPClient()
	t.Cleanup(client.CloseIdleConnections)
	projectClient := agentcomposev2connect.NewProjectServiceClient(client, secondBaseURL)
	response, err := projectClient.ListProjects(ctx, connect.NewRequest(&agentcomposev2.ListProjectsRequest{Limit: 500}))
	if err != nil {
		t.Fatalf("ListProjects after daemon restart: %v", err)
	}
	if response.Msg.GetTotal() != 204 || len(response.Msg.GetProjects()) != 204 {
		t.Fatalf("project list after restart = total %d projects %d", response.Msg.GetTotal(), len(response.Msg.GetProjects()))
	}
	secondDaemon.stop(t)
	assertE2EDaemonReleased(t, secondDaemon, filepath.Join(testRoot, "agent-compose.sock"), secondAddress)
}

func seedE2EProjectListDatabase(t *testing.T, ctx context.Context, databasePath string, count int) {
	t.Helper()
	database, err := storagesqlite.Open(databasePath, 0)
	if err != nil {
		t.Fatalf("open project list database: %v", err)
	}
	store := configstore.FromDB(database.DB())
	for index := range count {
		id := fmt.Sprintf("project-%03d", index)
		name := id
		if index == 42 {
			name = "needle-" + id
		}
		project, err := store.UpsertProject(ctx, domain.ProjectRecord{
			ID: id, Name: name, SourcePath: filepath.Join("/projects", id, "agent-compose.yml"), SourceJSON: `{"kind":"local"}`,
		})
		if err != nil {
			_ = database.Close()
			t.Fatalf("seed project %s: %v", id, err)
		}
		revision, _, err := store.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{
			ProjectID: project.ID, SpecHash: "hash-" + id, SpecJSON: `{"agents":[]}`,
		})
		if err != nil {
			_ = database.Close()
			t.Fatalf("seed project revision %s: %v", id, err)
		}
		if _, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{
			ID: id + "-agent", ProjectID: id, AgentName: "worker", Revision: revision.Revision, Provider: "codex", SpecJSON: `{"name":"worker"}`,
		}); err != nil {
			_ = database.Close()
			t.Fatalf("seed project agent %s: %v", id, err)
		}
	}
	if _, err := store.MarkProjectRemoved(ctx, "project-001"); err != nil {
		_ = database.Close()
		t.Fatalf("mark E2E project removed: %v", err)
	}
	if _, err := database.DB().ExecContext(ctx, `UPDATE project SET created_at = 1000, updated_at = 1000 WHERE removed_at = 0`); err != nil {
		_ = database.Close()
		t.Fatalf("stabilize E2E project ordering: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close project list database: %v", err)
	}
}

func assertE2EProjectListResponses(t *testing.T, ctx context.Context, binary, baseURL string) {
	t.Helper()
	httpClient := newE2EHTTPClient()
	t.Cleanup(httpClient.CloseIdleConnections)
	projectClient := agentcomposev2connect.NewProjectServiceClient(httpClient, baseURL)

	all, err := projectClient.ListProjects(ctx, connect.NewRequest(&agentcomposev2.ListProjectsRequest{Limit: 500}))
	if err != nil {
		t.Fatalf("ListProjects large page: %v", err)
	}
	if all.Msg.GetTotal() != 204 || len(all.Msg.GetProjects()) != 204 {
		t.Fatalf("large project page = total %d projects %d", all.Msg.GetTotal(), len(all.Msg.GetProjects()))
	}
	if first := all.Msg.GetProjects()[0]; first.GetProjectId() != "project-000" || first.GetAgentCount() != 1 || first.GetSchedulerCount() != 0 {
		t.Fatalf("first project summary = %#v", first)
	}

	tail, err := projectClient.ListProjects(ctx, connect.NewRequest(&agentcomposev2.ListProjectsRequest{Offset: 200, Limit: 10}))
	if err != nil {
		t.Fatalf("ListProjects tail page: %v", err)
	}
	if tail.Msg.GetTotal() != 204 || len(tail.Msg.GetProjects()) != 4 {
		t.Fatalf("tail project page = total %d projects %d", tail.Msg.GetTotal(), len(tail.Msg.GetProjects()))
	}

	matched, err := projectClient.ListProjects(ctx, connect.NewRequest(&agentcomposev2.ListProjectsRequest{Query: "needle", Limit: 10}))
	if err != nil {
		t.Fatalf("ListProjects query: %v", err)
	}
	if matched.Msg.GetTotal() != 1 || len(matched.Msg.GetProjects()) != 1 || matched.Msg.GetProjects()[0].GetProjectId() != "project-042" {
		t.Fatalf("queried project page = %#v", matched.Msg)
	}

	includingRemoved, err := projectClient.ListProjects(ctx, connect.NewRequest(&agentcomposev2.ListProjectsRequest{IncludeRemoved: true, Limit: 500}))
	if err != nil {
		t.Fatalf("ListProjects including removed: %v", err)
	}
	if includingRemoved.Msg.GetTotal() != 205 || len(includingRemoved.Msg.GetProjects()) != 205 {
		t.Fatalf("project page including removed = total %d projects %d", includingRemoved.Msg.GetTotal(), len(includingRemoved.Msg.GetProjects()))
	}

	command := exec.CommandContext(ctx, binary, "--host", baseURL, "--json", "project", "ls", "--limit", "500")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("project ls against real daemon: %v\n%s", err, output)
	}
	var cliPage struct {
		Projects []struct {
			ID             string `json:"id"`
			AgentCount     uint32 `json:"agent_count"`
			SchedulerCount uint32 `json:"scheduler_count"`
		} `json:"projects"`
		Total uint32 `json:"total"`
	}
	if err := json.Unmarshal(output, &cliPage); err != nil {
		t.Fatalf("decode project ls output: %v\n%s", err, output)
	}
	if cliPage.Total != 204 || len(cliPage.Projects) != 204 || cliPage.Projects[0].ID != "project-000" || cliPage.Projects[0].AgentCount != 1 {
		t.Fatalf("project ls output = total %d projects %d first %#v", cliPage.Total, len(cliPage.Projects), cliPage.Projects[0])
	}
}

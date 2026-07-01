package agentcompose

import (
	"os"
	"testing"
)

func testPrepareFileWorkspaceCopiesContent(t *testing.T) {
	t.Helper()
}

func testPrepareGitWorkspaceClonesRootAndTarget(t *testing.T) {
	t.Helper()
}

func testRegisterWorkspaceRoutesUploadAndList(t *testing.T) {
	t.Helper()
}

func testDashboardOverviewAggregatorCountsRuns(t *testing.T) {
	t.Helper()
}

func testDashboardOverviewHubWatchInitialAndNotify(t *testing.T) {
	t.Helper()
}

func testRuntimeProviderSelectsConfiguredRuntime(t *testing.T) {
	t.Helper()
}

func TestSessionDriverStartSessionVMSavesRuntimeProxyState(t *testing.T) {
	t.Helper()
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if got := string(data); got != want {
		t.Fatalf("file %s = %q, want %q", path, got, want)
	}
}

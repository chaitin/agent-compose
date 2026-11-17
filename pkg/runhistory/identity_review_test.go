package runhistory

import (
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/identity"
)

func TestRunHistoryRejectsNoncanonicalIdentity(t *testing.T) {
	projectID := identity.NewID(identity.ResourceProject, "project")
	runID := identity.NewID(identity.ResourceRun, "run")
	schedulerID := identity.NewID(identity.ResourceScheduler, "scheduler")
	for _, transform := range []func(string) string{
		func(s string) string { return " " + s + " " },
		func(s string) string { return "sha256:" + s },
		strings.ToUpper,
	} {
		for _, field := range []string{"project", "run", "scheduler"} {
			id := Identity{ProjectID: projectID, Kind: SchedulerRun, RunID: runID, SchedulerID: schedulerID}
			switch field {
			case "project":
				id.ProjectID = transform(id.ProjectID)
			case "run":
				id.RunID = transform(id.RunID)
			case "scheduler":
				id.SchedulerID = transform(id.SchedulerID)
			}
			if err := id.Validate(); err == nil {
				t.Fatalf("accepted noncanonical %s identity: %+v", field, id)
			}
		}
	}
}

func TestRunHistoryAcceptsOnlyCanonicalInvocationUUID(t *testing.T) {
	projectID := identity.NewID(identity.ResourceProject, "project")
	schedulerID := identity.NewID(identity.ResourceScheduler, "scheduler")
	const invocation = "3f2b8c1e-6a4d-4e8b-9c2a-1d5e7f9a0b3c"
	if err := (Identity{ProjectID: projectID, Kind: SchedulerRun, RunID: invocation, SchedulerID: schedulerID}).Validate(); err != nil {
		t.Fatalf("rejected invocation execution id: %v", err)
	}
	if err := (Identity{ProjectID: projectID, Kind: Run, RunID: invocation}).Validate(); err == nil {
		t.Fatal("a standalone run identity must stay a resource id")
	}
	for _, runID := range []string{
		strings.ToUpper(invocation),
		"{" + invocation + "}",
		"urn:uuid:" + invocation,
		strings.ReplaceAll(invocation, "-", ""),
		" " + invocation,
		invocation[:8],
	} {
		if err := (Identity{ProjectID: projectID, Kind: SchedulerRun, RunID: runID, SchedulerID: schedulerID}).Validate(); err == nil {
			t.Fatalf("accepted noncanonical invocation id %q", runID)
		}
	}
}

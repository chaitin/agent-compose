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

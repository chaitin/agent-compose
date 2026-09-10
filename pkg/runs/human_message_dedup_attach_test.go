package runs

import (
	"slices"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// The start frame's client frame ID has to survive the trip from the attach
// stream to the run's creation, or the opening message is recorded under the
// run-scoped identity and a resend of it goes unrecognised. The provider is one
// prompt attach refuses, so the run is created and its opening message recorded
// without a runtime ever being opened.
func TestPromptAttachNamesTheOpeningMessageByItsStartFrame(t *testing.T) {
	controller, configDB, _ := newTestRunAttachController(t, nil)
	configDB.revision.SpecJSON = `{"agents":[{"name":"worker","provider":"gemini"}]}`
	requests := []*agentcomposev2.AttachAgentRunRequest{{
		ClientFrameId: "frame-open",
		Frame: &agentcomposev2.AttachAgentRunRequest_Start{Start: &agentcomposev2.AttachAgentRunStart{
			Request: &agentcomposev2.RunAgentRequest{ProjectId: "project-1", AgentName: "worker", Prompt: "hello"},
			Mode:    agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT,
		}},
	}}
	var runID string
	err := controller.RunProjectCommandAttach(t.Context(), recvAttachAgentRunRequests(requests), func(output RunAttachOutput) error {
		if output.Kind == RunAttachOutputResult {
			runID = output.Run.RunID
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunProjectCommandAttach: %v", err)
	}
	if runID == "" {
		t.Fatal("the run never reported its result")
	}
	named := attachedHumanEventID(runID, "frame-open", 0, "")
	if !slices.ContainsFunc(configDB.events, func(event domain.ProjectRunEventRecord) bool {
		return event.ID == named && event.Text == "hello"
	}) {
		t.Fatalf("opening message events = %#v, want it named by the start frame", configDB.events)
	}
}

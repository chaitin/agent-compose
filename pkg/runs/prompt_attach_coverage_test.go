package runs

import "testing"

func TestIntegrationPromptAttachWorkflow(t *testing.T) {
	runPromptAttachWorkflow(t)
}

func TestE2EPromptAttachWorkflow(t *testing.T) {
	runPromptAttachWorkflow(t)
}

// runPromptAttachWorkflow exercises the prompt-attach input and projection
// boundary as one workflow: published-skill start frames, queued and cancelled
// turns, transcript projection, and provider model selection.
func runPromptAttachWorkflow(t *testing.T) {
	t.Helper()
	suite := []struct {
		name string
		fn   func(*testing.T)
	}{
		{"start-frame", TestPromptAttachStartFrameNamesPublishedSkills},
		{"input-close-error", TestAttachInputPumpsCloseRuntimeInputOnReceiveError},
		{"input-waits-turn", TestPromptAttachInputWaitsForCompletedTurnBeforeForwardingQueuedMessages},
		{"input-cancel", TestPromptAttachInputCancellationUnblocksTurnWait},
		{"projector-result-final", TestPromptAttachProjectorLogsResultFinalTextWithoutAgentEventText},
		{"projector-human-final", TestPromptAttachProjectorLogsHumanMessagesAndTurnFinalText},
		{"projector-turn-final", TestPromptAttachProjectorLogsTurnFinalTextWithoutAgentEventText},
		{"projector-unterminated", TestPromptAttachProjectorSeparatesHumanMessageAfterUnterminatedAgentText},
		{"projector-queued", TestPromptAttachProjectorDoesNotDuplicateSeparatorsBetweenQueuedHumanMessages},
		{"projector-stderr", TestPromptAttachProjectorSeparatesHumanMessageFromStderrTail},
		{"projector-idempotent", TestPromptAttachProjectorPersistsEachFrameIdempotently},
		{"projector-terminal", TestPromptAttachProjectorProjectsTerminalAgentEventAfterOnlyHumanMessage},
		{"projector-tools", TestPromptAttachProjectorKeepsToolActivityInTheTranscript},
		{"projector-kinds", TestPromptAttachProjectorNamesFramesByEventKind},
		{"projector-tail", TestPromptAttachProjectorLogsFinalTextTailAfterToolActivity},
		{"projector-tail-later", TestPromptAttachProjectorLogsFinalTextTailOnLaterTurn},
		{"model-facade", TestPromptAttachProvidersAllHaveFacadeCases},
		{"model-resolved", TestPromptAttachRuntimeModelUsesFacadeResolvedModel},
		{"model-configured", TestPromptAttachRuntimeModelKeepsConfiguredModelWithoutFacadeModel},
		{"model-ignores-env", TestPromptAttachRuntimeModelIgnoresProviderSpecificModelEnv},
	}
	for _, tc := range suite {
		t.Run(tc.name, tc.fn)
	}
}

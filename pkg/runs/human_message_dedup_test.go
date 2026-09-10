package runs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// A client resends a message when it lost the stream before the turn's outcome
// reached it, and it resends under the frame ID the message already had.
// Recording it again would put the message in the history twice.
func TestProjectorRecordsAResentMessageOnce(t *testing.T) {
	store := &projectorEventStore{keys: map[string]struct{}{}}
	logsPath := filepath.Join(t.TempDir(), "transcript.txt")
	projector := newPersistentPromptAttachProjector(t.Context(), persistentPromptAttachProjectorDeps{Run: domain.ProjectRunRecord{RunID: "run-resend", AgentName: "worker"}, Sandbox: &domain.Sandbox{}, LogsPath: logsPath, EventStore: store})

	if fresh, err := projector.AppendHumanMessageFrame("question", "frame-1"); err != nil || !fresh {
		t.Fatalf("first delivery = (%v, %v), want a new message", fresh, err)
	}
	if fresh, err := projector.AppendHumanMessageFrame("question", "frame-1"); err != nil || fresh {
		t.Fatalf("resend = (%v, %v), want it recognised as already delivered", fresh, err)
	}
	if len(store.events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(store.events))
	}
	// The transcript is the other place a message lands, and the resend must
	// not appear there either.
	transcript, err := os.ReadFile(logsPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if got := strings.Count(string(transcript), "question"); got != 1 {
		t.Fatalf("transcript shows the message %d times, want once: %q", got, transcript)
	}
}

// A frame ID is the client's promise that two messages are one. Different text
// under an ID already recorded breaks that promise, and treating it as a resend
// would silently drop what the person actually said.
func TestProjectorRefusesAFrameIDReusedForADifferentMessage(t *testing.T) {
	store := &projectorEventStore{keys: map[string]struct{}{}}
	logsPath := filepath.Join(t.TempDir(), "transcript.txt")
	projector := newPersistentPromptAttachProjector(t.Context(), persistentPromptAttachProjectorDeps{Run: domain.ProjectRunRecord{RunID: "run-reused", AgentName: "worker"}, Sandbox: &domain.Sandbox{}, LogsPath: logsPath, EventStore: store})

	if _, err := projector.AppendHumanMessageFrame("question", "frame-1"); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	fresh, err := projector.AppendHumanMessageFrame("something else", "frame-1")
	if !errors.Is(err, errClientFrameReused) || fresh {
		t.Fatalf("reused frame ID = (%v, %v), want errClientFrameReused", fresh, err)
	}
	if len(store.events) != 1 || store.events[0].Text != "question" {
		t.Fatalf("recorded events = %#v, want only the original message", store.events)
	}
	transcript, err := os.ReadFile(logsPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if strings.Contains(string(transcript), "something else") {
		t.Fatalf("a refused message reached the transcript: %q", transcript)
	}
}

// Without a frame ID a message is identified by its position, which never
// repeats. That is every client that predates frame IDs, the CLI among them,
// and a person saying the same thing twice has to be heard twice.
func TestProjectorTreatsMessagesWithoutAFrameIDAsAlwaysNew(t *testing.T) {
	store := &projectorEventStore{keys: map[string]struct{}{}}
	projector := newPersistentPromptAttachProjector(t.Context(), persistentPromptAttachProjectorDeps{Run: domain.ProjectRunRecord{RunID: "run-unnamed", AgentName: "worker"}, Sandbox: &domain.Sandbox{}, LogsPath: filepath.Join(t.TempDir(), "transcript.txt"), EventStore: store})

	for range 2 {
		if fresh, err := projector.AppendHumanMessageFrame("again", ""); err != nil || !fresh {
			t.Fatalf("unnamed message = (%v, %v), want a new message every time", fresh, err)
		}
	}
	if len(store.events) != 2 {
		t.Fatalf("recorded %d events, want 2", len(store.events))
	}
}

// A message already delivered is not handed to the agent again, and the turn
// gate it took is handed straight back: no turn runs for it, so nothing else
// would ever release that gate.
func TestPumpDoesNotRunAMessageAlreadyDelivered(t *testing.T) {
	interaction, answers, done := startPromptPump(t,
		func(_, clientFrameID string) (bool, error) { return clientFrameID != "frame-delivered", nil },
		RunAttachInput{Kind: RunAttachInputHumanMessage, Text: "again", ClientFrameID: "frame-delivered"},
		RunAttachInput{Kind: RunAttachInputHumanMessage, Text: "next", ClientFrameID: "frame-new"},
	)

	// The next message reaches the agent without anyone releasing the gate a
	// second time. Had the declined message kept the turn it took, this would
	// wait for a turn that is never coming.
	assertPromptRuntimeFrame(t, receiveRuntimeInputFrame(t, interaction.sent), "human_message", "next")
	assertPromptRuntimeFrame(t, receiveRuntimeInputFrame(t, interaction.sent), "eof", "")
	waitForPump(t, done)
	// A client waiting on the resend would otherwise wait forever: it has to
	// be told, by name, that this message was not run.
	assertDeclined(t, answers, attachErrorDuplicateMessage, "frame-delivered")
}

// A frame ID reused for different text is answered and not run, and — unlike
// a failure to record — does not end the session: it is the client's mistake,
// not the daemon's.
func TestPumpAnswersAReusedFrameIDWithoutRunningIt(t *testing.T) {
	interaction, answers, done := startPromptPump(t,
		func(_, clientFrameID string) (bool, error) {
			if clientFrameID == "frame-reused" {
				return false, errClientFrameReused
			}
			return true, nil
		},
		RunAttachInput{Kind: RunAttachInputHumanMessage, Text: "different", ClientFrameID: "frame-reused"},
		RunAttachInput{Kind: RunAttachInputHumanMessage, Text: "next", ClientFrameID: "frame-new"},
	)

	assertPromptRuntimeFrame(t, receiveRuntimeInputFrame(t, interaction.sent), "human_message", "next")
	assertPromptRuntimeFrame(t, receiveRuntimeInputFrame(t, interaction.sent), "eof", "")
	waitForPump(t, done)
	assertDeclined(t, answers, attachErrorClientFrameReused, "frame-reused")
}

// A message that could not be recorded still ends the input, as it did before
// duplicates were told apart. Declining is only for messages known to be
// delivered.
func TestPumpStillStopsWhenAMessageCannotBeRecorded(t *testing.T) {
	interaction, answers, done := startPromptPump(t,
		func(string, string) (bool, error) { return false, errors.New("disk full") },
		RunAttachInput{Kind: RunAttachInputHumanMessage, Text: "question", ClientFrameID: "frame-1"},
	)

	waitForPump(t, done)
	assertNoRuntimeInputFrame(t, interaction.sent)
	select {
	case answer := <-answers:
		t.Fatalf("a recording failure was answered as a declined message: %#v", answer)
	default:
	}
}

// The opening message travels in the start frame and is recorded when the run
// is created, well away from the attached-message path that sees a resend. A
// resend is recognised only because both take their identity from the frame
// ID the client gave the start frame.
func TestAResentOpeningMessageIsRecognisedByTheFrameThatCarriedIt(t *testing.T) {
	store := newOpeningPromptRunStore()
	coordinator := NewCoordinator(store, func(_, _, _, key string) (string, error) { return "run-" + key, nil })
	run, err := coordinator.BeginRun(t.Context(), StartRequest{ProjectID: "project-1", AgentName: "worker", Source: domain.ProjectRunSourceAPI, ClientRequestID: "open", Prompt: "hello", PromptFrameID: "frame-open"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if len(store.events) != 1 {
		t.Fatalf("recorded %d opening events, want 1", len(store.events))
	}
	opening := store.events[0]

	events := &projectorEventStore{keys: map[string]struct{}{opening.ID: {}}, events: []domain.ProjectRunEventRecord{opening}}
	projector := newPersistentPromptAttachProjector(t.Context(), persistentPromptAttachProjectorDeps{Run: run, Sandbox: &domain.Sandbox{}, LogsPath: filepath.Join(t.TempDir(), "transcript.txt"), EventStore: events})
	if fresh, err := projector.AppendHumanMessageFrame("hello", "frame-open"); err != nil || fresh {
		t.Fatalf("resent opening message = (%v, %v), want it recognised as already delivered", fresh, err)
	}
}

// Runs started without a frame ID — the CLI, the scheduler, every client that
// predates this — keep the identity their opening message always had, so no
// persisted event changes its name.
func TestAnOpeningMessageWithoutAFrameIDKeepsItsIdentity(t *testing.T) {
	store := newOpeningPromptRunStore()
	coordinator := NewCoordinator(store, func(_, _, _, key string) (string, error) { return "run-" + key, nil })
	run, err := coordinator.BeginRun(t.Context(), StartRequest{ProjectID: "project-1", AgentName: "worker", Source: domain.ProjectRunSourceAPI, ClientRequestID: "plain", Prompt: "hello"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if len(store.events) != 1 || store.events[0].ID != initialPromptEventID(run.RunID) {
		t.Fatalf("opening events = %#v, want the run-scoped identity", store.events)
	}
}

// What makes the two paths meet: a named opening message is exactly the
// attached-message identity for that frame, whatever its position or text.
func TestOpeningPromptIdentityIsTheAttachedMessageIdentity(t *testing.T) {
	const runID = "run-identity"
	named := openingPromptEventID(runID, "frame-open")
	if named != attachedHumanEventID(runID, "frame-open", 7, "anything") {
		t.Fatal("a named opening message does not share the attached-message identity for its frame")
	}
	if named == initialPromptEventID(runID) {
		t.Fatal("a named opening message kept the run-scoped identity")
	}
	if openingPromptEventID(runID, "  ") != initialPromptEventID(runID) {
		t.Fatal("a blank frame ID changed the opening message's identity")
	}
}

// Several goroutines write to an attached run's sender: the receive loop, the
// input pump declining a message, and the result frame sent after the
// interaction returns while the pump may still be alive. The sender itself has
// to order them — the stream takes one send at a time and its detached flag is
// plain state — so no caller can end up holding an unserialized one. Under
// -race this fails if it does not.
func TestInteractiveRunOutputSenderIsSafeForConcurrentWriters(t *testing.T) {
	session := NewInteractiveSession("run-concurrent")

	var streamed int
	attached := newInteractiveRunOutputSender(session, AttachDisconnectCancel, func(RunAttachOutput) error {
		streamed++ // deliberately unsynchronized: the sender must serialize it
		return nil
	})
	writeConcurrently(attached)
	if streamed != 800 {
		t.Fatalf("streamed %d frames, want 800", streamed)
	}

	// A stream that fails flips the sender to detached: the first failure
	// writes that flag and every writer reads it.
	var failed int
	detaching := newInteractiveRunOutputSender(session, AttachDisconnectDetach, func(RunAttachOutput) error {
		failed++
		return errors.New("stream closed")
	})
	writeConcurrently(detaching)
	if failed != 1 {
		t.Fatalf("wrote to a closed stream %d times, want once before detaching", failed)
	}
}

// A pump whose session has just ended can find its turn gate and its context
// ready together, and select picks either. It must not go on to run or answer
// the message: its interaction is gone, and the result frame may already be on
// its way to the client.
func TestPumpDoesNotAnswerAfterItsSessionEnded(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	interaction := newObservedRuntimeInteraction()
	turnReady := make(chan struct{}, 1)
	turnReady <- struct{}{}
	queue := make(chan RunAttachInput, 1)
	queue <- RunAttachInput{Kind: RunAttachInputHumanMessage, Text: "again", ClientFrameID: "frame-delivered"}
	close(queue)

	var answered int
	pumpRunPromptAttachInput(ctx, func() (RunAttachInput, error) {
		request, ok := <-queue
		if !ok {
			return RunAttachInput{}, io.EOF
		}
		return request, nil
	}, promptInputPump{
		Input:          &promptWrapperInput{interaction: interaction},
		TurnReady:      turnReady,
		OnHumanMessage: func(string, string) (bool, error) { return false, nil },
		Send: func(RunAttachOutput) error {
			answered++
			return nil
		},
	})

	if answered != 0 {
		t.Fatalf("a pump whose session ended answered %d times", answered)
	}
	assertNoRuntimeInputFrame(t, interaction.sent)
}

// writeConcurrently sends through one sender from several goroutines at once.
func writeConcurrently(send RunAttachSender) {
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				_ = send(RunAttachOutput{Kind: RunAttachOutputData})
			}
		})
	}
	wg.Wait()
}

// startPromptPump feeds requests to a prompt input pump until they run out,
// with the gate open as it is once the previous turn has completed, and
// returns what reached the agent, what the client was answered, and when the
// pump finished.
func startPromptPump(t *testing.T, record func(text, clientFrameID string) (bool, error), requests ...RunAttachInput) (*observedRuntimeInteraction, <-chan RunAttachOutput, <-chan struct{}) {
	t.Helper()
	interaction := newObservedRuntimeInteraction()
	turnReady := make(chan struct{}, 1)
	turnReady <- struct{}{}
	answers := make(chan RunAttachOutput, len(requests))
	queue := make(chan RunAttachInput, len(requests))
	for _, request := range requests {
		queue <- request
	}
	close(queue)
	done := make(chan struct{})
	go func() {
		defer close(done)
		pumpRunPromptAttachInput(t.Context(), func() (RunAttachInput, error) {
			request, ok := <-queue
			if !ok {
				return RunAttachInput{}, io.EOF
			}
			return request, nil
		}, promptInputPump{
			Input:          &promptWrapperInput{interaction: interaction},
			TurnReady:      turnReady,
			OnHumanMessage: record,
			Send: func(output RunAttachOutput) error {
				answers <- output
				return nil
			},
		})
	}()
	return interaction, answers, done
}

func waitForPump(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("prompt input pump did not finish")
	}
}

// assertDeclined checks that the client was told, once and without ending the
// session, that the message it sent as clientFrameID was not run.
func assertDeclined(t *testing.T, answers <-chan RunAttachOutput, code, clientFrameID string) {
	t.Helper()
	select {
	case answer := <-answers:
		if answer.Kind != RunAttachOutputError || answer.Terminal || answer.Code != code || answer.Details["client_frame_id"] != clientFrameID {
			t.Fatalf("answer = %#v, want a non-terminal %s naming %s", answer, code, clientFrameID)
		}
	default:
		t.Fatalf("the client was never told its %s message was not run", clientFrameID)
	}
	select {
	case extra := <-answers:
		t.Fatalf("unexpected further answer %#v", extra)
	default:
	}
}

// newOpeningPromptRunStore is a run store holding one enabled agent, enough for
// BeginRun to create a run with an opening message.
func newOpeningPromptRunStore() *fakeRunStore {
	return &fakeRunStore{
		project: domain.ProjectRecord{ID: "project-1", Name: "Project", CurrentRevision: 3},
		revision: domain.ProjectRevisionRecord{ProjectID: "project-1", Revision: 3,
			SpecJSON: `{"name":"project","agents":[{"name":"worker","provider":"codex","image":"guest:latest","driver":{"name":"docker"},"enabled":true}]}`},
		projectAgent: domain.ProjectAgentRecord{ProjectID: "project-1", AgentName: "worker", ID: "agent-1", Driver: "docker", Image: "guest:latest"},
		agent:        domain.AgentDefinition{ID: "agent-1", Enabled: true, Driver: "docker", GuestImage: "guest:latest", ProjectID: "project-1", AgentName: "worker"},
		runs:         map[string]domain.ProjectRunRecord{},
	}
}

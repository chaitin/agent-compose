package runs

import (
	"context"
	"strings"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

type journalHarness struct {
	t       *testing.T
	run     domain.ProjectRunRecord
	store   *projectorEventStore
	p       *agentRunProjector
	journal testJournal
	offset  uint64
}

func newJournalHarness(t *testing.T) *journalHarness {
	t.Helper()
	run := projectionTestRun(t)
	h := &journalHarness{t: t, run: run, store: &projectorEventStore{keys: map[string]struct{}{}}}
	h.p = newAgentRunProjector(context.Background(), run, nil, run.LogsPath, nil)
	h.p.events = h.store
	return h
}

func (h *journalHarness) push(text string) {
	h.t.Helper()
	h.offset += uint64(len(text))
	chunk := h.journal.record(domain.ExecChunk{Text: text, Journal: &domain.JournalPosition{End: h.offset}})
	if _, err := h.p.ProjectRuntimeOutput(chunk); err != nil {
		h.t.Fatal(err)
	}
}

// restart stops the daemon without closing the log and rebuilds the
// projector from it, then replays the journal from where it resumes.
func (h *journalHarness) restart() {
	h.t.Helper()
	abandonRunLog(h.t, h.run.LogsPath)
	h.p = newAgentRunProjector(context.Background(), h.run, nil, h.run.LogsPath, nil)
	h.p.events = h.store
	resume, err := h.p.RestoreJournal()
	if err != nil || resume == nil {
		h.t.Fatalf("resume=%+v err=%v", resume, err)
	}
	for _, chunk := range h.journal.after(resume.Offset) {
		if _, err := h.p.ProjectRuntimeOutput(chunk); err != nil {
			h.t.Fatal(err)
		}
	}
}

func committedEntries(t *testing.T, run domain.ProjectRunRecord) []runlog.Entry {
	t.Helper()
	entries, err := runlog.ReadEntries(run.LogsPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestHumanInputIsCommittedOnceTheOutputReachesALineBoundary(t *testing.T) {
	h := newJournalHarness(t)
	h.push("{\"v\":1,\"seq\":1,\"type\":\"agent_turn_completed\",\"finalText\":\"first\",\"finalTextSource\":\"provider_message\"}\n")
	h.push(`{"v":1,"seq":2,"type":"agent_event","event":{"kind":"text_delta","text":"half`)
	done := make(chan error, 1)
	go func() { done <- h.p.AppendHumanMessageFrame("next question", "") }()
	select {
	case err := <-done:
		t.Fatalf("human input went ahead inside a split frame: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	h.push("\"}}\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// The input is durable before it is sent: a restart now keeps it.
	var users int
	for _, e := range committedEntries(t, h.run) {
		if e.Type == runlog.EntryUser && e.Text == "next question" {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("human input not committed: %+v", committedEntries(t, h.run))
	}
	h.restart()
	if h.p.humanIndex != 1 || h.p.turnEnded {
		t.Fatalf("rebuilt humanIndex=%d turnEnded=%v", h.p.humanIndex, h.p.turnEnded)
	}
}

func TestHumanInputIsRefusedWhenTheOutputNeverReachesALineBoundary(t *testing.T) {
	previous := humanInputQuiescenceTimeout
	humanInputQuiescenceTimeout = 50 * time.Millisecond
	t.Cleanup(func() { humanInputQuiescenceTimeout = previous })
	h := newJournalHarness(t)
	h.push(`{"v":1,"seq":1,"type":"agent_event","event":{"kind":"text_delta","text":"half`)
	if err := h.p.AppendHumanMessageFrame("lost?", ""); err == nil || !strings.Contains(err.Error(), "line boundary") {
		t.Fatalf("err=%v", err)
	}
	for _, e := range committedEntries(t, h.run) {
		if e.Type == runlog.EntryUser && !e.Initial {
			t.Fatalf("refused input reached the log: %+v", e)
		}
	}
}

func TestRestartRebuildsTurnsAndPendingMessagesFromTheLog(t *testing.T) {
	h := newJournalHarness(t)
	h.push("{\"v\":1,\"seq\":1,\"type\":\"agent_event\",\"event\":{\"kind\":\"text_delta\",\"text\":\"one\"}}\n")
	h.push("{\"v\":1,\"seq\":2,\"type\":\"agent_turn_completed\",\"finalText\":\"one\",\"finalTextSource\":\"provider_message\"}\n")
	if err := h.p.AppendHumanMessageFrame("again", ""); err != nil {
		t.Fatal(err)
	}
	h.push("{\"v\":1,\"seq\":3,\"type\":\"agent_event\",\"event\":{\"kind\":\"text_delta\",\"text\":\"two\"}}\n")
	h.restart()
	p := h.p
	// The second turn's prose was held for coalescing: replay rebuilt it.
	if p.humanIndex != 1 || p.turnEnded || len(p.pendingMessages) != 0 {
		t.Fatalf("humanIndex=%d turnEnded=%v pending=%+v", p.humanIndex, p.turnEnded, p.pendingMessages)
	}
	h.push("{\"v\":1,\"seq\":4,\"type\":\"result\",\"finalText\":\"two\",\"finalTextSource\":\"provider_message\"}\n")
	h.restart()
	p = h.p
	if !p.turnEnded || p.terminal == nil || len(p.pendingMessages) != 1 || p.pendingMessages[0].Text != "two" || p.pendingMessages[0].LogSeq == 0 {
		t.Fatalf("turnEnded=%v terminal=%v pending=%+v", p.turnEnded, p.terminal != nil, p.pendingMessages)
	}
	if !strings.Contains(p.loggedProse, "two") || strings.Contains(p.loggedProse, "one") {
		t.Fatalf("loggedProse=%q", p.loggedProse)
	}
	// The completed first turn's message went to the store with its turn; the
	// last one goes with the run.
	var answers []string
	for _, event := range h.store.events {
		if event.Kind == domain.ProjectRunEventKindAgentMessage {
			answers = append(answers, event.Text)
		}
	}
	if len(answers) != 1 || answers[0] != "one" {
		t.Fatalf("persisted=%+v", h.store.events)
	}
	text := runlog.RenderText(committedEntries(t, h.run))
	if strings.Count(text, "two") != 1 || strings.Count(text, "one") != 1 {
		t.Fatalf("text=%q", text)
	}
}

func TestExitCommitsTextTheMergerStillHeld(t *testing.T) {
	h := newJournalHarness(t)
	h.push("{\"v\":1,\"seq\":1,\"type\":\"agent_event\",\"event\":{\"kind\":\"text_delta\",\"text\":\"no newline\"}}\n")
	exit := h.journal.record(domain.ExecChunk{Journal: &domain.JournalPosition{End: h.offset, Exit: &domain.JournalExit{Result: domain.ExecResult{Success: true}}}})
	if _, err := h.p.ProjectRuntimeOutput(exit); err != nil {
		t.Fatal(err)
	}
	abandonRunLog(t, h.run.LogsPath)
	p := newAgentRunProjector(context.Background(), h.run, nil, h.run.LogsPath, nil)
	resume, err := p.RestoreJournal()
	if err != nil || resume == nil || resume.Exit == nil || resume.Offset != h.offset {
		t.Fatalf("resume=%+v err=%v", resume, err)
	}
	if text := runlog.RenderText(committedEntries(t, h.run)); text != "no newline" {
		t.Fatalf("text=%q", text)
	}
}

func TestHeldProseIsCommittedOnceTheAgentPauses(t *testing.T) {
	previous := heldTextFlushDelay
	heldTextFlushDelay = 20 * time.Millisecond
	t.Cleanup(func() { heldTextFlushDelay = previous })
	h := newJournalHarness(t)
	h.push("{\"v\":1,\"seq\":1,\"type\":\"agent_event\",\"event\":{\"kind\":\"tool_call\",\"id\":\"tool-1\",\"name\":\"shell\",\"command\":\"pwd\"}}\n")
	// The agent starts a paragraph and pauses: nothing covers the tool call
	// yet, since the merger holds the prose for coalescing.
	h.push("{\"v\":1,\"seq\":2,\"type\":\"agent_event\",\"event\":{\"kind\":\"text_delta\",\"text\":\"thinking \"}}\n")
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries := committedEntries(t, h.run)
		if text := runlog.RenderText(entries); runlog.Summarize(entries).ToolCalls == 1 && strings.HasSuffix(text, "thinking ") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("held prose never committed: %+v", entries)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The paragraph continues in a new entry, and a restart replays nothing
	// twice.
	h.push("{\"v\":1,\"seq\":3,\"type\":\"agent_event\",\"event\":{\"kind\":\"text_delta\",\"text\":\"done\"}}\n")
	h.restart()
	h.push("{\"v\":1,\"seq\":4,\"type\":\"result\",\"finalText\":\"thinking done\",\"finalTextSource\":\"provider_message\"}\n")
	if _, err := h.p.CompleteExecution(domain.ExecResult{Success: true}, nil); err != nil {
		t.Fatal(err)
	}
	if text := runlog.RenderText(committedEntries(t, h.run)); strings.Count(text, "thinking ") != 1 || strings.Count(text, "done") != 1 {
		t.Fatalf("text=%q", text)
	}
}

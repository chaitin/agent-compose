package e2e

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func (f *runLogE2E) assertMessages(t *testing.T, runID string, answers []string, incomplete bool) {
	t.Helper()
	response, err := f.runs.ListRunMessages(f.ctx, connect.NewRequest(&agentcomposev2.ListRunMessagesRequest{RunId: runID, Limit: 100}))
	if err != nil {
		t.Fatal(err)
	}
	if response.Msg.GetTotal() != uint32(2*len(answers)) {
		t.Fatalf("conversation has missing or duplicate messages: %s", response.Msg)
	}
	seen, assistant := map[string]bool{}, map[string]bool{}
	for _, message := range response.Msg.GetMessages() {
		if message.GetId() == "" || seen[message.GetId()] || (!incomplete && message.GetLogSeq() == 0) {
			t.Fatalf("invalid or duplicate conversation identity: %s", message)
		}
		seen[message.GetId()] = true
		if message.GetRole() == agentcomposev2.RunMessageRole_RUN_MESSAGE_ROLE_AGENT {
			assistant[message.GetText()] = true
		}
	}
	for _, answer := range answers {
		if !assistant[answer] {
			t.Fatalf("conversation lost answer %q: %s", answer, response.Msg)
		}
	}
}

func (f *runLogE2E) logs(t *testing.T, request *agentcomposev2.FollowRunLogsRequest) ([]*agentcomposev2.RunLogEntry, string) {
	t.Helper()
	stream, err := f.runs.FollowRunLogs(f.ctx, connect.NewRequest(request))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stream.Close(); err != nil {
			t.Errorf("close log stream: %v", err)
		}
	}()
	var entries []*agentcomposev2.RunLogEntry
	var text strings.Builder
	for stream.Receive() {
		entries = append(entries, stream.Msg().GetEntries()...)
		text.WriteString(stream.Msg().GetData())
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	return entries, text.String()
}

func (f *runLogE2E) assertLog(t *testing.T, runID string, answers []string) {
	t.Helper()
	entries, _ := f.logs(t, &agentcomposev2.FollowRunLogsRequest{RunId: runID, Structured: true})
	users, turns, calls, results := 0, 0, 0, 0
	var seq uint64
	var text strings.Builder
	for _, entry := range entries {
		if entry.GetSeq() != seq+1 {
			t.Fatalf("non-contiguous log sequence: previous=%d entry=%s", seq, entry)
		}
		seq = entry.GetSeq()
		if entry.GetUserInput() != nil {
			users++
		}
		if entry.GetTurnEnd() != nil {
			turns++
		}
		if entry.GetToolCall() != nil {
			calls++
		}
		if entry.GetToolResult() != nil {
			results++
		}
		text.WriteString(entry.GetText().GetText())
	}
	if users != len(answers) || turns != len(answers) || calls != len(answers) || results != len(answers) {
		t.Fatalf("duplicate/missing projections: users=%d turns=%d calls=%d results=%d want=%d", users, turns, calls, results, len(answers))
	}
	_, rendered := f.logs(t, &agentcomposev2.FollowRunLogsRequest{RunId: runID})
	// Tool output may interrupt streamed prose in the combined text view.
	proseView := strings.ReplaceAll(rendered, "fixture-tool", "")
	for _, answer := range answers {
		if strings.Count(text.String(), answer) != 1 || strings.Count(proseView, answer) != 1 {
			t.Fatalf("lost/duplicate answer %q: prose=%q rendered=%q", answer, text.String(), rendered)
		}
	}
	if strings.Contains(rendered, "__AGENT_") || strings.Contains(rendered, `"type":"agent_event"`) {
		t.Fatalf("wire protocol leaked into text log: %q", rendered)
	}
	// Both public resume cursors must return precisely the remaining suffix.
	last, _ := f.logs(t, &agentcomposev2.FollowRunLogsRequest{RunId: runID, Structured: true, StartSeq: seq - 1})
	if len(last) != 1 || last[0].GetSeq() != seq {
		t.Fatalf("structured resume returned %v, want seq %d", last, seq)
	}
	if len(rendered) > 5 {
		_, suffix := f.logs(t, &agentcomposev2.FollowRunLogsRequest{RunId: runID, StartOffset: 5})
		if suffix != rendered[5:] {
			t.Fatalf("text byte cursor resumed with the wrong suffix: %q", suffix)
		}
	}
}

func (f *runLogE2E) assertNoAgentCells(t *testing.T) {
	t.Helper()
	response, err := f.sandboxes.GetSandbox(f.ctx, connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: f.sandboxID}))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(response.Msg.GetSandbox().GetWorkspacePath())
	for _, name := range []string{"state/cells", "cells.json", "agent-thread.json"} {
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("new agent run kept legacy storage %s: %v", name, err)
		}
	}
	// Sandbox creation initializes an empty notebook index for all run kinds.
	// No agent cell or output may be added to that compatibility index.
	data, err := os.ReadFile(filepath.Join(root, "state", "cells.json"))
	if err == nil {
		var cells []json.RawMessage
		if err := json.Unmarshal(data, &cells); err != nil || len(cells) != 0 {
			t.Fatalf("agent run duplicated its notebook history: %s (%v)", data, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func (f *runLogE2E) finishInteractive(t *testing.T, runID string, answers *[]string) {
	t.Helper()
	f.wait(t, "first interactive answer", func() bool {
		response, err := f.runs.ListRunMessages(f.ctx, connect.NewRequest(&agentcomposev2.ListRunMessagesRequest{RunId: runID, Limit: 100}))
		if err != nil {
			t.Fatal(err)
		}
		return response.Msg.GetTotal() == 2
	})
	// Connect bidi streams require HTTP/2; the daemon exposes cleartext HTTP/2.
	transport := &http2.Transport{AllowHTTP: true, DialTLSContext: func(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	defer transport.CloseIdleConnections()
	client := agentcomposev2connect.NewRunServiceClient(&http.Client{Transport: transport}, f.baseURL)
	ctx, cancel := context.WithTimeout(f.ctx, 2*time.Minute)
	defer cancel()
	stream := client.AttachAgentRun(ctx)
	defer func() {
		// The server may already have closed a completed session.
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
	}()
	if err := stream.Send(&agentcomposev2.AttachAgentRunRequest{Frame: &agentcomposev2.AttachAgentRunRequest_Start{Start: &agentcomposev2.AttachAgentRunStart{
		RunId: runID, Mode: agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT, AttachStdin: true,
		DisconnectPolicy: agentcomposev2.AttachDisconnectPolicy_ATTACH_DISCONNECT_POLICY_DETACH,
	}}}); err != nil {
		t.Fatal(err)
	}
	// Existing sessions publish subsequent frames; they do not replay started.
	if err := stream.Send(&agentcomposev2.AttachAgentRunRequest{Frame: &agentcomposev2.AttachAgentRunRequest_HumanMessage{HumanMessage: &agentcomposev2.AttachHumanMessage{Text: "runlog:second:immediate"}}}); err != nil {
		t.Fatal(err)
	}
	*answers = append(*answers, runLogE2EAnswer("second"))
	for {
		frame, err := stream.Receive()
		if err != nil {
			t.Fatal(err)
		}
		if frame.GetError() != nil {
			t.Fatalf("interactive error: %s", frame)
		}
		if strings.Contains(frame.GetAgentTurnCompleted().GetResultJson(), runLogE2EAnswer("second")) {
			break
		}
	}
	if err := stream.Send(&agentcomposev2.AttachAgentRunRequest{Frame: &agentcomposev2.AttachAgentRunRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}}}); err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := stream.Receive()
		if err != nil {
			t.Fatal(err)
		}
		if frame.GetError() != nil {
			t.Fatalf("interactive completion error: %s", frame)
		}
		if frame.GetResult() != nil {
			return
		}
	}
}

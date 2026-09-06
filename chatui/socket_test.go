package main

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chaitin/agent-compose/sdk/go/chat"
)

// fakeDaemon answers AttachAgentRun with a scripted turn.
//
// It serves h2c because a Connect bidirectional stream needs HTTP/2, and it
// writes no response header until it has read the client's opening frame,
// which is how the real daemon behaves.
func fakeDaemon(t *testing.T, script []wireEvent) *httptest.Server {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/AttachAgentRun") {
			http.Error(w, "unexpected procedure "+r.URL.Path, http.StatusNotFound)
			return
		}
		if _, err := readEnvelope(r.Body); err != nil {
			return
		}
		w.Header().Set("Content-Type", "application/connect+json")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		send := func(frame map[string]any) bool {
			payload, err := json.Marshal(frame)
			if err != nil {
				return false
			}
			var header [5]byte
			binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
			if _, err := w.Write(header[:]); err != nil {
				return false
			}
			if _, err := w.Write(payload); err != nil {
				return false
			}
			if flusher != nil {
				flusher.Flush()
			}
			return true
		}
		send(map[string]any{"started": map[string]any{"runId": "run_1", "sandboxId": "sb_1"}})
		for _, event := range script {
			payload, err := json.Marshal(event.payload)
			if err != nil {
				t.Errorf("encode %s: %v", event.name, err)
				return
			}
			send(map[string]any{"agentEvent": map[string]any{
				"name": event.name, "payloadJson": string(payload),
			}})
		}
		send(map[string]any{"agentTurnCompleted": map[string]any{"runId": "run_1"}})
	})
	server := httptest.NewUnstartedServer(handler)
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	server.Config.Protocols = protocols
	server.Start()
	t.Cleanup(server.Close)
	return server
}

type wireEvent struct {
	name    string
	payload map[string]any
}

// readEnvelope consumes one Connect stream frame.
func readEnvelope(body io.Reader) ([]byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(body, header); err != nil {
		return nil, err
	}
	payload := make([]byte, binary.BigEndian.Uint32(header[1:]))
	if _, err := io.ReadFull(body, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// A turn's events reach the browser with the step each one names, and a tool
// result stays joined to the call it answers. Both are what the UI needs to
// draw the work as steps rather than as a flat log.
func TestTheSocketRelaysATurnAsStructuredSteps(t *testing.T) {
	daemon := fakeDaemon(t, []wireEvent{
		{"step_start", map[string]any{"step": 0}},
		{"reasoning_delta", map[string]any{"step": 0, "text": "先看看目录"}},
		{"tool_call", map[string]any{
			"step": 0, "id": "call_1", "name": "bash",
			"toolKind": "execute", "status": "in_progress", "command": "ls /",
		}},
		{"tool_result", map[string]any{"step": 0, "id": "call_1", "ok": true, "output": "bin\netc\n"}},
		{"step_end", map[string]any{"step": 0, "stopReason": "tool_use"}},
		{"step_start", map[string]any{"step": 1}},
		{"text_delta", map[string]any{"step": 1, "text": "根目录里有 "}},
		{"text_delta", map[string]any{"step": 1, "text": "bin 和 etc。"}},
		{"usage", map[string]any{"step": 1, "scope": "turn", "inputTokens": 120, "outputTokens": 30}},
		{"step_end", map[string]any{"step": 1, "stopReason": "stop"}},
	})

	server := newTestServer(t)
	client, err := chat.New(chat.Config{BaseURL: daemon.URL})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	server.client, server.daemon = client, daemon.URL

	front := httptest.NewServer(server.routes())
	t.Cleanup(front.Close)

	browser := newBrowser(t, front.URL)
	id := browserCreate(t, browser, front.URL)

	socketURL := strings.Replace(front.URL, "http://", "ws://", 1) + "/api/conversations/" + id + "/socket"
	dialer := &websocket.Dialer{Jar: browser.Jar}
	conn, handshake, err := dialer.Dial(socketURL, nil)
	if err != nil {
		t.Fatalf("dial socket: %v", err)
	}
	defer func() { _ = handshake.Body.Close() }()
	defer func() { _ = conn.Close() }()

	if err := conn.WriteJSON(map[string]string{"type": "message", "text": "根目录有什么"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	var kinds []string
	var steps []any
	tools := map[string]map[string]any{}
	answer := ""
	var done map[string]any
	for done == nil {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		var frame map[string]any
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatalf("read: %v (collected %v)", err, kinds)
		}
		switch frame["type"] {
		case "turn_started":
			if frame["prompt"] != "根目录有什么" {
				t.Errorf("turn_started did not carry the prompt: %v", frame)
			}
		case "event":
			event, _ := frame["event"].(map[string]any)
			kind, _ := event["kind"].(string)
			kinds = append(kinds, kind)
			if kind == "text_delta" {
				answer += event["text"].(string)
			}
			if kind == "tool_call" || kind == "tool_result" {
				tools[kind] = event
			}
			if step, present := event["step"]; present {
				steps = append(steps, step)
			}
		case "error":
			t.Fatalf("the turn failed: %v", frame)
		case "turn_done":
			done = frame
		}
	}

	want := []string{
		"step_start", "reasoning_delta", "tool_call", "tool_result", "step_end",
		"step_start", "text_delta", "text_delta", "usage", "step_end",
	}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Errorf("relayed %v, want %v", kinds, want)
	}
	if len(steps) != len(want) {
		t.Errorf("%d of %d events arrived without a step number", len(want)-len(steps), len(want))
	}
	if tools["tool_call"]["id"] != tools["tool_result"]["id"] {
		t.Errorf("a tool result lost the call it answers: %v / %v", tools["tool_call"], tools["tool_result"])
	}
	if tools["tool_call"]["command"] != "ls /" {
		t.Errorf("the tool call lost its command: %v", tools["tool_call"])
	}
	if answer != "根目录里有 bin 和 etc。" {
		t.Errorf("the answer did not accumulate: %q", answer)
	}
	if done["continuity"] != string(chat.Continuous) {
		t.Errorf("turn_done reported continuity %v", done["continuity"])
	}
}

// A turn also names its conversation, so the sidebar row appears without the
// page having to guess a title or poll for one.
func TestTheFirstMessageNamesTheConversationOverTheSocket(t *testing.T) {
	daemon := fakeDaemon(t, []wireEvent{{"text_delta", map[string]any{"text": "好的"}}})

	server := newTestServer(t)
	client, err := chat.New(chat.Config{BaseURL: daemon.URL})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	server.client, server.daemon = client, daemon.URL

	front := httptest.NewServer(server.routes())
	t.Cleanup(front.Close)
	browser := newBrowser(t, front.URL)
	id := browserCreate(t, browser, front.URL)

	socketURL := strings.Replace(front.URL, "http://", "ws://", 1) + "/api/conversations/" + id + "/socket"
	conn, handshake, err := (&websocket.Dialer{Jar: browser.Jar}).Dial(socketURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = handshake.Body.Close() }()
	defer func() { _ = conn.Close() }()
	if err := conn.WriteJSON(map[string]string{"type": "message", "text": "帮我看下日志\n第二行"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	for {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		var frame map[string]any
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatalf("read: %v", err)
		}
		if frame["type"] != "conversation" {
			continue
		}
		row, _ := frame["conversation"].(map[string]any)
		if row["title"] != "帮我看下日志" {
			t.Fatalf("the sidebar row was not named after the first message: %v", row)
		}
		if row["started"] != true {
			t.Fatalf("the row does not record that the conversation has run: %v", row)
		}
		return
	}
}

// newBrowser signs in and keeps the cookie, the way a page does.
func newBrowser(t *testing.T, base string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("jar: %v", err)
	}
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	response, err := client.Post(base+"/api/login", "application/json",
		strings.NewReader(`{"user":"alice","password":"alice-password"}`))
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("sign in: %s", response.Status)
	}
	if parsed, err := url.Parse(base); err == nil && len(jar.Cookies(parsed)) == 0 {
		t.Fatal("sign in left no cookie")
	}
	return client
}

func browserCreate(t *testing.T, browser *http.Client, base string) string {
	t.Helper()
	response, err := browser.Post(base+"/api/conversations", "application/json",
		strings.NewReader(`{"projectId":"p","agentName":"a"}`))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.ID
}

package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chaitin/agent-compose/sdk/go/chat"
)

const (
	// writeWait bounds a single frame write.
	writeWait = 10 * time.Second
	// pongWait is how long a viewer may go silent before it is dropped, and
	// pingPeriod how often it is prodded. Proxies close idle connections, and a
	// conversation is idle most of the time.
	pongWait   = 60 * time.Second
	pingPeriod = 25 * time.Second
)

// clientFrame is what the browser sends.
type clientFrame struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// socket serves one viewer of one conversation.
//
// The browser and this server hold the conversation over a duplex connection
// because that is what a conversation is; this server and the daemon hold it
// over the SDK's duplex stream for the same reason. Neither side has to
// pretend a long-lived exchange is a sequence of unrelated requests.
func (s *uiServer) socket(w http.ResponseWriter, r *http.Request) {
	found, ok := s.lookup(w, r)
	if !ok {
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has already written a response.
		return
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	found.join()
	defer found.leave()

	// One writer goroutine: a WebSocket permits only one concurrent write, and
	// turns and pings are produced independently.
	outbound := make(chan any, 64)
	go writeFrames(ctx, cancel, conn, outbound)

	emit := func(frame any) bool {
		select {
		case outbound <- frame:
			return true
		case <-ctx.Done():
			return false
		}
	}
	go streamTurns(ctx, found, emit)

	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		var frame clientFrame
		if err := conn.ReadJSON(&frame); err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				slog.Debug("chat socket ended", "error", err)
			}
			return
		}
		switch frame.Type {
		case "message":
			if err := found.send(ctx, frame.Text); err != nil {
				emit(map[string]any{"type": "error", "message": err.Error(), "busy": errors.Is(err, chat.ErrBusy)})
			}
		case "stop":
			stopped, err := found.interrupt(ctx)
			if err != nil {
				emit(map[string]any{"type": "error", "message": err.Error()})
				continue
			}
			// The daemon cancels the whole session rather than one turn, so
			// the conversation restarts on the next message. Say so instead of
			// letting the page pretend the agent kept its context.
			emit(map[string]any{"type": "stopped", "stopped": stopped, "contextLost": stopped})
		}
	}
}

// streamTurns relays every turn on the conversation, including turns this
// viewer did not start and one already in flight when it connected.
func streamTurns(ctx context.Context, found *session, emit func(any) bool) {
	for reply, prompt := range found.turns(ctx) {
		if !emit(map[string]any{"type": "turn_started", "prompt": prompt}) {
			return
		}
		for event, err := range reply.Events(ctx) {
			if err != nil {
				emit(map[string]any{"type": "error", "message": err.Error()})
				break
			}
			if !emit(map[string]any{"type": "event", "event": renderEvent(event)}) {
				return
			}
		}
		message, err := reply.Wait(ctx)
		if err != nil {
			if !emit(map[string]any{"type": "error", "message": err.Error()}) {
				return
			}
			continue
		}
		if !emit(map[string]any{
			"type":       "turn_done",
			"text":       message.Text,
			"result":     json.RawMessage(cmp.Or(string(message.Result), "null")),
			"continuity": found.conversation.Continuity(),
		}) {
			return
		}
	}
}

// writeFrames owns the connection's write side and keeps it alive through
// proxies that close idle connections.
func writeFrames(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, outbound <-chan any) {
	defer cancel()
	ping := time.NewTicker(pingPeriod)
	defer ping.Stop()
	for {
		select {
		case frame := <-outbound:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteJSON(frame); err != nil {
				return
			}
		case <-ping.C:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-ctx.Done():
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			_ = conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return
		}
	}
}

// sameOriginOnly accepts a handshake only from the page this server serves.
//
// A WebSocket handshake is not subject to the same-origin policy, so a
// permissive check would let any site a viewer visits drive their
// conversations. allowed adds explicit origins for a separately hosted page.
func sameOriginOnly(allowed []string) func(*http.Request) bool {
	permitted := make(map[string]struct{}, len(allowed))
	for _, origin := range allowed {
		if origin = strings.TrimSpace(origin); origin != "" {
			permitted[strings.ToLower(origin)] = struct{}{}
		}
	}
	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			// Not a browser; there is no origin to lie about.
			return true
		}
		if _, ok := permitted[strings.ToLower(origin)]; ok {
			return true
		}
		parsed, err := url.Parse(origin)
		return err == nil && strings.EqualFold(parsed.Host, r.Host)
	}
}

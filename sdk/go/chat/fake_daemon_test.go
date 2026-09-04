package chat

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// fakeDaemon is a stand-in for agent-compose speaking the Connect protocol:
// unary JSON for the run procedures and a bidirectional stream for
// AttachAgentRun. It serves h2c because Connect needs HTTP/2 for streams.
type fakeDaemon struct {
	t      *testing.T
	server *httptest.Server

	// unary maps a procedure name to its canned response.
	unary map[string]any
	// attach drives the conversation stream.
	attach func(*fakeStream)

	// hold keeps handlers that emulate a detached session parked until the
	// test ends.
	hold chan struct{}

	mu    sync.Mutex
	calls []string
}

func (d *fakeDaemon) record(procedure string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, procedure)
}

// count reports how many times a procedure was called.
func (d *fakeDaemon) count(procedure string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	total := 0
	for _, call := range d.calls {
		if call == procedure {
			total++
		}
	}
	return total
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	daemon := &fakeDaemon{t: t, unary: map[string]any{}, hold: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc(runService+"AttachAgentRun", daemon.handleAttach)
	mux.HandleFunc(runService, daemon.handleUnary)
	daemon.server = httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(daemon.server.Close)
	// Registered second so it runs first: Close waits on outstanding handlers,
	// and a parked one would never return.
	t.Cleanup(func() { close(daemon.hold) })
	return daemon
}

func (d *fakeDaemon) client(t *testing.T) *Client {
	t.Helper()
	client, err := New(Config{BaseURL: d.server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func (d *fakeDaemon) procedure(path string) string {
	return path[len(runService):]
}

func (d *fakeDaemon) handleUnary(w http.ResponseWriter, r *http.Request) {
	procedure := d.procedure(r.URL.Path)
	d.record(procedure)
	response, ok := d.unary[procedure]
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "not_found", "message": procedure + " is not configured"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		d.t.Errorf("encode %s response: %v", procedure, err)
	}
}

func (d *fakeDaemon) handleAttach(w http.ResponseWriter, r *http.Request) {
	d.record("AttachAgentRun")
	if r.ProtoMajor < 2 {
		d.t.Errorf("attach arrived over HTTP/%d, want HTTP/2", r.ProtoMajor)
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		d.t.Fatalf("response writer cannot flush")
	}
	w.Header().Set("Content-Type", streamMediaType)
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	stream := &fakeStream{t: d.t, body: r.Body, out: w, flush: flusher.Flush, hold: d.hold}
	if d.attach != nil {
		d.attach(stream)
	}
	stream.end(nil)
}

// fakeStream reads and writes Connect envelopes for one attach.
type fakeStream struct {
	t     *testing.T
	body  io.ReadCloser
	out   io.Writer
	flush func()
	// hold blocks a handler that should keep the session open the way a
	// detached daemon does. It is closed when the test's server shuts down.
	hold chan struct{}
}

// recv reads the next client frame, reporting false once the client stops
// sending. It never fails the test itself: this runs on the server's
// goroutine, which may outlive the test.
func (s *fakeStream) recv() (wireAttachRequest, bool) {
	var header [5]byte
	if _, err := io.ReadFull(s.body, header[:]); err != nil {
		return wireAttachRequest{}, false
	}
	payload := make([]byte, binary.BigEndian.Uint32(header[1:]))
	if _, err := io.ReadFull(s.body, payload); err != nil {
		return wireAttachRequest{}, false
	}
	var frame wireAttachRequest
	if err := json.Unmarshal(payload, &frame); err != nil {
		return wireAttachRequest{}, false
	}
	return frame, true
}

func (s *fakeStream) send(frame wireAttachResponse) {
	s.write(0, frame)
}

// agentEvent sends one provider-neutral agent event.
func (s *fakeStream) agentEvent(kind string, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	s.send(wireAttachResponse{AgentEvent: &wireAgentEvent{Name: kind, PayloadJSON: string(encoded)}})
}

func (s *fakeStream) end(failure *wireError) {
	s.write(flagEndStream, endStream{Error: failure})
}

func (s *fakeStream) write(flags byte, message any) {
	payload, err := json.Marshal(message)
	if err != nil {
		return
	}
	var header [5]byte
	header[0] = flags
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if _, err := s.out.Write(header[:]); err != nil {
		return
	}
	if _, err := s.out.Write(payload); err != nil {
		return
	}
	s.flush()
}

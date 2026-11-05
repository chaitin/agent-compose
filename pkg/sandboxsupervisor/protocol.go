package sandboxsupervisor

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// ProtocolVersion is the supervisor handshake version. A supervisor refuses a
// version it does not speak; the token is checked before anything else is
// trusted.
const ProtocolVersion = 1

// Handshake operations.
const (
	// OpPing checks readiness and the token without opening a destination.
	OpPing = "ping"
	// OpConnect requests one relayed connection to Destination.
	OpConnect = "connect"
)

// Limits bound what an untrusted client can allocate before authentication.
const (
	// MaxHandshakeBytes bounds one JSON handshake line, including the token.
	MaxHandshakeBytes = 8192
	// HandshakeTimeout bounds reading one request and writing one response.
	HandshakeTimeout = 10 * time.Second
)

// ErrHandshakeTooLarge reports a client that did not terminate its handshake
// line within MaxHandshakeBytes.
var ErrHandshakeTooLarge = errors.New("sandbox supervisor handshake is too large")

// Request is the line-oriented client handshake. Only OpConnect uses the
// destination and attribution fields; OpPing uses the token alone.
type Request struct {
	Version   int    `json:"version"`
	Op        string `json:"op"`
	Token     string `json:"token"`
	SandboxID string `json:"sandbox_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Marker    string `json:"marker,omitempty"`
	PID       int    `json:"pid,omitempty"`
	Binary    string `json:"binary,omitempty"`
	// Destination is "host:port" in the form the workload asked for. A
	// synthetic DNS answer may have been mapped back to its name first.
	Destination string `json:"destination,omitempty"`
}

// Response answers one handshake. Reason is safe to log: it never carries the
// token, the proxy credential, or another secret.
type Response struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// WriteRequest writes one JSON-framed request.
func WriteRequest(writer io.Writer, request Request) error {
	encoded, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode sandbox supervisor request: %w", err)
	}
	if len(encoded) > MaxHandshakeBytes {
		return ErrHandshakeTooLarge
	}
	if _, err := writer.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("write sandbox supervisor request: %w", err)
	}
	return nil
}

// ReadRequest reads one JSON-framed request, bounding the line length before
// decoding so an unauthenticated client cannot make the supervisor allocate an
// unbounded buffer.
func ReadRequest(reader *bufio.Reader) (Request, error) {
	line, err := readFrame(reader, MaxHandshakeBytes)
	if err != nil {
		return Request{}, err
	}
	var request Request
	if err := json.Unmarshal(line, &request); err != nil {
		return Request{}, fmt.Errorf("decode sandbox supervisor request: %w", err)
	}
	return request, nil
}

// WriteResponse writes one JSON-framed response.
func WriteResponse(writer io.Writer, response Response) error {
	encoded, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode sandbox supervisor response: %w", err)
	}
	if _, err := writer.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("write sandbox supervisor response: %w", err)
	}
	return nil
}

// ReadResponse reads one JSON-framed response.
func ReadResponse(reader *bufio.Reader) (Response, error) {
	line, err := readFrame(reader, MaxHandshakeBytes)
	if err != nil {
		return Response{}, err
	}
	var response Response
	if err := json.Unmarshal(line, &response); err != nil {
		return Response{}, fmt.Errorf("decode sandbox supervisor response: %w", err)
	}
	return response, nil
}

// readFrame reads one newline-terminated frame of at most limit bytes.
func readFrame(reader *bufio.Reader, limit int) ([]byte, error) {
	frame := make([]byte, 0, 256)
	for len(frame) <= limit {
		value, err := reader.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) && len(frame) > 0 {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
		if value == '\n' {
			return frame, nil
		}
		frame = append(frame, value)
	}
	return nil, ErrHandshakeTooLarge
}

package sandboxsupervisor

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/egress"
)

func (s *Server) handleConnect(ctx context.Context, conn net.Conn, reader *bufio.Reader, request Request) {
	destination, err := NormalizeEndpoint(request.Destination)
	if err != nil {
		_ = WriteResponse(conn, Response{Reason: "invalid destination"})
		return
	}
	upstream, upstreamReader, err := s.open(ctx, request, destination)
	if err != nil {
		s.logger.Info("sandbox supervisor denied a destination", "sandbox_id", s.options.SandboxID, "destination", destination, "error", err)
		_ = WriteResponse(conn, Response{Reason: "destination unavailable"})
		return
	}
	defer func() { _ = upstream.Close() }()
	if err := WriteResponse(conn, Response{OK: true}); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	s.logger.Debug("sandbox supervisor relay started", "sandbox_id", s.options.SandboxID, "destination", destination)
	relay(conn, reader, upstream, upstreamReader)
}

// open connects the relay to the destination. Engine-owned endpoints are
// connected directly; everything else is a CONNECT through the daemon proxy,
// so the daemon remains the policy decision point for ordinary egress.
func (s *Server) open(ctx context.Context, request Request, destination string) (net.Conn, *bufio.Reader, error) {
	dialer := &net.Dialer{Timeout: s.timeout}
	if _, direct := s.direct[destination]; direct {
		conn, err := dialer.DialContext(ctx, "tcp", destination)
		if err != nil {
			return nil, nil, fmt.Errorf("connect engine endpoint: %w", err)
		}
		return conn, bufio.NewReader(conn), nil
	}
	if s.proxy == nil {
		return nil, nil, errors.New("no daemon proxy is configured")
	}
	conn, err := dialer.DialContext(ctx, "tcp", s.proxy.address)
	if err != nil {
		return nil, nil, fmt.Errorf("connect daemon proxy: %w", err)
	}
	if err := conn.SetDeadline(time.Now().Add(s.timeout)); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	requestBytes := s.connectRequest(request, destination)
	if _, err := conn.Write(requestBytes); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("write proxy CONNECT: %w", err)
	}
	reader := bufio.NewReader(conn)
	if err := readProxyResponse(reader); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return conn, reader, nil
}

func (s *Server) connectRequest(request Request, destination string) []byte {
	var builder strings.Builder
	builder.WriteString("CONNECT ")
	builder.WriteString(destination)
	builder.WriteString(" HTTP/1.1\r\nHost: ")
	builder.WriteString(destination)
	builder.WriteString("\r\n")
	if s.proxy.authHeader != "" {
		builder.WriteString("Proxy-Authorization: ")
		builder.WriteString(s.proxy.authHeader)
		builder.WriteString("\r\n")
	}
	writeHeader(&builder, egress.IdentityHeaderSandbox, s.options.SandboxID)
	writeHeader(&builder, egress.IdentityHeaderSession, request.SessionID)
	writeHeader(&builder, egress.IdentityHeaderMarker, request.Marker)
	if request.PID > 0 {
		writeHeader(&builder, egress.IdentityHeaderPID, strconv.Itoa(request.PID))
	}
	if request.Binary != "" {
		writeHeader(&builder, egress.IdentityHeaderBinary, base64.StdEncoding.EncodeToString([]byte(request.Binary)))
	}
	builder.WriteString("\r\n")
	return []byte(builder.String())
}

func writeHeader(builder *strings.Builder, name, value string) {
	value = strings.Map(func(character rune) rune {
		if character == '\r' || character == '\n' || character == '\x00' {
			return -1
		}
		return character
	}, strings.TrimSpace(value))
	if value == "" {
		return
	}
	builder.WriteString(name)
	builder.WriteString(": ")
	builder.WriteString(value)
	builder.WriteString("\r\n")
}

func readProxyResponse(reader *bufio.Reader) error {
	statusLine, err := readHeaderLine(reader)
	if err != nil {
		return fmt.Errorf("read proxy CONNECT response: %w", err)
	}
	fields := strings.Fields(statusLine)
	if len(fields) < 2 {
		return fmt.Errorf("malformed proxy CONNECT response %q", statusLine)
	}
	switch fields[1] {
	case "200":
		return nil
	case "403", "407":
		return errors.New("the daemon proxy denied the destination")
	default:
		return fmt.Errorf("the daemon proxy answered %s", fields[1])
	}
}

// readHeaderLine reads until the empty line that terminates the response
// header, bounding the total size.
func readHeaderLine(reader *bufio.Reader) (string, error) {
	status := ""
	total := 0
	for {
		line, err := reader.ReadString('\n')
		total += len(line)
		if err != nil {
			return "", err
		}
		if total > proxyHeaderLimit {
			return "", errors.New("proxy CONNECT response header is too large")
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			return status, nil
		}
		if status == "" {
			status = trimmed
		}
	}
}

// relay copies bytes in both directions until either side closes. Closing
// both connections after the first EOF prevents the second copy goroutine from
// pinning the supervisor after the workload is gone. The readers are preserved
// because the handshake reader may already hold bytes the client pipelined.
func relay(client net.Conn, clientReader io.Reader, upstream net.Conn, upstreamReader io.Reader) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, clientReader)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, upstreamReader)
		done <- struct{}{}
	}()
	<-done
	_ = client.Close()
	_ = upstream.Close()
	<-done
}

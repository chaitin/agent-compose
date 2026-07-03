package llms

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
)

func BearerToken(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 7 && strings.EqualFold(value[:7], "Bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}

func RuntimeFacadeToken(header http.Header) string {
	if token := BearerToken(header.Get("Authorization")); token != "" {
		return token
	}
	return strings.TrimSpace(header.Get("x-api-key"))
}

func CopyRuntimeHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		if ForbiddenRuntimeHeader(key) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func CopyRuntimeResponseHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		if ForbiddenRuntimeResponseHeader(key) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func ForbiddenRuntimeResponseHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "content-length", "content-encoding":
		return true
	default:
		return false
	}
}

func CopyRuntimeResponseBody(dst io.Writer, resp *http.Response) error {
	if resp == nil || resp.Body == nil {
		return nil
	}
	if !RuntimeResponseShouldFlush(resp.Header) {
		_, err := io.Copy(dst, resp.Body)
		return err
	}
	flusher, ok := dst.(http.Flusher)
	if !ok {
		_, err := io.Copy(dst, resp.Body)
		return err
	}
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			flusher.Flush()
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

func RuntimeResponseShouldFlush(header http.Header) bool {
	contentType := strings.ToLower(header.Get("Content-Type"))
	return strings.Contains(contentType, "text/event-stream")
}

func ForbiddenRuntimeHeader(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return true
	}
	if RuntimeHeaderNameContainsSensitiveToken(lower) {
		return true
	}
	switch lower {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "host", "content-length", "accept-encoding":
		return true
	default:
		return false
	}
}

func RuntimeHeaderNameContainsSensitiveToken(lower string) bool {
	parts := strings.FieldsFunc(lower, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	})
	for _, part := range parts {
		switch part {
		case "token", "secret", "apikey", "auth":
			return true
		}
	}
	return strings.Contains(lower, "api-key")
}

func ReadRawSSEEvents(r io.Reader, handle func(protocolbridge.RawStreamEvent) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var event protocolbridge.RawStreamEvent
	var data []string
	flush := func() error {
		if event.Event == "" && event.ID == "" && event.Retry == nil && len(data) == 0 {
			return nil
		}
		event.Data = []byte(strings.Join(data, "\n"))
		if err := handle(event); err != nil {
			return err
		}
		event = protocolbridge.RawStreamEvent{}
		data = nil
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if ok && strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		if !ok {
			field = line
			value = ""
		}
		switch field {
		case "event":
			event.Event = value
		case "data":
			data = append(data, value)
		case "id":
			event.ID = value
		case "retry":
			if retry, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				event.Retry = &retry
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}

func WriteRawSSEEvent(w io.Writer, event protocolbridge.RawStreamEvent) error {
	if event.ID != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", event.ID); err != nil {
			return err
		}
	}
	if event.Event != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event.Event); err != nil {
			return err
		}
	}
	for _, line := range strings.Split(string(event.Data), "\n") {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	if event.Retry != nil {
		if _, err := fmt.Fprintf(w, "retry: %d\n", *event.Retry); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "\n")
	return err
}

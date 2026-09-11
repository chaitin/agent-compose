package capability

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// OctoBusError preserves structured errors returned by the OctoBus admin API.
type OctoBusError struct {
	HTTPStatus int
	Code       string
	Message    string
	Details    json.RawMessage
}

func (e *OctoBusError) Error() string {
	if e == nil {
		return ""
	}
	code := strings.TrimSpace(e.Code)
	message := strings.TrimSpace(e.Message)
	switch {
	case code != "" && message != "":
		return fmt.Sprintf("octobus returned HTTP %d: %s: %s", e.HTTPStatus, code, message)
	case message != "":
		return fmt.Sprintf("octobus returned HTTP %d: %s", e.HTTPStatus, message)
	case code != "":
		return fmt.Sprintf("octobus returned HTTP %d: %s", e.HTTPStatus, code)
	default:
		return fmt.Sprintf("octobus returned HTTP %d", e.HTTPStatus)
	}
}

func octobusHTTPError(status int, body []byte) error {
	upstream := &OctoBusError{HTTPStatus: status, Code: fmt.Sprintf("HTTP_%d", status), Message: http.StatusText(status)}
	var payload struct {
		Error struct {
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Details json.RawMessage `json:"details"`
		} `json:"error"`
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		code := strings.TrimSpace(payload.Error.Code)
		if code == "" {
			code = strings.TrimSpace(payload.Code)
		}
		if code != "" {
			upstream.Code = code
		}
		message := strings.TrimSpace(payload.Error.Message)
		if message == "" {
			message = strings.TrimSpace(payload.Message)
		}
		if message != "" {
			upstream.Message = message
		}
		details := payload.Error.Details
		if len(details) == 0 {
			details = payload.Details
		}
		if len(details) > 0 {
			upstream.Details = append(json.RawMessage(nil), details...)
		}
	}
	return upstream
}

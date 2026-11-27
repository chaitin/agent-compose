package eventstream

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrInvalidCursor reports a malformed opaque cursor. An expired cursor is not
// an error: the broker reports SyncRequired so the caller can reconcile.
var ErrInvalidCursor = errors.New("invalid cursor")

// Cursor identifies a position in one daemon epoch. Epochs change on daemon
// restart and when the broker is reclaimed after being idle, so a cursor from
// an old epoch is never silently applied to a new stream.
type Cursor struct {
	Epoch    string
	Sequence uint64
}

// EncodeCursor returns the opaque token sent to clients. The encoding is not
// part of the contract; clients must treat it as an opaque string.
func EncodeCursor(epoch string, sequence uint64) string {
	raw := epoch + ":" + strconv.FormatUint(sequence, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses a token produced by EncodeCursor.
func DecodeCursor(token string) (Cursor, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Cursor{}, fmt.Errorf("%w: empty", ErrInvalidCursor)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: %w", ErrInvalidCursor, err)
	}
	epoch, sequenceText, ok := strings.Cut(string(decoded), ":")
	if !ok || strings.TrimSpace(epoch) == "" || strings.Contains(sequenceText, ":") {
		return Cursor{}, fmt.Errorf("%w: malformed payload", ErrInvalidCursor)
	}
	sequence, err := strconv.ParseUint(sequenceText, 10, 64)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: %w", ErrInvalidCursor, err)
	}
	return Cursor{Epoch: epoch, Sequence: sequence}, nil
}

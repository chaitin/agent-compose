package agentinput

import (
	"crypto/sha256"
	"encoding/hex"
)

// Test fixtures are only magic-byte prefixes: admission sniffs the header, it
// never decodes the image.
func pngBytes() []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
}

func jpegBytes() []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 32)...)
}

func gifBytes() []byte {
	return append([]byte("GIF89a"), make([]byte, 32)...)
}

func webpBytes() []byte {
	data := append([]byte("RIFF"), 0, 0, 0, 0)
	data = append(data, []byte("WEBPVP8 ")...)
	return append(data, make([]byte, 32)...)
}

func pdfBytes() []byte {
	return append([]byte("%PDF-1.7\n"), make([]byte, 32)...)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

package main

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/joho/godotenv"
)

const dotenvTestSecret = "sk-super-secret-credential"

// TestSanitizeDotenvErrorDropsFileContent pins the daemon and CLI boundary: the
// parser echoes the unparsed remainder of the file, which for a deployment .env
// is where the provider keys live, so the surfaced message must not contain it.
func TestSanitizeDotenvErrorDropsFileContent(t *testing.T) {
	content := "GOOD=1\nmy-key=SECRETVALUE\nLLM_API_KEY=" + dotenvTestSecret + "\nANOTHER=2\n"
	_, err := godotenv.Unmarshal(content)
	if err == nil {
		t.Fatal("godotenv.Unmarshal() accepted an invalid variable name")
	}
	if !strings.Contains(err.Error(), dotenvTestSecret) {
		t.Fatalf("test premise broken: the parser error does not carry the credential: %v", err)
	}

	sanitized := sanitizeDotenvError(err).Error()
	if strings.Contains(sanitized, dotenvTestSecret) {
		t.Fatalf("sanitized error leaked the credential: %q", sanitized)
	}
	if sanitized != `unexpected character "-" in variable name` {
		t.Fatalf("sanitized error = %q, want the offending character kept", sanitized)
	}
}

func TestSanitizeDotenvErrorDropsUnterminatedValue(t *testing.T) {
	content := "TOKEN=\"" + dotenvTestSecret + "\nNEXT=1\n"
	_, err := godotenv.Unmarshal(content)
	if err == nil {
		t.Fatal("godotenv.Unmarshal() accepted an unterminated quoted value")
	}
	if !strings.Contains(err.Error(), dotenvTestSecret) {
		t.Fatalf("test premise broken: the parser error does not carry the credential: %v", err)
	}

	sanitized := sanitizeDotenvError(err).Error()
	if strings.Contains(sanitized, dotenvTestSecret) {
		t.Fatalf("sanitized error leaked the credential: %q", sanitized)
	}
	if !strings.Contains(sanitized, "unterminated quoted value") {
		t.Fatalf("sanitized error lost the diagnosis: %q", sanitized)
	}
}

// TestSanitizeDotenvErrorSummarizesUnknownShape keeps the boundary fail-closed:
// a message shape the sanitizer does not recognize is summarized instead of
// echoed, so a future parser message cannot reintroduce the leak.
func TestSanitizeDotenvErrorSummarizesUnknownShape(t *testing.T) {
	cause := errors.New(`some future parser message near "LLM_API_KEY=` + dotenvTestSecret + `"`)
	sanitized := sanitizeDotenvError(cause)
	if strings.Contains(sanitized.Error(), dotenvTestSecret) {
		t.Fatalf("unrecognized shape leaked content: %q", sanitized.Error())
	}
	if !errors.Is(sanitized, cause) {
		t.Fatalf("sanitized error lost the original in its chain")
	}
}

// TestSanitizeDotenvErrorKeepsFilesystemErrors: a read failure names a path and
// carries no file content, so it keeps its original message and chain.
func TestSanitizeDotenvErrorKeepsFilesystemErrors(t *testing.T) {
	cause := &fs.PathError{Op: "open", Path: "/srv/app/.env", Err: fs.ErrNotExist}
	sanitized := sanitizeDotenvError(cause)
	if sanitized.Error() != cause.Error() {
		t.Fatalf("filesystem error was rewritten: %q", sanitized.Error())
	}
	if !errors.Is(sanitized, fs.ErrNotExist) {
		t.Fatalf("filesystem error lost its chain")
	}
}

func TestSanitizeDotenvErrorNil(t *testing.T) {
	if err := sanitizeDotenvError(nil); err != nil {
		t.Fatalf("sanitizeDotenvError(nil) = %v", err)
	}
}

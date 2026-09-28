package main

import (
	"errors"
	"io/fs"
	"strings"
)

// godotenv embeds the offending file content in its parse errors: the
// name-validation error reports the unparsed remainder of the file
// (`unexpected character %q in variable name near %q`), and an unterminated
// quote reports the value itself (`unterminated quoted value %s`). Either form
// can carry every later KEY=value line — including provider credentials — into
// the daemon log or a CLI error, so only the part that describes the syntax
// problem may be surfaced.
const (
	dotenvUnexpectedCharacterPrefix = "unexpected character "
	dotenvUnexpectedCharacterSuffix = " in variable name"
)

// sanitizeDotenvError returns an error whose message cannot carry dotenv file
// content. The original failure stays in the chain for errors.Is/errors.As.
//
// A filesystem failure names a path and is returned unchanged. Every other
// failure from the dotenv parser embeds content, so it is reduced to a
// description of the syntax problem; an unrecognized message is summarized
// rather than echoed, so a future godotenv message cannot reintroduce the leak.
func sanitizeDotenvError(err error) error {
	if err == nil {
		return nil
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return err
	}
	return &dotenvParseError{cause: err}
}

// dotenvParseError is a parse failure whose message never contains file content.
type dotenvParseError struct {
	cause error
}

func (e *dotenvParseError) Error() string { return dotenvParseSummary(e.cause.Error()) }

func (e *dotenvParseError) Unwrap() error { return e.cause }

func dotenvParseSummary(message string) string {
	// The unexpected-character error is the common case, and its offending
	// character is quoted before the file content, so it can be kept.
	if rest, ok := strings.CutPrefix(message, dotenvUnexpectedCharacterPrefix); ok {
		if character, _, found := strings.Cut(rest, dotenvUnexpectedCharacterSuffix); found {
			return dotenvUnexpectedCharacterPrefix + character + dotenvUnexpectedCharacterSuffix
		}
	}
	switch {
	case strings.Contains(message, "unterminated quoted value"):
		return "unterminated quoted value"
	case strings.Contains(message, "zero length string"):
		return "empty variable name"
	default:
		return "unparseable variable definition"
	}
}

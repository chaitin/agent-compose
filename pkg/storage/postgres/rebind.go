package postgres

import (
	"fmt"
	"strings"
)

// maxBindParameters is PostgreSQL's protocol limit (65535).
const maxBindParameters = 65535

// rebindQuery rewrites SQLite-style '?' placeholders to PostgreSQL's '$n'
// positional parameters. The storage packages keep one SQL source; only the
// Postgres driver translates it.
//
// The scanner deliberately skips quoted strings, quoted identifiers, comments,
// and dollar-quoted bodies. agent-compose SQL uses '?' only as a placeholder:
// PostgreSQL's JSON '?' operator is not used, and '?|' / '?&' are preserved in
// case a future statement needs them.
func rebindQuery(query string) (string, error) {
	if !strings.ContainsRune(query, '?') {
		return query, nil
	}

	var out strings.Builder
	out.Grow(len(query) + 8)
	arg := 0
	for i := 0; i < len(query); {
		switch query[i] {
		case '\'':
			next := copySingleQuoted(&out, query, i)
			i = next
		case '"':
			next := copyDoubleQuoted(&out, query, i)
			i = next
		case '-':
			if i+1 < len(query) && query[i+1] == '-' {
				next := copyLineComment(&out, query, i)
				i = next
			} else {
				out.WriteByte(query[i])
				i++
			}
		case '/':
			if i+1 < len(query) && query[i+1] == '*' {
				next := copyBlockComment(&out, query, i)
				i = next
			} else {
				out.WriteByte(query[i])
				i++
			}
		case '$':
			if next, ok := copyDollarQuoted(&out, query, i); ok {
				i = next
			} else {
				if i+1 < len(query) && query[i+1] >= '0' && query[i+1] <= '9' {
					return "", fmt.Errorf("query mixes $n parameters with ? placeholders")
				}
				out.WriteByte(query[i])
				i++
			}
		case '?':
			if i+1 < len(query) && (query[i+1] == '|' || query[i+1] == '&') {
				// PostgreSQL JSON existence operators, not placeholders.
				out.WriteByte(query[i])
				i++
				continue
			}
			arg++
			if arg > maxBindParameters {
				return "", fmt.Errorf("query has more than %d parameters", maxBindParameters)
			}
			fmt.Fprintf(&out, "$%d", arg)
			i++
		default:
			out.WriteByte(query[i])
			i++
		}
	}
	return out.String(), nil
}

// copySingleQuoted copies one single-quoted SQL string starting at start and
// returns the index just past it. Backslash escapes are honored only for E”
// strings, matching standard_conforming_strings=on for ordinary literals.
func copySingleQuoted(out *strings.Builder, query string, start int) int {
	escapes := start > 0 && (query[start-1] == 'E' || query[start-1] == 'e') &&
		(start < 2 || !isIdentifierByte(query[start-2]))
	out.WriteByte(query[start])
	i := start + 1
	for i < len(query) {
		if escapes && query[i] == '\\' {
			if i+1 < len(query) {
				out.WriteByte(query[i])
				out.WriteByte(query[i+1])
				i += 2
				continue
			}
			out.WriteByte(query[i])
			i++
			continue
		}
		if query[i] == '\'' {
			if i+1 < len(query) && query[i+1] == '\'' {
				out.WriteString("''")
				i += 2
				continue
			}
			out.WriteByte(query[i])
			return i + 1
		}
		out.WriteByte(query[i])
		i++
	}
	return i
}

func copyDoubleQuoted(out *strings.Builder, query string, start int) int {
	out.WriteByte(query[start])
	i := start + 1
	for i < len(query) {
		if query[i] == '"' {
			if i+1 < len(query) && query[i+1] == '"' {
				out.WriteString("\"\"")
				i += 2
				continue
			}
			out.WriteByte(query[i])
			return i + 1
		}
		out.WriteByte(query[i])
		i++
	}
	return i
}

func copyLineComment(out *strings.Builder, query string, start int) int {
	i := start
	for i < len(query) {
		out.WriteByte(query[i])
		if query[i] == '\n' {
			return i + 1
		}
		i++
	}
	return i
}

func copyBlockComment(out *strings.Builder, query string, start int) int {
	out.WriteString("/*")
	i := start + 2
	depth := 1
	for i < len(query) {
		if i+1 < len(query) && query[i] == '/' && query[i+1] == '*' {
			depth++
			out.WriteString("/*")
			i += 2
			continue
		}
		if i+1 < len(query) && query[i] == '*' && query[i+1] == '/' {
			depth--
			out.WriteString("*/")
			i += 2
			if depth == 0 {
				return i
			}
			continue
		}
		out.WriteByte(query[i])
		i++
	}
	return i
}

// copyDollarQuoted recognizes $tag$...$tag$ bodies. It reports false for a
// '$' that does not start a dollar quote (for example '$1').
func copyDollarQuoted(out *strings.Builder, query string, start int) (int, bool) {
	tagEnd := start + 1
	for tagEnd < len(query) && isDollarTagByte(query[tagEnd], tagEnd == start+1) {
		tagEnd++
	}
	if tagEnd >= len(query) || query[tagEnd] != '$' {
		return start, false
	}
	tag := query[start : tagEnd+1] // includes both dollar signs
	closing := strings.Index(query[tagEnd+1:], tag)
	if closing < 0 {
		// An unterminated dollar quote is copied verbatim; the server reports
		// the syntax error.
		out.WriteString(query[start:])
		return len(query), true
	}
	end := tagEnd + 1 + closing + len(tag)
	out.WriteString(query[start:end])
	return end, true
}

func isDollarTagByte(b byte, first bool) bool {
	if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '_' {
		return true
	}
	return !first && b >= '0' && b <= '9'
}

func isIdentifierByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '$'
}

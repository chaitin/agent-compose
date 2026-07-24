package capabilities

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const capsetMetadataName = "x-octobus-capset"

// ValidateCapsetDeclaration rejects characters that cannot safely be carried
// in generated Markdown metadata instructions. Control characters can split
// an instruction across lines, while a backtick can terminate its code span.
func ValidateCapsetDeclaration(declaration string) error {
	if strings.TrimSpace(declaration) == "" {
		return errors.New("capset declaration is required")
	}
	for _, r := range declaration {
		if unicode.IsControl(r) {
			return errors.New("capset declaration must not contain control characters")
		}
		if r == '`' {
			return errors.New("capset declaration must not contain backticks")
		}
	}
	return nil
}

// QualifyCapabilityGuide adds an authoritative, agent-compose-local routing
// instruction to an upstream guide and rewrites the metadata forms generated
// by OctoBus. Only a standalone metadata assignment or an exact Markdown code
// span is rewritten; arbitrary prose and other fields are deliberately not
// interpreted as metadata.
func QualifyCapabilityGuide(guide []byte, declaration, capsetID string) []byte {
	declaration = strings.TrimSpace(declaration)
	capsetID = strings.TrimSpace(capsetID)
	if declaration == "" || capsetID == "" || declaration == capsetID {
		return guide
	}
	var result bytes.Buffer
	_, _ = fmt.Fprintf(&result, "> **Agent Compose routing:** use `x-octobus-capset: %s` for every capability call below. Any unqualified `%s` value shown by the upstream guide refers to this declaration.\n\n", declaration, capsetID)
	result.Write(rewriteCapabilityGuideMetadata(guide, declaration, capsetID))
	return result.Bytes()
}

func rewriteCapabilityGuideMetadata(guide []byte, declaration, capsetID string) []byte {
	assignment := capsetMetadataName + ": " + capsetID
	qualifiedAssignment := capsetMetadataName + ": " + declaration
	codeAssignment := "`" + assignment + "`"
	qualifiedCodeAssignment := "`" + qualifiedAssignment + "`"

	lines := bytes.SplitAfter(guide, []byte("\n"))
	for i, line := range lines {
		line = bytes.ReplaceAll(line, []byte(codeAssignment), []byte(qualifiedCodeAssignment))
		content := bytes.TrimSuffix(line, []byte("\n"))
		lineEnding := line[len(content):]
		if bytes.Equal(bytes.TrimSpace(content), []byte(assignment)) {
			leading := content[:len(content)-len(bytes.TrimLeft(content, " \t"))]
			trailing := content[len(bytes.TrimRight(content, " \t")):]
			content = append(append(append([]byte{}, leading...), qualifiedAssignment...), trailing...)
			line = append(content, lineEnding...)
		}
		lines[i] = line
	}
	return bytes.Join(lines, nil)
}

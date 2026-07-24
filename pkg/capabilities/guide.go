package capabilities

import (
	"bytes"
	"fmt"
	"strings"
)

// QualifyCapabilityGuide adds an authoritative, agent-compose-local routing
// instruction to an upstream guide. The upstream markdown format is not an
// agent-compose contract, so this avoids unsafe rewriting of arbitrary
// markdown while ensuring the guest is given the qualified declaration it
// must send to the daemon.
func QualifyCapabilityGuide(guide []byte, declaration, capsetID string) []byte {
	declaration = strings.TrimSpace(declaration)
	capsetID = strings.TrimSpace(capsetID)
	if declaration == "" || capsetID == "" || declaration == capsetID {
		return guide
	}
	var result bytes.Buffer
	_, _ = fmt.Fprintf(&result, "> **Agent Compose routing:** use `x-octobus-capset: %s` for every capability call below. Any unqualified `%s` value shown by the upstream guide refers to this declaration.\n\n", declaration, capsetID)
	result.Write(guide)
	return result.Bytes()
}

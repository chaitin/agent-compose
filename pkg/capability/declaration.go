package capability

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
)

var capsetIDPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,62}$`)

// CapsetDeclaration identifies either a global capset or a project server and capset pair.
type CapsetDeclaration struct {
	ServerName string
	CapsetID   string
}

// Qualified reports whether the declaration selects a project OctoBus server.
func (d CapsetDeclaration) Qualified() bool {
	return d.ServerName != ""
}

// ParseCapsetDeclaration validates and splits an agent-compose capset declaration.
func ParseCapsetDeclaration(value string) (CapsetDeclaration, error) {
	value = strings.TrimSpace(value)
	for _, r := range value {
		if unicode.IsControl(r) {
			return CapsetDeclaration{}, errors.New("capset declaration must not contain control characters")
		}
		if r == '`' {
			return CapsetDeclaration{}, errors.New("capset declaration must not contain backticks")
		}
	}
	serverName, capsetID, qualified := strings.Cut(value, "/")
	if !qualified {
		capsetID = value
		serverName = ""
	} else if serverName == "" {
		return CapsetDeclaration{}, errors.New("octobus server name is required")
	}
	if !capsetIDPattern.MatchString(capsetID) {
		return CapsetDeclaration{}, errors.New("capset id must match ^[a-zA-Z][a-zA-Z0-9_-]{0,62}$")
	}
	return CapsetDeclaration{ServerName: serverName, CapsetID: capsetID}, nil
}

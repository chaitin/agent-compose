package agentinput

import "encoding/json"

// ManifestVersion is the guest manifest schema version the runtime checks.
const ManifestVersion = 1

// Manifest is the guest-side description of one turn: ordered text and media
// with already-materialized paths. It never carries bytes, so a large image is
// written once and named once.
type Manifest struct {
	Version int            `json:"version"`
	Parts   []ManifestPart `json:"parts"`
}

// ManifestPart is one ordered piece of the turn.
type ManifestPart struct {
	Text  string         `json:"text,omitempty"`
	Media *ManifestMedia `json:"media,omitempty"`
}

// ManifestMedia is one media part after the daemon resolved its source to a
// guest path.
type ManifestMedia struct {
	MediaType string `json:"mediaType"`
	Filename  string `json:"filename,omitempty"`
	// Path is the absolute guest path the runtime must read.
	Path string `json:"path"`
	// WorkspacePath is set when the caller referenced an existing workspace
	// file; empty means the daemon materialized the bytes itself.
	WorkspacePath string `json:"workspacePath,omitempty"`
	SizeBytes     int64  `json:"sizeBytes,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
}

// NewManifest stamps a manifest with the current schema version.
func NewManifest(parts []ManifestPart) Manifest {
	return Manifest{Version: ManifestVersion, Parts: parts}
}

// Marshal encodes the manifest the guest runtime reads.
func (m Manifest) Marshal() ([]byte, error) {
	return json.Marshal(m)
}

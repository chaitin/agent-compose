package agentinput

import "encoding/json"

// ManifestVersion is the guest manifest schema version the runtime checks.
const ManifestVersion = 1

// Manifest is the guest-side description of one turn: ordered text and media
// with already-materialized paths. It never carries bytes, so a large image is
// written once and named once.
type Manifest struct {
	Version int `json:"version"`
	// Limits are the admission limits the guest re-checks against the actual
	// files. The daemon cannot stat a workspace path on every driver, and a
	// runner otherwise reads an arbitrarily large file into memory.
	Limits ManifestLimits `json:"limits"`
	Parts  []ManifestPart `json:"parts"`
}

// ManifestLimits is the subset of Limits the guest enforces.
type ManifestLimits struct {
	MaxPartBytes uint64 `json:"maxPartBytes"`
	MaxTurnBytes uint64 `json:"maxTurnBytes"`
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
	// Reference marks a non-native type: the runtime verifies the file but
	// renders a path reference for the agent instead of attaching it.
	Reference bool `json:"reference,omitempty"`
}

// NewManifest stamps a manifest with the current schema version.
func NewManifest(parts []ManifestPart) Manifest {
	limits := DefaultLimits()
	return Manifest{
		Version: ManifestVersion,
		Limits:  ManifestLimits{MaxPartBytes: limits.MaxPartBytes, MaxTurnBytes: limits.MaxTurnBytes},
		Parts:   parts,
	}
}

// Marshal encodes the manifest the guest runtime reads.
func (m Manifest) Marshal() ([]byte, error) {
	return json.Marshal(m)
}

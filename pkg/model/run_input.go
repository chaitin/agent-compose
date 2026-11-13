package model

import "time"

// RunInputFile is an immutable declaration, never a host path or download URL.
type RunInputFile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Size         uint64 `json:"size"`
	SHA256       []byte `json:"sha256"`
	RelativePath string `json:"relative_path,omitempty"`
}

// RunInputPreparation is durable delivery evidence owned by one Run.
// Incomplete evidence after a crash is unknown; it never permits resubmission.
type RunInputPreparation struct {
	RunID          string         `json:"run_id"`
	TemporaryNames []string       `json:"temporary_names,omitempty"`
	SandboxID      string         `json:"sandbox_id,omitempty"`
	State          string         `json:"state"`
	ManifestSHA256 []byte         `json:"manifest_sha256"`
	Files          []RunInputFile `json:"files"`
	FailureReason  int32          `json:"failure_reason,omitempty"`
	Deadline       time.Time      `json:"deadline"`
}

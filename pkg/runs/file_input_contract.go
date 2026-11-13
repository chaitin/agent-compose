package runs

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/chaitin/agent-compose/internal/projects"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

const (
	MaxInputFiles           = 10
	MaxInputFileBytes       = 100 << 20
	MaxInputTotalBytes      = 200 << 20
	MaxInputChunkBytes      = 256 << 10
	InputPreparationTimeout = 5 * time.Minute
)

func newRunInput(req RunAgentRequest) (*domain.RunInputPreparation, error) {
	if len(req.InputFiles) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(req.ClientRequestID) == "" || strings.TrimSpace(req.Prompt) == "" || req.Command != "" || req.TriggerID != "" {
		return nil, fmt.Errorf("%w: files require a prompt and stable client request ID", ErrInvalidRequest)
	}
	if err := ValidateInputFiles(req.InputFiles); err != nil {
		return nil, err
	}
	files := make([]domain.RunInputFile, len(req.InputFiles))
	for i, file := range req.InputFiles {
		file.SHA256 = bytes.Clone(file.SHA256)
		files[i] = file
	}
	encoded, err := json.Marshal(files)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(encoded)
	return &domain.RunInputPreparation{State: "receiving", Files: files, ManifestSHA256: digest[:], Deadline: time.Now().UTC().Add(InputPreparationTimeout)}, nil
}

// ValidateInputFiles enforces the independently checked Runtime limits.
func ValidateInputFiles(files []domain.RunInputFile) error {
	if len(files) > MaxInputFiles {
		return fmt.Errorf("%w: too many input files", ErrInvalidRequest)
	}
	seen := make(map[string]bool, len(files))
	var total uint64
	for _, file := range files {
		if file.ID == "" || len(file.ID) > 256 || seen[file.ID] || !utf8.ValidString(file.ID) {
			return fmt.Errorf("%w: invalid or duplicate file identity", ErrInvalidRequest)
		}
		seen[file.ID] = true
		if !ValidInputFilename(file.Name) || file.RelativePath != "" || len(file.SHA256) != sha256.Size || file.Size > MaxInputFileBytes {
			return fmt.Errorf("%w: invalid input file declaration", ErrInvalidRequest)
		}
		total += file.Size
		if total > MaxInputTotalBytes {
			return fmt.Errorf("%w: input total exceeds limit", ErrInvalidRequest)
		}
	}
	return nil
}

// ValidInputFilename accepts a single portable basename, never a path.
func ValidInputFilename(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 240 && utf8.ValidString(name) &&
		!strings.ContainsAny(name, "/\\:") && !strings.HasPrefix(name, ".ac-upload-") &&
		!strings.ContainsFunc(name, unicode.IsControl)
}

func replayRunInput(input *domain.RunInputPreparation) *domain.RunInputPreparation {
	if input == nil {
		return nil
	}
	copy := *input
	if (copy.State == "receiving" || copy.State == "publishing") && !time.Now().Before(copy.Deadline) {
		copy.State = "unknown"
	}
	return &copy
}

// RequestIdentity selects the existing project/agent/source idempotency scope.
type RequestIdentity struct{ ProjectID, AgentName, Source, ClientRequestID string }

func ResolveRequestRunID(identity RequestIdentity) (string, error) {
	return projects.StableProjectRunID(strings.TrimSpace(identity.ProjectID), strings.TrimSpace(identity.AgentName), NormalizeSource(identity.Source), strings.TrimSpace(identity.ClientRequestID))
}

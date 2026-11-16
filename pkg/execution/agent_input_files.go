package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chaitin/agent-compose/pkg/agentinput"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// AgentInputFileRequest describes the daemon and guest copies of one turn's
// multimodal input.
type AgentInputFileRequest struct {
	Config         *appconfig.Config
	Sandbox        *domain.Sandbox
	RunID          string
	Input          []agentinput.Part
	WriteGuestFile GuestFileWriterFunc
}

// WriteAgentInputFiles materializes inline media and writes the guest manifest
// for one turn. It returns the guest path of the manifest, which the runtime
// reads through --content-file. An empty input returns an empty path and writes
// nothing, so a text-only run keeps its existing prompt-file path.
//
// Bytes are written under the sandbox state tree, not the workspace: they are
// turn input rather than project content, so they must not pollute an agent's
// diff and must not be reachable through the workspace file API.
func WriteAgentInputFiles(ctx context.Context, req AgentInputFileRequest) (string, error) {
	if len(req.Input) == 0 {
		return "", nil
	}
	session, runID, writeGuestFile := req.Sandbox, req.RunID, req.WriteGuestFile
	if session == nil || strings.TrimSpace(session.Summary.WorkspacePath) == "" {
		return "", fmt.Errorf("sandbox workspace path is required to write agent input")
	}
	appconfig.ApplyDefaultGuestPaths(req.Config)
	segment := runStateSegment(runID)
	hostRel := filepath.Join("state", "agents", "attachments", segment, "turn-0")
	guestRel := filepath.Join(req.Config.GuestStateRoot, "agents", "attachments", segment, "turn-0")

	hostDir := filepath.Join(filepath.Dir(session.Summary.WorkspacePath), hostRel)
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		return "", fmt.Errorf("create agent input dir: %w", err)
	}

	parts := make([]agentinput.ManifestPart, 0, len(req.Input))
	for index, part := range req.Input {
		manifestPart, err := writeAgentInputPart(ctx, agentInputPartDeps{
			config:         req.Config,
			hostDir:        hostDir,
			guestDir:       guestRel,
			index:          index,
			part:           part,
			writeGuestFile: writeGuestFile,
		})
		if err != nil {
			return "", err
		}
		parts = append(parts, manifestPart)
	}
	manifest, err := agentinput.NewManifest(parts).Marshal()
	if err != nil {
		return "", fmt.Errorf("encode agent input manifest: %w", err)
	}
	hostManifest := filepath.Join(hostDir, "content.json")
	if err := os.WriteFile(hostManifest, manifest, 0o600); err != nil {
		return "", fmt.Errorf("write agent input manifest: %w", err)
	}
	guestManifest := filepath.Join(guestRel, "content.json")
	if writeGuestFile != nil {
		if err := writeGuestFile(ctx, guestManifest, manifest); err != nil {
			return "", fmt.Errorf("push agent input manifest to guest: %w", err)
		}
	}
	return guestManifest, nil
}

type agentInputPartDeps struct {
	config         *appconfig.Config
	hostDir        string
	guestDir       string
	index          int
	part           agentinput.Part
	writeGuestFile GuestFileWriterFunc
}

func writeAgentInputPart(ctx context.Context, deps agentInputPartDeps) (agentinput.ManifestPart, error) {
	part := deps.part
	if part.Media == nil {
		return agentinput.ManifestPart{Text: part.Text}, nil
	}
	media := part.Media
	switch media.Source {
	case agentinput.SourceInline:
		name := fmt.Sprintf("part-%d", deps.index)
		hostPath := filepath.Join(deps.hostDir, name)
		if err := os.WriteFile(hostPath, media.Data, 0o600); err != nil {
			return agentinput.ManifestPart{}, fmt.Errorf("write agent input part %d: %w", deps.index, err)
		}
		guestPath := filepath.Join(deps.guestDir, name)
		if deps.writeGuestFile != nil {
			if err := deps.writeGuestFile(ctx, guestPath, media.Data); err != nil {
				return agentinput.ManifestPart{}, fmt.Errorf("push agent input part %d to guest: %w", deps.index, err)
			}
		}
		digest := media.SHA256
		if digest == "" {
			sum := sha256.Sum256(media.Data)
			digest = hex.EncodeToString(sum[:])
		}
		return agentinput.ManifestPart{Media: &agentinput.ManifestMedia{
			MediaType: media.Type,
			Filename:  media.Filename,
			Path:      guestPath,
			SizeBytes: int64(len(media.Data)),
			SHA256:    digest,
		}}, nil
	case agentinput.SourceWorkspacePath:
		cleaned, err := agentinput.NormalizeWorkspacePath(media.WorkspacePath)
		if err != nil {
			return agentinput.ManifestPart{}, fmt.Errorf("agent input part %d: %w", deps.index, err)
		}
		return agentinput.ManifestPart{Media: &agentinput.ManifestMedia{
			MediaType:     media.Type,
			Filename:      media.Filename,
			Path:          filepath.Join(deps.config.GuestWorkspacePath, cleaned),
			WorkspacePath: cleaned,
			SHA256:        media.SHA256,
		}}, nil
	default:
		return agentinput.ManifestPart{}, fmt.Errorf("agent input part %d has no source", deps.index)
	}
}

// runStateSegment keeps a run ID safe to use as one path segment. Run IDs are
// daemon-generated, so this only guards against a caller-supplied value and
// never changes a normal ID.
func runStateSegment(runID string) string {
	trimmed := strings.TrimSpace(runID)
	if trimmed == "" {
		return "run"
	}
	var builder strings.Builder
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	if builder.Len() > 128 {
		return builder.String()[:128]
	}
	return builder.String()
}

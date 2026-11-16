package execution

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chaitin/agent-compose/pkg/agentinput"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// AgentInputFileRequest describes the files of one turn.
type AgentInputFileRequest struct {
	Config         *appconfig.Config
	Sandbox        *domain.Sandbox
	RunID          string
	Input          []agentinput.Part
	Limits         agentinput.Limits
	WriteGuestFile GuestFileWriterFunc
}

// ResolvedFile is one file after the daemon placed it inside the workspace (or
// resolved an existing workspace path). The prompt references GuestPath.
type ResolvedFile struct {
	GuestPath string
	Name      string
	MediaType string
	SizeBytes int64
}

// WriteAgentInputFiles writes inline files under the sandbox workspace and
// resolves workspace references. It returns the files the prompt should name.
//
// Files live under <workspace>/.agent-compose/uploads/<run>/turn-0 so the agent
// can read them with its normal tools and later turns still find them. An empty
// input writes nothing.
func WriteAgentInputFiles(ctx context.Context, req AgentInputFileRequest) ([]ResolvedFile, error) {
	if len(req.Input) == 0 {
		return nil, nil
	}
	session, runID, writeGuestFile := req.Sandbox, req.RunID, req.WriteGuestFile
	if session == nil || strings.TrimSpace(session.Summary.WorkspacePath) == "" {
		return nil, fmt.Errorf("sandbox workspace path is required to write agent input")
	}
	appconfig.ApplyDefaultGuestPaths(req.Config)
	segment := runStateSegment(runID)
	hostDir := filepath.Join(session.Summary.WorkspacePath, ".agent-compose", "uploads", segment, "turn-0")
	guestRel := filepath.Join(".agent-compose", "uploads", segment, "turn-0")
	guestDir := filepath.Join(req.Config.GuestWorkspacePath, guestRel)
	limits := req.Limits
	if limits.MaxFileBytes == 0 {
		limits.MaxFileBytes = agentinput.DefaultMaxFileBytes
	}

	files := make([]ResolvedFile, 0, len(req.Input))
	used := make(map[string]struct{}, len(req.Input))
	for index, part := range req.Input {
		if part.File == nil {
			continue
		}
		file, err := writeAgentInputFile(ctx, agentInputFileDeps{
			config:         req.Config,
			session:        session,
			hostDir:        hostDir,
			guestDir:       guestDir,
			index:          index,
			file:           *part.File,
			limits:         limits,
			used:           used,
			writeGuestFile: writeGuestFile,
		})
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

type agentInputFileDeps struct {
	config         *appconfig.Config
	session        *domain.Sandbox
	hostDir        string
	guestDir       string
	index          int
	file           agentinput.File
	limits         agentinput.Limits
	used           map[string]struct{}
	writeGuestFile GuestFileWriterFunc
}

func writeAgentInputFile(ctx context.Context, deps agentInputFileDeps) (ResolvedFile, error) {
	file := deps.file
	switch file.Source {
	case agentinput.SourceInline:
		name := uniqueAgentInputName(deps.used, deps.index, agentinput.SafeFileName(file.Name, file.MediaType))
		if err := os.MkdirAll(deps.hostDir, 0o755); err != nil {
			return ResolvedFile{}, fmt.Errorf("create agent input dir: %w", err)
		}
		hostPath := filepath.Join(deps.hostDir, name)
		if err := os.WriteFile(hostPath, file.Data, 0o600); err != nil {
			return ResolvedFile{}, fmt.Errorf("write agent input file %d: %w", deps.index, err)
		}
		guestPath := filepath.Join(deps.guestDir, name)
		if deps.writeGuestFile != nil {
			if err := deps.writeGuestFile(ctx, guestPath, file.Data); err != nil {
				return ResolvedFile{}, fmt.Errorf("push agent input file %d to guest: %w", deps.index, err)
			}
		}
		return ResolvedFile{GuestPath: guestPath, Name: name, MediaType: file.MediaType, SizeBytes: int64(len(file.Data))}, nil
	case agentinput.SourceWorkspacePath:
		cleaned, err := agentinput.NormalizeWorkspacePath(file.WorkspacePath)
		if err != nil {
			return ResolvedFile{}, fmt.Errorf("agent input file %d: %w", deps.index, err)
		}
		if size, ok := workspaceFileSize(deps.session, cleaned); ok && deps.limits.MaxFileBytes > 0 && uint64(size) > deps.limits.MaxFileBytes {
			return ResolvedFile{}, fmt.Errorf("agent input file %d is %d bytes, limit is %d", deps.index, size, deps.limits.MaxFileBytes)
		}
		return ResolvedFile{
			GuestPath: filepath.Join(deps.config.GuestWorkspacePath, cleaned),
			Name:      filepath.Base(cleaned),
			MediaType: file.MediaType,
		}, nil
	default:
		return ResolvedFile{}, fmt.Errorf("agent input file %d has no source", deps.index)
	}
}

// workspaceFileSize reports the size of a workspace file when the daemon shares
// the sandbox filesystem. K8s does not, so the size is unknown there and the
// guest cannot be asked to check it in this design; the limit is enforced
// wherever the daemon can read the file.
func workspaceFileSize(session *domain.Sandbox, relative string) (int64, bool) {
	info, err := os.Stat(filepath.Join(session.Summary.WorkspacePath, relative))
	if err != nil || !info.Mode().IsRegular() {
		return 0, false
	}
	return info.Size(), true
}

func uniqueAgentInputName(used map[string]struct{}, index int, name string) string {
	candidate := name
	if _, exists := used[candidate]; exists {
		extension := filepath.Ext(name)
		base := strings.TrimSuffix(name, extension)
		candidate = fmt.Sprintf("%s-%d%s", base, index, extension)
	}
	used[candidate] = struct{}{}
	return candidate
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

// AppendFileReferences appends the sandbox paths of the turn's files to the
// prompt. It is the only provider-facing step: every harness receives the same
// text and reads the files with its own tools.
func AppendFileReferences(message string, files []ResolvedFile) string {
	if len(files) == 0 {
		return message
	}
	var builder strings.Builder
	builder.WriteString(message)
	builder.WriteString("\n\nAttached files (read them with your file tools):")
	for _, file := range files {
		builder.WriteString("\n- ")
		builder.WriteString(file.GuestPath)
		if file.MediaType != "" {
			builder.WriteString(" (")
			builder.WriteString(file.MediaType)
			builder.WriteString(")")
		}
	}
	return builder.String()
}

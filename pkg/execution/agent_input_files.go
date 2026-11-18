package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chaitin/agent-compose/pkg/agentinput"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// AgentInputFileRequest describes the files of one turn.
type AgentInputFileRequest struct {
	Config  *appconfig.Config
	Sandbox *domain.Sandbox
	RunID   string
	// Turn numbers the per-run upload directory (0 is the opening turn).
	Turn           int
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
	turnDir := fmt.Sprintf("turn-%d", req.Turn)
	uploadRel := filepath.Join(".agent-compose", "uploads", segment, turnDir)
	guestDir := filepath.Join(req.Config.GuestWorkspacePath, uploadRel)
	// A nil writer is the established marker for a driver that shares the
	// sandbox filesystem (docker/boxlite/microsandbox). A non-nil writer means
	// only the guest can see the workspace (K8s), so a daemon-local path must
	// not be mistaken for the pod's file.
	workspaceShared := writeGuestFile == nil
	limits := req.Limits
	if limits.MaxFileBytes == 0 {
		limits.MaxFileBytes = agentinput.DefaultMaxFileBytes
	}

	files := make([]ResolvedFile, 0, len(req.Input))
	used := make(map[string]struct{}, len(req.Input))
	var totalBytes int64
	for index, part := range req.Input {
		if part.File == nil {
			continue
		}
		file, err := writeAgentInputFile(ctx, agentInputFileDeps{
			config:          req.Config,
			session:         session,
			workspaceRoot:   session.Summary.WorkspacePath,
			uploadRel:       uploadRel,
			guestDir:        guestDir,
			workspaceShared: workspaceShared,
			index:           index,
			file:            *part.File,
			limits:          limits,
			used:            used,
			writeGuestFile:  writeGuestFile,
		})
		if err != nil {
			return nil, err
		}
		if part.File.Source == agentinput.SourceUpload {
			// Admission bounded staged files by the separate upload budget.
			files = append(files, file)
			continue
		}
		totalBytes += file.SizeBytes
		if limits.MaxTurnBytes > 0 && uint64(totalBytes) > limits.MaxTurnBytes {
			return nil, fmt.Errorf("agent input turn files are %d bytes, limit is %d", totalBytes, limits.MaxTurnBytes)
		}
		files = append(files, file)
	}
	return files, nil
}

type agentInputFileDeps struct {
	config          *appconfig.Config
	session         *domain.Sandbox
	workspaceRoot   string
	uploadRel       string
	guestDir        string
	workspaceShared bool
	index           int
	file            agentinput.File
	limits          agentinput.Limits
	used            map[string]struct{}
	writeGuestFile  GuestFileWriterFunc
}

func writeAgentInputFile(ctx context.Context, deps agentInputFileDeps) (ResolvedFile, error) {
	file := deps.file
	switch file.Source {
	case agentinput.SourceInline:
		name := uniqueAgentInputName(deps.used, deps.index, agentinput.SafeFileName(file.Name, file.MediaType))
		if err := writeAgentInputUpload(deps.workspaceRoot, filepath.Join(deps.uploadRel, name), file.Data); err != nil {
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
		size, err := checkWorkspaceFile(deps.session, cleaned, deps.workspaceShared)
		if err != nil {
			return ResolvedFile{}, fmt.Errorf("agent input file %d: %w", deps.index, err)
		}
		if deps.limits.MaxFileBytes > 0 && uint64(size) > deps.limits.MaxFileBytes {
			return ResolvedFile{}, fmt.Errorf("agent input file %d is %d bytes, limit is %d", deps.index, size, deps.limits.MaxFileBytes)
		}
		if file.SHA256 != "" && deps.workspaceShared {
			if err := verifyWorkspaceFileDigest(deps.workspaceRoot, cleaned, file.SHA256); err != nil {
				return ResolvedFile{}, fmt.Errorf("agent input file %d: %w", deps.index, err)
			}
		}
		return ResolvedFile{
			GuestPath: filepath.Join(deps.config.GuestWorkspacePath, cleaned),
			Name:      filepath.Base(cleaned),
			MediaType: file.MediaType,
			SizeBytes: size,
		}, nil
	case agentinput.SourceUpload:
		if deps.writeGuestFile != nil {
			// The guest writer takes one buffer; staging a large object in
			// daemon memory is what the upload source exists to avoid.
			return ResolvedFile{}, domain.ClassifyError(domain.ErrUnsupported,
				fmt.Sprintf("agent input file %d: upload input is unsupported by this sandbox driver", deps.index), nil)
		}
		name := uniqueAgentInputName(deps.used, deps.index, agentinput.SafeFileName(file.Name, file.MediaType))
		if err := copyAgentInputUpload(deps.workspaceRoot, filepath.Join(deps.uploadRel, name), file.UploadPath, file.UploadSize); err != nil {
			return ResolvedFile{}, fmt.Errorf("copy agent input file %d: %w", deps.index, err)
		}
		return ResolvedFile{GuestPath: filepath.Join(deps.guestDir, name), Name: name, MediaType: file.MediaType, SizeBytes: file.UploadSize}, nil
	default:
		return ResolvedFile{}, fmt.Errorf("agent input file %d has no source", deps.index)
	}
}

// checkWorkspaceFile validates a workspace-relative reference and returns its
// size. When the daemon shares the sandbox filesystem it walks every component
// with lstat, rejecting symlinks and missing files; a symlink could otherwise
// expose daemon-owned state to the agent.
//
// K8s does not share the filesystem: a daemon-local workspace path is not the
// pod's workspace, so the reference is returned unchecked and the harness fails
// if it cannot read it. workspaceShared comes from the caller's driver
// capability, never from whether a local directory happens to exist.
func checkWorkspaceFile(session *domain.Sandbox, relative string, workspaceShared bool) (int64, error) {
	if !workspaceShared {
		return 0, nil
	}
	root := session.Summary.WorkspacePath
	if _, err := os.Stat(root); err != nil {
		return 0, fmt.Errorf("workspace file %q is not readable", relative)
	}
	current := root
	segments := strings.Split(relative, "/")
	for index, segment := range segments {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			return 0, fmt.Errorf("workspace file %q is not readable", relative)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return 0, fmt.Errorf("workspace file %q has a symlinked path component", relative)
		}
		if index < len(segments)-1 && !info.IsDir() {
			return 0, fmt.Errorf("workspace file %q has a non-directory path component", relative)
		}
		if index == len(segments)-1 {
			if !info.Mode().IsRegular() {
				return 0, fmt.Errorf("workspace file %q is not a regular file", relative)
			}
			return info.Size(), nil
		}
	}
	return 0, fmt.Errorf("workspace file %q is not readable", relative)
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

// verifyWorkspaceFileDigest hashes a workspace reference through an os.Root
// anchor, so a component swapped for an escaping symlink after the lstat walk
// still cannot read outside the workspace.
func verifyWorkspaceFileDigest(workspaceRoot, relative, expected string) error {
	root, err := os.OpenRoot(workspaceRoot)
	if err != nil {
		return fmt.Errorf("open sandbox workspace: %w", err)
	}
	// The anchor only scopes the read; the file handle reports its own errors.
	defer func() { _ = root.Close() }()
	handle, err := root.Open(relative)
	if err != nil {
		return fmt.Errorf("workspace file %q is not readable", relative)
	}
	defer func() { _ = handle.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, handle); err != nil {
		return fmt.Errorf("read workspace file %q: %w", relative, err)
	}
	if hex.EncodeToString(digest.Sum(nil)) != expected {
		return fmt.Errorf("workspace file %q sha256 does not match", relative)
	}
	return nil
}

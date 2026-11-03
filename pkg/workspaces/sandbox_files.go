package workspaces

import (
	"context"
	"io"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// SandboxEntryKind is one filesystem object kind below a sandbox workspace
// root. It is a closed set because the RPC that exposes it uses a closed enum.
type SandboxEntryKind string

const (
	SandboxEntryFile      SandboxEntryKind = "file"
	SandboxEntryDirectory SandboxEntryKind = "directory"
	SandboxEntrySymlink   SandboxEntryKind = "symlink"
	SandboxEntryOther     SandboxEntryKind = "other"
)

// SandboxEntry is one entry of a sandbox workspace directory. Path is always
// workspace-relative and slash-separated; it never contains a host path.
type SandboxEntry struct {
	Path    string
	Name    string
	Kind    SandboxEntryKind
	Size    int64
	ModTime time.Time
}

// SandboxFileInfo is the metadata sent before a file's content.
type SandboxFileInfo struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// SandboxFiles reads files below one sandbox's workspace root. It is
// read-only: it never creates, writes, deletes, or starts a runtime. A
// directory listing is one level deep and bounded by offset and limit;
// implementations must not materialize the whole directory to satisfy it.
//
// Authorization, auditing, rate limiting and content policy belong to the
// caller. This is an administrator-level read primitive, not a user-facing
// file service.
type SandboxFiles interface {
	ListSandboxFiles(ctx context.Context, sandbox *domain.Sandbox, path string, offset, limit int) ([]SandboxEntry, int, error)
	OpenSandboxFile(ctx context.Context, sandbox *domain.Sandbox, path string) (io.ReadCloser, SandboxFileInfo, error)
}

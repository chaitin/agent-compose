package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

var errInvalidAgentSkillEntry = errors.New("agent skill entry is not a regular file or directory")

// AgentSkillFileEntry describes the content and executable permissions that a
// private skills projection must preserve. Timestamps and writable bits do not
// identify skill content.
type AgentSkillFileEntry struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Executable uint32 `json:"executable"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
}

// AgentSkillsProjectionEntries inventories skill content for a guest delivery
// comparison. The root manifest is daemon bookkeeping, not skill content.
func AgentSkillsProjectionEntries(ctx context.Context, root string) ([]AgentSkillFileEntry, error) {
	return agentSkillTreeEntries(ctx, root, true)
}

// FingerprintAgentSkill identifies a skill's complete relative tree, including
// empty directories and executable permissions, without depending on mtimes.
func FingerprintAgentSkill(ctx context.Context, root string) (string, error) {
	entries, err := agentSkillTreeEntries(ctx, root, false)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("encode agent skill fingerprint: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func agentSkillTreeEntries(ctx context.Context, root string, projection bool) ([]AgentSkillFileEntry, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("agent skill root %s: %w", root, errInvalidAgentSkillEntry)
	}
	opened, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = opened.Close() }()
	entries := make([]AgentSkillFileEntry, 0)
	err = fs.WalkDir(opened.FS(), ".", func(rel string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if rel == "." && projection {
			return nil
		}
		if projection && rel == agentSkillsManifestFileName {
			return nil
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		entry := AgentSkillFileEntry{Path: filepath.ToSlash(rel), Executable: uint32(info.Mode().Perm() & 0o111)}
		switch {
		case info.IsDir():
			entry.Kind = "directory"
		case info.Mode().IsRegular():
			entry.Kind = "file"
			file, err := opened.Open(rel)
			if err != nil {
				return err
			}
			hash := sha256.New()
			size, copyErr := io.Copy(hash, agentSkillContextReader{ctx: ctx, reader: file})
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			entry.Size = size
			entry.SHA256 = hex.EncodeToString(hash.Sum(nil))
		default:
			return fmt.Errorf("agent skill entry %s: %w", rel, errInvalidAgentSkillEntry)
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("fingerprint agent skill %s: %w", root, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

type agentSkillContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r agentSkillContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

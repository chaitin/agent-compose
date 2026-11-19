package runinputs

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/chaitin/agent-compose/pkg/agentinput"
)

type metadata struct {
	UploadID    string `json:"upload_id"`
	ProjectID   string `json:"project_id"`
	SHA256      string `json:"sha256"`
	SizeBytes   int64  `json:"size_bytes"`
	CreatedAtMS int64  `json:"created_at_ms"`
	ExpiresAtMS int64  `json:"expires_at_ms"`
}

// load rebuilds the index. It runs inside Open before the store is shared, so
// it removes residue directly.
func (s *Store) load() error {
	projects, err := os.ReadDir(s.opts.Root)
	if err != nil {
		return err
	}
	now := s.opts.Now()
	for _, projectEntry := range projects {
		if !projectEntry.IsDir() {
			continue
		}
		dir := filepath.Join(s.opts.Root, projectEntry.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		known := map[string]bool{}
		for _, item := range entries {
			name := item.Name()
			if !strings.HasSuffix(name, metadataSuffix) || strings.HasPrefix(name, tempPrefix) {
				continue
			}
			loaded, ok := s.loadMetadata(dir, projectEntry.Name(), name)
			if !ok {
				continue
			}
			known[loaded.UploadID] = true
			if !now.Before(loaded.ExpiresAt) {
				s.removeAll(objectFiles(loaded.Path))
				continue
			}
			s.index(loaded)
		}
		for _, item := range entries {
			name := item.Name()
			if known[name] || known[strings.TrimSuffix(name, metadataSuffix)] {
				continue
			}
			// A temp file or content without metadata cannot belong to a
			// live upload before the daemon serves.
			s.removePath(filepath.Join(dir, name))
		}
	}
	return nil
}

func (s *Store) loadMetadata(dir, projectID, name string) (*entry, bool) {
	path := filepath.Join(dir, name)
	raw, err := os.ReadFile(path)
	var meta metadata
	if err == nil {
		err = json.Unmarshal(raw, &meta)
	}
	contentPath := filepath.Join(dir, strings.TrimSuffix(name, metadataSuffix))
	if err != nil || meta.UploadID+metadataSuffix != name || meta.ProjectID != projectID {
		s.opts.Logger.Warn("discarding unreadable run input upload metadata", "path", path, "error", err)
		s.removeAll([]string{path, contentPath})
		return nil, false
	}
	info, err := os.Stat(contentPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() != meta.SizeBytes {
		s.opts.Logger.Warn("discarding run input upload with missing or truncated content", "upload_id", meta.UploadID)
		s.removeAll([]string{path, contentPath})
		return nil, false
	}
	expires := time.UnixMilli(meta.ExpiresAtMS)
	return &entry{
		Object: Object{
			UploadID:  meta.UploadID,
			ProjectID: meta.ProjectID,
			SHA256:    meta.SHA256,
			SizeBytes: meta.SizeBytes,
			CreatedAt: time.UnixMilli(meta.CreatedAtMS),
			ExpiresAt: expires,
			Path:      contentPath,
		},
		persisted: expires,
	}, true
}

// removeResidue removes files that belong to no live object and are older
// than staleResidueAge. live is a snapshot of the index; an object committed
// after it has fresh files, which the age check keeps.
func (s *Store) removeResidue(live map[string]struct{}) {
	projects, err := os.ReadDir(s.opts.Root)
	if err != nil {
		s.opts.Logger.Warn("cannot scan run input staging root", "error", err)
		return
	}
	now := s.opts.Now()
	for _, projectEntry := range projects {
		if !projectEntry.IsDir() {
			continue
		}
		dir := filepath.Join(s.opts.Root, projectEntry.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, item := range entries {
			name := item.Name()
			if _, ok := live[strings.TrimSuffix(name, metadataSuffix)]; ok {
				continue
			}
			info, err := item.Info()
			if err != nil || now.Sub(info.ModTime()) < staleResidueAge {
				continue
			}
			s.removePath(filepath.Join(dir, name))
		}
		// A project directory empties once its last object is collected. Remove
		// it only after it has aged, so an upload that is between MkdirAll and
		// its first write is never pulled out from under itself. Best effort:
		// the directory is non-empty whenever an upload is in flight.
		if info, err := os.Stat(dir); err == nil && now.Sub(info.ModTime()) >= staleResidueAge {
			_ = os.Remove(dir)
		}
	}
}

func writeMetadata(object Object) error {
	raw, err := json.Marshal(metadata{
		UploadID:    object.UploadID,
		ProjectID:   object.ProjectID,
		SHA256:      object.SHA256,
		SizeBytes:   object.SizeBytes,
		CreatedAtMS: object.CreatedAt.UnixMilli(),
		ExpiresAtMS: object.ExpiresAt.UnixMilli(),
	})
	if err != nil {
		return err
	}
	path := object.Path + metadataSuffix
	temp := filepath.Join(filepath.Dir(path), tempPrefix+uuid.NewString())
	if err := os.WriteFile(temp, raw, 0o600); err != nil {
		_ = os.Remove(temp)
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}

func (s *Store) removeAll(paths []string) {
	for _, path := range paths {
		s.removePath(path)
	}
}

func (s *Store) removePath(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.opts.Logger.Warn("cannot remove run input staging file", "path", path, "error", err)
	}
}

func validSegment(value string) bool {
	if value == "" || len(value) > 128 || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return !strings.HasPrefix(value, ".")
}

func isSHA256(value string) bool {
	return agentinput.IsSHA256(value)
}

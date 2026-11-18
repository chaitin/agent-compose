package runinputs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// Upload is one upload in progress. It is not safe for concurrent use.
type Upload struct {
	store     *Store
	projectID string
	size      int64
	digest    string
	file      *os.File
	hash      hash.Hash
	written   int64
	done      bool
}

func (s *Store) createUpload(projectID string, size int64, digest string) (*Upload, error) {
	dir := filepath.Join(s.opts.Root, projectID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create run input staging directory: %w", err)
	}
	temp, err := os.OpenFile(filepath.Join(dir, tempPrefix+uuid.NewString()), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create run input upload: %w", err)
	}
	return &Upload{store: s, projectID: projectID, size: size, digest: digest, file: temp, hash: sha256.New()}, nil
}

// Write appends a chunk. Bytes beyond the declared size are rejected.
func (u *Upload) Write(chunk []byte) error {
	if u.done {
		return fmt.Errorf("%w: upload is finished", ErrInvalid)
	}
	if u.written+int64(len(chunk)) > u.size {
		return fmt.Errorf("%w: upload exceeds its declared %d bytes", ErrInvalid, u.size)
	}
	if _, err := u.file.Write(chunk); err != nil {
		return fmt.Errorf("write run input upload: %w", err)
	}
	u.hash.Write(chunk)
	u.written += int64(len(chunk))
	return nil
}

// Commit verifies the content and publishes it. When another upload of the
// same content committed first, that object is returned and this copy is
// discarded. Commit always finishes the upload; Abort afterwards is a no-op.
//
// The content and metadata are published before the object enters the index,
// so no lock is held for the rename or the metadata write. A crash between the
// two leaves files that Open removes.
func (u *Upload) Commit() (Object, bool, error) {
	if u.done {
		return Object{}, false, fmt.Errorf("%w: upload is finished", ErrInvalid)
	}
	defer u.Abort()
	if u.written != u.size {
		return Object{}, false, fmt.Errorf("%w: received %d of %d declared bytes", ErrInvalid, u.written, u.size)
	}
	if hex.EncodeToString(u.hash.Sum(nil)) != u.digest {
		return Object{}, false, fmt.Errorf("%w: sha256 does not match the uploaded bytes", ErrInvalid)
	}
	if err := u.file.Sync(); err != nil {
		return Object{}, false, fmt.Errorf("sync run input upload: %w", err)
	}
	if err := u.file.Chmod(0o444); err != nil {
		return Object{}, false, fmt.Errorf("seal run input upload: %w", err)
	}
	s := u.store
	now := s.opts.Now()
	object := Object{
		UploadID:  uuid.NewString(),
		ProjectID: u.projectID,
		SHA256:    u.digest,
		SizeBytes: u.size,
		CreatedAt: now,
		ExpiresAt: now.Add(s.opts.TTL),
	}
	object.Path = filepath.Join(s.opts.Root, u.projectID, object.UploadID)
	if err := os.Rename(u.file.Name(), object.Path); err != nil {
		return Object{}, false, fmt.Errorf("publish run input upload: %w", err)
	}
	if err := writeMetadata(object); err != nil {
		s.removePath(object.Path)
		return Object{}, false, fmt.Errorf("publish run input upload: %w", err)
	}
	// The deferred Abort releases the reservation; commit moves the bytes
	// into usage first, so the accounting never dips below the stored total.
	indexed, deduplicated := s.commit(object)
	if deduplicated {
		s.removeAll(objectFiles(object.Path))
	}
	return indexed, deduplicated, nil
}

// Abort discards an unfinished upload and releases its reservation. It is
// safe to call more than once and after Commit.
func (u *Upload) Abort() {
	if u.done {
		return
	}
	u.done = true
	name := u.file.Name()
	_ = u.file.Close()
	u.store.removePath(name)
	u.store.release(u.projectID, u.size)
}

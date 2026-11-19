// Package runinputs owns the staging area for files uploaded before a run
// references them (UploadRunInput → AgentInputFile.upload).
//
// An object is content only: the file name is chosen by the run that
// references it. Objects are content-addressed within a project, so the same
// bytes are stored once, and they expire unless a run references them. The
// execution layer copies an object into the sandbox workspace; the agent never
// sees or changes the staged file.
//
// Layout, under Root:
//
//	<project-id>/<upload-id>         content, read-only
//	<project-id>/<upload-id>.json    metadata, written after the content
//	<project-id>/.upload-<uuid>      an upload in progress
//
// The daemon is the only writer of Root, so one in-process index is the source
// of truth while it runs; the metadata files rebuild that index on start.
//
// Locking: the mutex guards only the in-memory index and byte accounting. All
// file work (create, rename, metadata writes, removal, statfs, directory scans)
// runs outside it. An object leaves the index before its files are removed, so
// a crash in between leaves residue that Open and Collect remove.
package runinputs

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	// ErrInvalid reports a request the caller must correct.
	ErrInvalid = errors.New("invalid run input upload")
	// ErrNotFound reports an unknown, expired, or other-project upload.
	ErrNotFound = errors.New("run input upload not found")
	// ErrExhausted reports that the staging area cannot accept more bytes.
	ErrExhausted = errors.New("run input staging area is full")
)

const (
	metadataSuffix = ".json"
	tempPrefix     = ".upload-"
	// staleResidueAge bounds how long an abandoned temp file or a content
	// file without metadata may stay. Both are only possible after a crash
	// or a failed write; a fresh one may belong to an upload in progress.
	staleResidueAge = time.Hour
	// projectShareDivisor caps one project at a fraction of the store so a
	// single project cannot exhaust the shared budget.
	projectShareDivisor = 4
	// minFreeRatio rejects uploads that would leave the filesystem with less
	// free space than this fraction of its size.
	minFreeRatio = 0.10
)

// Options configures a Store.
type Options struct {
	Root           string
	MaxObjectBytes int64
	MaxStoreBytes  int64
	TTL            time.Duration
	Logger         *slog.Logger
	// Now and FreeSpace are test seams; nil uses the real clock and statfs.
	Now       func() time.Time
	FreeSpace func(path string) (free, total uint64, err error)
}

// Object is one staged upload.
type Object struct {
	UploadID  string
	ProjectID string
	SHA256    string
	SizeBytes int64
	CreatedAt time.Time
	ExpiresAt time.Time
	// Path is the absolute content path. It is valid until the object
	// expires; a reader that loses the race with expiry gets ErrNotExist.
	Path string
}

// entry is the index record of one object. persisted is the expiry the
// metadata file holds; writing marks the single goroutine allowed to rewrite
// that file, so concurrent extensions never land on disk out of order.
type entry struct {
	Object
	persisted time.Time
	writing   bool
	dropped   bool
}

// Store is the staging area. It is safe for concurrent use.
type Store struct {
	opts Options

	mu       sync.Mutex
	objects  map[string]*entry // by upload id
	byDigest map[digestKey]string
	used     int64
	reserved int64
	project  map[string]int64 // used + reserved per project
}

type digestKey struct {
	project string
	sha256  string
}

// Open creates the root if needed, rebuilds the index from metadata, and
// removes residue: no upload can be in flight before the daemon serves.
func Open(opts Options) (*Store, error) {
	if strings.TrimSpace(opts.Root) == "" {
		return nil, fmt.Errorf("run input staging root is required")
	}
	if opts.MaxObjectBytes <= 0 || opts.MaxStoreBytes <= 0 || opts.TTL <= 0 {
		return nil, fmt.Errorf("run input staging limits must be positive")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.FreeSpace == nil {
		opts.FreeSpace = freeSpace
	}
	if err := os.MkdirAll(opts.Root, 0o700); err != nil {
		return nil, fmt.Errorf("create run input staging root: %w", err)
	}
	store := &Store{
		opts:     opts,
		objects:  map[string]*entry{},
		byDigest: map[digestKey]string{},
		project:  map[string]int64{},
	}
	if err := store.load(); err != nil {
		return nil, fmt.Errorf("load run input staging area: %w", err)
	}
	return store, nil
}

// MaxObjectBytes reports the per-object limit.
func (s *Store) MaxObjectBytes() int64 { return s.opts.MaxObjectBytes }

// Begin starts an upload. When the project already holds the same content it
// returns that object instead, with its expiry extended, and no Upload.
//
// Begin only checks that the declared size could still fit. The bytes are
// charged as they arrive (see reserve), so an upload that never sends content
// cannot pin capacity it does not use.
func (s *Store) Begin(projectID string, size int64, digest string) (*Upload, *Object, error) {
	if !validSegment(projectID) {
		return nil, nil, fmt.Errorf("%w: project id %q is not a valid path segment", ErrInvalid, projectID)
	}
	if size <= 0 {
		return nil, nil, fmt.Errorf("%w: size_bytes must be greater than zero", ErrInvalid)
	}
	if size > s.opts.MaxObjectBytes {
		return nil, nil, fmt.Errorf("%w: upload is %d bytes, limit is %d", ErrInvalid, size, s.opts.MaxObjectBytes)
	}
	if !isSHA256(digest) {
		return nil, nil, fmt.Errorf("%w: sha256 must be 64 lowercase hex characters", ErrInvalid)
	}
	// statfs is file-system work, so it is read before taking the lock.
	free, total, spaceErr := s.opts.FreeSpace(s.opts.Root)

	s.mu.Lock()
	garbage := s.dropExpiredLocked()
	if existing := s.lookupDigestLocked(projectID, digest); existing != nil {
		if existing.SizeBytes != size {
			s.mu.Unlock()
			s.removeAll(garbage)
			return nil, nil, fmt.Errorf("%w: sha256 matches an upload of %d bytes, not %d", ErrInvalid, existing.SizeBytes, size)
		}
		s.extendLocked(existing)
		snapshot := existing.Object
		s.mu.Unlock()
		s.removeAll(garbage)
		s.persist(existing)
		return nil, &snapshot, nil
	}
	if err := s.admitLocked(projectID, size, free, total, spaceErr); err != nil {
		s.mu.Unlock()
		s.removeAll(garbage)
		return nil, nil, err
	}
	s.mu.Unlock()
	s.removeAll(garbage)

	upload, err := s.createUpload(projectID, size, digest)
	if err != nil {
		return nil, nil, err
	}
	return upload, nil, nil
}

// admitLocked rejects an upload whose declared size provably cannot fit. It
// does not reserve the bytes: the caller charges them as they arrive, so the
// check is advisory under concurrency and the per-chunk reserve is binding.
func (s *Store) admitLocked(projectID string, size int64, free, total uint64, spaceErr error) error {
	if s.used+s.reserved+size > s.opts.MaxStoreBytes {
		return fmt.Errorf("%w: staging %d more bytes would exceed %d", ErrExhausted, size, s.opts.MaxStoreBytes)
	}
	if limit := s.opts.MaxStoreBytes / projectShareDivisor; s.project[projectID]+size > limit {
		return fmt.Errorf("%w: project would exceed its %d byte share", ErrExhausted, limit)
	}
	if spaceErr != nil {
		// The byte budgets still bound the store; a filesystem that cannot
		// report its space must not block every upload.
		s.opts.Logger.Warn("cannot read run input staging filesystem space", "error", spaceErr)
		return nil
	}
	if float64(free)-float64(size) < float64(total)*minFreeRatio {
		return fmt.Errorf("%w: filesystem free space is below %.0f%%", ErrExhausted, minFreeRatio*100)
	}
	return nil
}

// Resolve returns a live object of the project and extends its expiry, so a
// run that was just admitted keeps it until the copy into the workspace.
func (s *Store) Resolve(projectID, uploadID string) (Object, error) {
	s.mu.Lock()
	found := s.objects[strings.TrimSpace(uploadID)]
	if found == nil || found.ProjectID != projectID {
		s.mu.Unlock()
		return Object{}, fmt.Errorf("%w: upload %q does not exist in this project", ErrNotFound, uploadID)
	}
	if !s.opts.Now().Before(found.ExpiresAt) {
		garbage := s.dropLocked(found)
		s.mu.Unlock()
		s.removeAll(garbage)
		return Object{}, fmt.Errorf("%w: upload %q has expired", ErrNotFound, uploadID)
	}
	s.extendLocked(found)
	snapshot := found.Object
	s.mu.Unlock()
	s.persist(found)
	return snapshot, nil
}

// Collect removes expired objects and stale residue. It returns the number of
// objects removed.
func (s *Store) Collect() int {
	s.mu.Lock()
	garbage := s.dropExpiredLocked()
	live := make(map[string]struct{}, len(s.objects))
	for id := range s.objects {
		live[id] = struct{}{}
	}
	s.mu.Unlock()
	s.removeAll(garbage)
	s.removeResidue(live)
	return len(garbage) / filesPerObject
}

// commit indexes a published object. When the same content was indexed first
// it returns that object instead and the caller discards its copy. The upload
// charged its bytes as they arrived, so publishing moves that reservation into
// usage and a duplicate releases it.
func (s *Store) commit(object Object) (Object, bool) {
	s.mu.Lock()
	if existing := s.lookupDigestLocked(object.ProjectID, object.SHA256); existing != nil {
		s.extendLocked(existing)
		snapshot := existing.Object
		s.reserved -= object.SizeBytes
		s.releaseProjectLocked(object.ProjectID, object.SizeBytes)
		s.mu.Unlock()
		s.persist(existing)
		return snapshot, true
	}
	s.objects[object.UploadID] = &entry{Object: object, persisted: object.ExpiresAt}
	s.byDigest[digestKey{project: object.ProjectID, sha256: object.SHA256}] = object.UploadID
	s.reserved -= object.SizeBytes
	s.used += object.SizeBytes
	s.mu.Unlock()
	return object, false
}

// reserve charges bytes that have arrived to an upload in progress. Begin only
// checks the declared size, so capacity tracks what is actually stored rather
// than what a client promised. The free-space guard is repeated here because
// concurrent uploads can all pass the Begin check before any of them writes.
func (s *Store) reserve(projectID string, size int64) error {
	if size <= 0 {
		return nil
	}
	// statfs is file-system work, so it runs before taking the lock.
	free, total, spaceErr := s.opts.FreeSpace(s.opts.Root)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used+s.reserved+size > s.opts.MaxStoreBytes {
		return fmt.Errorf("%w: staging %d more bytes would exceed %d", ErrExhausted, size, s.opts.MaxStoreBytes)
	}
	if limit := s.opts.MaxStoreBytes / projectShareDivisor; s.project[projectID]+size > limit {
		return fmt.Errorf("%w: project would exceed its %d byte share", ErrExhausted, limit)
	}
	if spaceErr == nil && float64(free)-float64(size) < float64(total)*minFreeRatio {
		return fmt.Errorf("%w: filesystem free space is below %.0f%%", ErrExhausted, minFreeRatio*100)
	}
	s.reserved += size
	s.project[projectID] += size
	return nil
}

// release returns a reservation taken by Begin.
func (s *Store) release(projectID string, size int64) {
	s.mu.Lock()
	s.reserved -= size
	s.releaseProjectLocked(projectID, size)
	s.mu.Unlock()
}

// persist writes the entry's in-memory expiry to its metadata file without
// holding the lock. Only one goroutine writes an entry at a time; it loops
// until the file holds the latest expiry, so an extension that arrives while a
// write is in flight is never lost or overwritten by an older one. A failed
// write keeps the extension in memory and only weakens it across a restart.
func (s *Store) persist(target *entry) {
	s.mu.Lock()
	if target.writing {
		s.mu.Unlock()
		return
	}
	target.writing = true
	for !target.dropped && target.ExpiresAt.After(target.persisted) {
		snapshot := target.Object
		s.mu.Unlock()
		err := writeMetadata(snapshot)
		s.mu.Lock()
		if err != nil {
			s.opts.Logger.Warn("cannot persist run input upload expiry", "upload_id", snapshot.UploadID, "error", err)
			break
		}
		target.persisted = snapshot.ExpiresAt
	}
	target.writing = false
	dropped := target.dropped
	path := target.Path
	s.mu.Unlock()
	if dropped {
		// The object expired while its metadata was being rewritten; the
		// rewrite may have recreated the file after the drop removed it.
		s.removePath(path + metadataSuffix)
	}
}

func (s *Store) dropExpiredLocked() []string {
	now := s.opts.Now()
	var garbage []string
	for _, candidate := range s.objects {
		if !now.Before(candidate.ExpiresAt) {
			garbage = append(garbage, s.dropLocked(candidate)...)
		}
	}
	return garbage
}

// lookupDigestLocked returns the live object with this content, or nil.
func (s *Store) lookupDigestLocked(projectID, digest string) *entry {
	id, ok := s.byDigest[digestKey{project: projectID, sha256: digest}]
	if !ok {
		return nil
	}
	found := s.objects[id]
	if found == nil || !s.opts.Now().Before(found.ExpiresAt) {
		return nil
	}
	return found
}

func (s *Store) extendLocked(target *entry) {
	if next := s.opts.Now().Add(s.opts.TTL); next.After(target.ExpiresAt) {
		target.ExpiresAt = next
	}
}

func (s *Store) index(target *entry) {
	s.objects[target.UploadID] = target
	s.byDigest[digestKey{project: target.ProjectID, sha256: target.SHA256}] = target.UploadID
	s.used += target.SizeBytes
	s.project[target.ProjectID] += target.SizeBytes
}

// dropLocked removes an entry from the index and returns the files to remove
// once the lock is released. Metadata comes first: content without metadata
// is residue the next scan removes, while metadata without content would be
// discarded on load.
func (s *Store) dropLocked(target *entry) []string {
	delete(s.objects, target.UploadID)
	key := digestKey{project: target.ProjectID, sha256: target.SHA256}
	if s.byDigest[key] == target.UploadID {
		delete(s.byDigest, key)
	}
	s.used -= target.SizeBytes
	s.releaseProjectLocked(target.ProjectID, target.SizeBytes)
	target.dropped = true
	return objectFiles(target.Path)
}

// filesPerObject is the length of objectFiles: metadata, then content.
const filesPerObject = 2

func objectFiles(contentPath string) []string {
	return []string{contentPath + metadataSuffix, contentPath}
}

func (s *Store) releaseProjectLocked(projectID string, size int64) {
	s.project[projectID] -= size
	if s.project[projectID] <= 0 {
		delete(s.project, projectID)
	}
}

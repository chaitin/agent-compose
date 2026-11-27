package runlog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/klauspost/compress/zstd"
)

// ArchivedFileName is the name of a run log once archived: the log
// compressed as one standard zstd stream, readable with `zstd -d`.
const ArchivedFileName = FileName + ".zst"

// ArchiveMarkerName is left in a run's directory once its log is archived. It
// records where the log went, so the run directory stays the place that
// answers "where is this run's history", and a reader can tell an archived
// log from one that was never written.
const ArchiveMarkerName = "archived.json"

// ErrArchived reports that a run log was moved to the archive and is no
// longer readable in place.
var ErrArchived = errors.New("run log is archived")

// ArchiveMarker describes an archived run log.
type ArchiveMarker struct {
	Version    int       `json:"version"`
	Path       string    `json:"path"`
	ArchivedAt time.Time `json:"archived_at"`
	// LogBytes is the size of the log that was archived, uncompressed.
	LogBytes int64 `json:"log_bytes"`
	// SHA256 is the digest of the archive file.
	SHA256 string `json:"sha256"`
}

const archiveMarkerVersion = 1

// ReadArchiveMarker reads the archive marker in a run's directory, reporting
// false when the run log has not been archived.
func ReadArchiveMarker(dir string) (ArchiveMarker, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, ArchiveMarkerName))
	if errors.Is(err, os.ErrNotExist) {
		return ArchiveMarker{}, false, nil
	}
	if err != nil {
		return ArchiveMarker{}, false, fmt.Errorf("read run log archive marker: %w", err)
	}
	var marker ArchiveMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return ArchiveMarker{}, false, fmt.Errorf("decode run log archive marker: %w", err)
	}
	if marker.Version != archiveMarkerVersion || marker.Path == "" || marker.SHA256 == "" {
		return ArchiveMarker{}, false, errors.New("invalid run log archive marker")
	}
	return marker, true, nil
}

// openLog opens the run log at path for reading. A log that was archived
// fails with ErrArchived rather than os.ErrNotExist, so callers that treat a
// missing log as "nothing written yet" do not mistake it for an empty run.
func openLog(path string) (*os.File, error) {
	file, err := os.Open(path)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return file, err
	}
	if archived := archivedError(path); archived != nil {
		return nil, archived
	}
	return nil, err
}

func archivedError(path string) error {
	marker, ok, err := ReadArchiveMarker(filepath.Dir(path))
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return fmt.Errorf("%w: moved to %s at %s", ErrArchived, marker.Path, marker.ArchivedAt.UTC().Format(time.RFC3339))
}

// ArchiveLog moves the finished run log at path to dst, compressed, and
// leaves an archive marker in its place. The log must no longer be written:
// only its committed prefix is archived.
//
// Each step is durable before the next one starts, and the original is
// removed only after the archive is verified and the marker committed, so an
// interrupted call is retried by calling it again. A call on a log that is
// already archived finishes any cleanup a previous call left and returns its
// marker.
func ArchiveLog(path, dst string, now time.Time) (ArchiveMarker, error) {
	dir := filepath.Dir(path)
	marker, ok, err := ReadArchiveMarker(dir)
	if err != nil {
		return ArchiveMarker{}, err
	}
	if ok {
		return marker, finishArchive(path, marker)
	}
	source, err := os.Open(path)
	if err != nil {
		return ArchiveMarker{}, err
	}
	defer func() { _ = source.Close() }()
	logBytes, err := committedLogBytes(source)
	if err != nil {
		return ArchiveMarker{}, err
	}
	sourceDigest, archiveDigest, err := writeArchive(io.NewSectionReader(source, 0, logBytes), dst)
	if err != nil {
		return ArchiveMarker{}, err
	}
	if err := verifyArchive(dst, sourceDigest); err != nil {
		return ArchiveMarker{}, err
	}
	marker = ArchiveMarker{
		Version: archiveMarkerVersion, Path: dst, ArchivedAt: now.UTC(),
		LogBytes: logBytes, SHA256: archiveDigest,
	}
	if err := writeArchiveMarker(dir, marker); err != nil {
		return ArchiveMarker{}, err
	}
	return marker, finishArchive(path, marker)
}

// writeArchive compresses log into dst through a temporary file renamed into
// place once synced, returning the digests of the log and of the archive.
func writeArchive(log io.Reader, dst string) (sourceDigest, archiveDigest string, err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", "", fmt.Errorf("create run log archive dir: %w", err)
	}
	tmp := dst + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", "", fmt.Errorf("create run log archive: %w", err)
	}
	defer func() {
		if err != nil {
			_ = file.Close()
			_ = os.Remove(tmp)
		}
	}()
	sourceHash, archiveHash := sha256.New(), sha256.New()
	encoder, err := zstd.NewWriter(io.MultiWriter(file, archiveHash), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return "", "", err
	}
	if _, err = io.Copy(encoder, io.TeeReader(log, sourceHash)); err != nil {
		_ = encoder.Close()
		return "", "", fmt.Errorf("compress run log: %w", err)
	}
	if err = encoder.Close(); err != nil {
		return "", "", fmt.Errorf("compress run log: %w", err)
	}
	if err = file.Sync(); err != nil {
		return "", "", fmt.Errorf("sync run log archive: %w", err)
	}
	if err = file.Close(); err != nil {
		return "", "", fmt.Errorf("close run log archive: %w", err)
	}
	if err = os.Rename(tmp, dst); err != nil {
		return "", "", fmt.Errorf("commit run log archive: %w", err)
	}
	if err = syncDir(filepath.Dir(dst)); err != nil {
		return "", "", err
	}
	return hex.EncodeToString(sourceHash.Sum(nil)), hex.EncodeToString(archiveHash.Sum(nil)), nil
}

// verifyArchive decompresses the archive at dst and checks it reproduces the
// log whose digest is want.
func verifyArchive(dst, want string) error {
	file, err := os.Open(dst)
	if err != nil {
		return fmt.Errorf("open run log archive: %w", err)
	}
	defer func() { _ = file.Close() }()
	decoder, err := zstd.NewReader(file, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return fmt.Errorf("open run log archive: %w", err)
	}
	defer decoder.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, decoder); err != nil {
		return fmt.Errorf("verify run log archive: %w", err)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		return fmt.Errorf("verify run log archive: content digest %s, want %s", got, want)
	}
	return nil
}

func writeArchiveMarker(dir string, marker ArchiveMarker) error {
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, ArchiveMarkerName)
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("write run log archive marker: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return fmt.Errorf("write run log archive marker: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync run log archive marker: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close run log archive marker: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("commit run log archive marker: %w", err)
	}
	return syncDir(dir)
}

// finishArchive removes the original log once the archive the marker names
// is confirmed intact.
func finishArchive(path string, marker ArchiveMarker) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	digest, err := fileDigest(marker.Path)
	if err != nil {
		return fmt.Errorf("verify run log archive before removing the original: %w", err)
	}
	if digest != marker.SHA256 {
		return fmt.Errorf("run log archive %s does not match its marker; the original is kept", marker.Path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove archived run log: %w", err)
	}
	return syncDir(filepath.Dir(path))
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// ReadArchivedLog decompresses an archived run log, for tools and tests that
// need its content back.
func ReadArchivedLog(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	decoder, err := zstd.NewReader(file, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	var out bytes.Buffer
	if _, err := io.Copy(&out, decoder); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	defer func() { _ = handle.Close() }()
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	return nil
}

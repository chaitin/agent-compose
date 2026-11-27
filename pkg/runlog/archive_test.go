package runlog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeArchivableLog(t *testing.T) (string, []byte) {
	t.Helper()
	w, path := openTest(t, Options{})
	if _, err := w.AppendInitialPrompt("fix the build"); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendOutput("stdout", strings.Repeat("compiling\n", 2000)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, original
}

func TestArchiveLogMovesTheLogAndLeavesAMarker(t *testing.T) {
	path, original := writeArchivableLog(t)
	dst := filepath.Join(t.TempDir(), "archives", "runs", "run-1", ArchivedFileName)
	at := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)

	marker, err := ArchiveLog(path, dst, at)
	if err != nil {
		t.Fatalf("ArchiveLog: %v", err)
	}
	if marker.Path != dst || !marker.ArchivedAt.Equal(at) || marker.LogBytes != int64(len(original)) {
		t.Fatalf("marker = %+v", marker)
	}
	restored, err := ReadArchivedLog(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Fatal("archive does not reproduce the log")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original log remains: %v", err)
	}
	if _, err := os.Stat(dst + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary archive remains: %v", err)
	}
	read, ok, err := ReadArchiveMarker(filepath.Dir(path))
	if err != nil || !ok || read != marker {
		t.Fatalf("ReadArchiveMarker = %+v, %v, %v", read, ok, err)
	}
}

func TestArchivedLogReadsFailWithErrArchived(t *testing.T) {
	path, _ := writeArchivableLog(t)
	if _, err := ArchiveLog(path, filepath.Join(t.TempDir(), ArchivedFileName), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEntries(path, 0); !errors.Is(err, ErrArchived) || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadEntries err = %v, want ErrArchived and not ErrNotExist", err)
	}
	if _, _, err := ReadEntryBatch(path, 0, 0, 10); !errors.Is(err, ErrArchived) {
		t.Fatalf("ReadEntryBatch err = %v", err)
	}
	if _, err := OpenTextView(path); !errors.Is(err, ErrArchived) {
		t.Fatalf("OpenTextView err = %v", err)
	}
	if _, err := SummarizeLog(path); !errors.Is(err, ErrArchived) {
		t.Fatalf("SummarizeLog err = %v", err)
	}
	// Reopening must not start a fresh log under the same run.
	if _, err := Open(path, Options{}); !errors.Is(err, ErrArchived) {
		t.Fatalf("Open err = %v, want ErrArchived", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open recreated the log: %v", err)
	}
}

func TestMissingLogThatWasNeverArchivedStillReportsNotExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs", "run-1", FileName)
	if _, err := ReadEntries(path, 0); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadEntries err = %v, want ErrNotExist", err)
	}
}

func TestArchiveLogFinishesAnInterruptedArchive(t *testing.T) {
	path, original := writeArchivableLog(t)
	dst := filepath.Join(t.TempDir(), ArchivedFileName)
	marker, err := ArchiveLog(path, dst, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the marker was committed but before the
	// original was removed.
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := ArchiveLog(path, dst, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("resumed ArchiveLog: %v", err)
	}
	if again != marker {
		t.Fatalf("resumed marker = %+v, want the committed %+v", again, marker)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original log remains after resume: %v", err)
	}
}

func TestArchiveLogKeepsTheOriginalWhenTheArchiveDoesNotMatch(t *testing.T) {
	path, original := writeArchivableLog(t)
	dst := filepath.Join(t.TempDir(), ArchivedFileName)
	if _, err := ArchiveLog(path, dst, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("corrupted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ArchiveLog(path, dst, time.Now()); err == nil {
		t.Fatal("ArchiveLog succeeded against a corrupted archive")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("original log was removed: %v", err)
	}
}

func TestArchiveLogArchivesOnlyTheCommittedPrefix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	w, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.AppendInitialPrompt("go"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit(0, nil); err != nil {
		t.Fatal(err)
	}
	w.SetSourceOffset(10)
	if err := w.AppendOutput("stdout", "committed\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit(10, nil); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The daemon died after appending a record it had not committed.
	w.SetSourceOffset(20)
	if err := w.AppendOutput("stdout", "uncommitted\n"); err != nil {
		t.Fatal(err)
	}
	crash(w)
	dst := filepath.Join(t.TempDir(), ArchivedFileName)
	if _, err := ArchiveLog(path, dst, time.Now()); err != nil {
		t.Fatal(err)
	}
	restored, err := ReadArchivedLog(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Fatalf("archived %d bytes, want the %d committed", len(restored), len(original))
	}
}

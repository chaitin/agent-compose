package driver

import (
	"reflect"
	"testing"
)

// BEHAVIOR CHANGE under test: the exec output filter used to DROP the
// libcontainer seccomp/no_new_privileges failure lines. It must now preserve
// every line and report the isolation facts. These tests used to assert the
// drop; they assert the report.

const testSeccompWarning = "\x1b[2m2026-05-05T15:49:43.862984Z\x1b[0m \x1b[33m WARN\x1b[0m \x1b[2mlibcontainer::process::init::process\x1b[0m\x1b[2m:\x1b[0m seccomp not available, unable to set seccomp privileges!\n"

func TestExecOutputFilterReportsKnownSeccompWarning(t *testing.T) {
	filter := newExecOutputFilter()
	chunks := collectFilteredChunks(filter,
		ExecChunk{Text: testSeccompWarning, Stream: StdioStderr},
		ExecChunk{Text: "ok\n", Stream: StdioStdout},
	)

	want := []ExecChunk{
		{Text: testSeccompWarning, Stream: StdioStderr},
		{Text: "ok\n", Stream: StdioStdout},
	}
	if !reflect.DeepEqual(chunks, want) {
		t.Fatalf("the seccomp warning must reach the user: got %#v want %#v", chunks, want)
	}
	if facts := filter.SecurityFacts(); facts.SeccompUnavailable != 1 || facts.NoNewPrivilegesUnavailable != 0 {
		t.Fatalf("SecurityFacts() = %+v, want one seccomp report", facts)
	}

	// A warning that appears on stdout is not an engine-observed sandbox
	// report; it stays visible but is never counted.
	stdoutFilter := newExecOutputFilter()
	stdoutChunks := collectFilteredChunks(stdoutFilter, ExecChunk{Text: testSeccompWarning})
	if wantStdout := []ExecChunk{{Text: testSeccompWarning}}; !reflect.DeepEqual(stdoutChunks, wantStdout) {
		t.Fatalf("stdout warning text should pass through unchanged: got %#v want %#v", stdoutChunks, wantStdout)
	}
	if facts := stdoutFilter.SecurityFacts(); !facts.IsZero() {
		t.Fatalf("SecurityFacts() = %+v, want no facts from stdout", facts)
	}
}

func TestExecOutputFilterHandlesSplitWarning(t *testing.T) {
	filter := newExecOutputFilter()
	chunks := collectFilteredChunks(filter,
		ExecChunk{Text: "\x1b[2m2026-05-05T15:49:43.862984Z\x1b[0m \x1b[33m WARN\x1b[0m \x1b[2mlibcontainer::process::init::process", Stream: StdioStderr},
		ExecChunk{Text: "\x1b[0m\x1b[2m:\x1b[0m seccomp not available, unable to set seccomp privileges!\n", Stream: StdioStderr},
		ExecChunk{Text: "done\n", Stream: StdioStdout},
	)

	want := []ExecChunk{
		{Text: testSeccompWarning, Stream: StdioStderr},
		{Text: "done\n", Stream: StdioStdout},
	}
	if !reflect.DeepEqual(chunks, want) {
		t.Fatalf("a warning split across chunks must be reassembled and reported: got %#v want %#v", chunks, want)
	}
	if facts := filter.SecurityFacts(); facts.SeccompUnavailable != 1 {
		t.Fatalf("SecurityFacts() = %+v, want exactly one seccomp report for a split warning", facts)
	}
}

func TestExecOutputFilterPreservesRealStderr(t *testing.T) {
	filter := newExecOutputFilter()
	chunks := collectFilteredChunks(filter,
		ExecChunk{Text: "real error", Stream: StdioStderr},
		ExecChunk{Text: "stdout\n", Stream: StdioStdout},
	)

	want := []ExecChunk{
		{Text: "real error", Stream: StdioStderr},
		{Text: "stdout\n", Stream: StdioStdout},
	}
	if !reflect.DeepEqual(chunks, want) {
		t.Fatalf("unexpected chunks: got %#v want %#v", chunks, want)
	}
	if facts := filter.SecurityFacts(); !facts.IsZero() {
		t.Fatalf("SecurityFacts() = %+v, want no facts for real stderr", facts)
	}
}

func TestExecOutputFilterKeepsStderrAfterWarning(t *testing.T) {
	filter := newExecOutputFilter()
	warning := "2026-05-05T15:49:43Z WARN libcontainer::process::init::process: seccomp not available, unable to set seccomp privileges!\n"
	chunks := collectFilteredChunks(filter,
		ExecChunk{Text: warning, Stream: StdioStderr},
		ExecChunk{Text: "boom\n", Stream: StdioStderr},
	)

	want := []ExecChunk{
		{Text: warning, Stream: StdioStderr},
		{Text: "boom\n", Stream: StdioStderr},
	}
	if !reflect.DeepEqual(chunks, want) {
		t.Fatalf("stderr after the warning must be preserved: got %#v want %#v", chunks, want)
	}
	if facts := filter.SecurityFacts(); facts.SeccompUnavailable != 1 {
		t.Fatalf("SecurityFacts() = %+v, want one seccomp report", facts)
	}
}

// TestExecOutputFilterReportsNoNewPrivilegesWarning covers the second silenced
// message, which has no logger prefix and must still be counted.
func TestExecOutputFilterReportsNoNewPrivilegesWarning(t *testing.T) {
	filter := newExecOutputFilter()
	chunks := collectFilteredChunks(filter,
		ExecChunk{Text: "seccomp not available, unable to enforce no_new_privileges!\n", Stream: StdioStderr},
	)

	want := []ExecChunk{{Text: "seccomp not available, unable to enforce no_new_privileges!\n", Stream: StdioStderr}}
	if !reflect.DeepEqual(chunks, want) {
		t.Fatalf("the no_new_privileges warning must reach the user: got %#v want %#v", chunks, want)
	}
	facts := filter.SecurityFacts()
	if facts.NoNewPrivilegesUnavailable != 1 || facts.SeccompUnavailable != 0 {
		t.Fatalf("SecurityFacts() = %+v, want one no_new_privileges report", facts)
	}
	if facts.Total() != 1 {
		t.Fatalf("Total() = %d, want 1", facts.Total())
	}
}

func TestExecSecurityFactsMergeAccumulates(t *testing.T) {
	merged := ExecSecurityFacts{SeccompUnavailable: 1}.Merge(ExecSecurityFacts{SeccompUnavailable: 2, NoNewPrivilegesUnavailable: 1})
	if merged.SeccompUnavailable != 3 || merged.NoNewPrivilegesUnavailable != 1 {
		t.Fatalf("Merge() = %+v, want accumulated counts", merged)
	}
	if merged.IsZero() {
		t.Fatal("IsZero() = true for a non-empty fact set")
	}
	if merged.Pointer() == nil {
		t.Fatal("Pointer() = nil for a non-empty fact set")
	}
	if (ExecSecurityFacts{}).Pointer() != nil {
		t.Fatal("Pointer() != nil for an empty fact set")
	}
}

func collectFilteredChunks(filter *execOutputFilter, input ...ExecChunk) []ExecChunk {
	var output []ExecChunk
	emit := func(chunk ExecChunk) {
		output = append(output, chunk)
	}
	for _, chunk := range input {
		filter.Write(chunk, emit)
	}
	filter.Finish(emit)
	return output
}

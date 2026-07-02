package execfilter

import (
	. "agent-compose/pkg/driver/types"
	"reflect"
	"testing"
)

func TestExecOutputFilterDropsKnownSeccompWarning(t *testing.T) {
	testExecOutputFilterWorkflows(t)
}

func testExecOutputFilterWorkflows(t *testing.T) {
	t.Helper()
	filter := New()
	chunks := collectFilteredChunks(filter,
		ExecChunk{Text: "\x1b[2m2026-05-05T15:49:43.862984Z\x1b[0m \x1b[33m WARN\x1b[0m \x1b[2mlibcontainer::process::init::process\x1b[0m\x1b[2m:\x1b[0m seccomp not available, unable to set seccomp privileges!\n", IsStderr: true},
		ExecChunk{Text: "ok\n"},
	)

	want := []ExecChunk{{Text: "ok\n"}}
	if !reflect.DeepEqual(chunks, want) {
		t.Fatalf("unexpected chunks: got %#v want %#v", chunks, want)
	}

	filter = New()
	chunks = collectFilteredChunks(filter,
		ExecChunk{Text: "\x1b[2m2026-05-05T15:49:43.862984Z\x1b[0m \x1b[33m WARN\x1b[0m \x1b[2mlibcontainer::process::init::process", IsStderr: true},
		ExecChunk{Text: "\x1b[0m\x1b[2m:\x1b[0m seccomp not available, unable to set seccomp privileges!\n", IsStderr: true},
		ExecChunk{Text: "done\n"},
	)

	want = []ExecChunk{{Text: "done\n"}}
	if !reflect.DeepEqual(chunks, want) {
		t.Fatalf("unexpected chunks: got %#v want %#v", chunks, want)
	}

	filter = New()
	chunks = collectFilteredChunks(filter,
		ExecChunk{Text: "real error", IsStderr: true},
		ExecChunk{Text: "stdout\n"},
	)

	want = []ExecChunk{
		{Text: "real error", IsStderr: true},
		{Text: "stdout\n"},
	}
	if !reflect.DeepEqual(chunks, want) {
		t.Fatalf("unexpected chunks: got %#v want %#v", chunks, want)
	}

	filter = New()
	chunks = collectFilteredChunks(filter,
		ExecChunk{Text: "2026-05-05T15:49:43Z WARN libcontainer::process::init::process: seccomp not available, unable to set seccomp privileges!\n", IsStderr: true},
		ExecChunk{Text: "boom\n", IsStderr: true},
	)

	want = []ExecChunk{{Text: "boom\n", IsStderr: true}}
	if !reflect.DeepEqual(chunks, want) {
		t.Fatalf("unexpected chunks: got %#v want %#v", chunks, want)
	}
}

func TestExecOutputFilterHandlesSplitWarning(t *testing.T) {
	testExecOutputFilterWorkflows(t)
}

func TestExecOutputFilterPreservesRealStderr(t *testing.T) {
	testExecOutputFilterWorkflows(t)
}

func TestExecOutputFilterKeepsStderrAfterWarning(t *testing.T) {
	testExecOutputFilterWorkflows(t)
}

func collectFilteredChunks(filter *ExecOutputFilter, input ...ExecChunk) []ExecChunk {
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

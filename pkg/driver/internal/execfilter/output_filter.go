package execfilter

import (
	. "agent-compose/pkg/driver/types"
	"strings"
)

const maxInitialExecStderrBuffer = 1024

var ignoredExecStderrMessages = []string{
	"seccomp not available, unable to set seccomp privileges!",
	"seccomp not available, unable to enforce no_new_privileges!",
}

type ExecOutputFilter struct {
	pendingStderr   strings.Builder
	filterStderrTop bool
}

func New() *ExecOutputFilter {
	return &ExecOutputFilter{filterStderrTop: true}
}

func (f *ExecOutputFilter) Write(chunk ExecChunk, emit func(ExecChunk)) {
	if emit == nil {
		return
	}
	if !chunk.IsStderr {
		f.flushPending(true, emit)
		emit(chunk)
		return
	}
	if !f.filterStderrTop {
		emit(chunk)
		return
	}
	_, _ = f.pendingStderr.WriteString(chunk.Text)
	f.flushPending(false, emit)
}

func (f *ExecOutputFilter) Finish(emit func(ExecChunk)) {
	if emit == nil {
		return
	}
	f.flushPending(true, emit)
}

func (f *ExecOutputFilter) flushPending(final bool, emit func(ExecChunk)) {
	pending := f.pendingStderr.String()
	if pending == "" {
		return
	}

	for {
		newlineIndex := strings.IndexByte(pending, '\n')
		if newlineIndex < 0 {
			break
		}
		line := pending[:newlineIndex+1]
		pending = pending[newlineIndex+1:]
		if isIgnoredExecStderrLine(line) {
			continue
		}
		emit(ExecChunk{Text: line, IsStderr: true})
		f.filterStderrTop = false
		if pending != "" {
			emit(ExecChunk{Text: pending, IsStderr: true})
			pending = ""
		}
		break
	}

	if pending != "" && (final || len(pending) >= maxInitialExecStderrBuffer) {
		if !isIgnoredExecStderrLine(pending) {
			emit(ExecChunk{Text: pending, IsStderr: true})
		}
		f.filterStderrTop = false
		pending = ""
	}

	f.pendingStderr.Reset()
	_, _ = f.pendingStderr.WriteString(pending)
}

func isIgnoredExecStderrLine(line string) bool {
	if !strings.Contains(line, "libcontainer::process::init::process") {
		return false
	}
	for _, message := range ignoredExecStderrMessages {
		if strings.Contains(line, message) {
			return true
		}
	}
	return false
}

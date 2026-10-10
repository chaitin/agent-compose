package driver

import "strings"

// maxInitialExecStderrBuffer bounds how long a newline-less stderr head is held
// before it is emitted. It is a memory bound, not a suppression window: the
// buffer exists to reassemble a line the lower layer split across chunks.
const maxInitialExecStderrBuffer = 1024

// Lower-layer isolation failure messages.
//
// These are the libcontainer (youki-style) reports §1.5 of the isolation plan
// calls out: the lower layer tries to set seccomp and no_new_privs, and says so
// on stderr when it cannot. SEC-3 deliberately reconnects them.
//
// BEHAVIOR CHANGE: the filter used to DROP these lines so they never reached a
// user. It now preserves every line and additionally counts the security
// facts, because "reduce noise" must never mean "hide a security fact". The
// four tests in exec_output_filter_test.go used to assert the drop; they now
// assert the report.
const (
	lowerLayerSeccompUnavailableMessage         = "seccomp not available, unable to set seccomp privileges!"
	lowerLayerNoNewPrivilegesUnavailableMessage = "seccomp not available, unable to enforce no_new_privileges!"
)

// ExecSecurityFacts counts the isolation-enforcement failures a lower layer
// reported on one exec's stderr. A nonzero count is engine-measured evidence:
// the engine did not ask a driver whether it was ready, it observed the lower
// layer say it failed. pkg/capmatrix turns these into unsupported capability
// assertions.
type ExecSecurityFacts struct {
	// SeccompUnavailable counts reports that seccomp privileges were not set.
	SeccompUnavailable int `json:"seccomp_unavailable,omitempty"`
	// NoNewPrivilegesUnavailable counts reports that no_new_privileges was not
	// enforced.
	NoNewPrivilegesUnavailable int `json:"no_new_privileges_unavailable,omitempty"`
}

// IsZero reports whether the lower layer reported no isolation failure.
func (f ExecSecurityFacts) IsZero() bool {
	return f.SeccompUnavailable == 0 && f.NoNewPrivilegesUnavailable == 0
}

// Total returns how many isolation failures were observed.
func (f ExecSecurityFacts) Total() int {
	return f.SeccompUnavailable + f.NoNewPrivilegesUnavailable
}

// Merge returns the sum of two observations, so facts gathered across several
// exec calls or several concurrent streams accumulate instead of overwriting.
func (f ExecSecurityFacts) Merge(other ExecSecurityFacts) ExecSecurityFacts {
	return ExecSecurityFacts{
		SeccompUnavailable:         f.SeccompUnavailable + other.SeccompUnavailable,
		NoNewPrivilegesUnavailable: f.NoNewPrivilegesUnavailable + other.NoNewPrivilegesUnavailable,
	}
}

// Pointer returns a pointer to a copy of the facts, or nil when the lower layer
// reported no isolation failure. It keeps the optional field out of a result
// JSON payload when there is nothing to report.
func (f ExecSecurityFacts) Pointer() *ExecSecurityFacts {
	if f.IsZero() {
		return nil
	}
	copy := f
	return &copy
}

// execOutputFilter reduces exec stderr noise while never hiding the security
// facts it observes. It reassembles lines split across chunks, emits every
// line, and counts the lower-layer isolation failures.
//
// Framing note: the previous filter fast-pathed stderr verbatim after the head
// line, so it buffered only the start of the stream. This filter reassembles
// every stderr chunk by newline (bounded by maxInitialExecStderrBuffer for a
// newline-less run), which is what makes the split-warning count exact but also
// means a long stderr run without newlines is held up to that bound before it
// is emitted.
type execOutputFilter struct {
	pendingStderr strings.Builder
	facts         ExecSecurityFacts
}

func newExecOutputFilter() *execOutputFilter {
	return &execOutputFilter{}
}

// SecurityFacts returns the isolation failures observed so far. The value is a
// snapshot; later writes do not mutate it.
func (f *execOutputFilter) SecurityFacts() ExecSecurityFacts {
	if f == nil {
		return ExecSecurityFacts{}
	}
	return f.facts
}

func (f *execOutputFilter) Write(chunk ExecChunk, emit func(ExecChunk)) {
	if emit == nil {
		return
	}
	if NormalizeStdioStream(chunk.Stream) != StdioStderr {
		f.flushPending(true, emit)
		emit(chunk)
		return
	}
	_, _ = f.pendingStderr.WriteString(chunk.Text)
	f.flushPending(false, emit)
}

func (f *execOutputFilter) Finish(emit func(ExecChunk)) {
	if emit == nil {
		return
	}
	f.flushPending(true, emit)
}

func (f *execOutputFilter) flushPending(final bool, emit func(ExecChunk)) {
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
		f.recordSecurityFact(line)
		emit(ExecChunk{Text: line, Stream: StdioStderr})
	}

	if pending != "" && (final || len(pending) >= maxInitialExecStderrBuffer) {
		f.recordSecurityFact(pending)
		emit(ExecChunk{Text: pending, Stream: StdioStderr})
		pending = ""
	}

	f.pendingStderr.Reset()
	_, _ = f.pendingStderr.WriteString(pending)
}

// recordSecurityFact counts a completed stderr line that reports a lower-layer
// isolation failure. It matches on the message text alone: the exact phrases
// are unambiguous, and requiring a logger prefix could drop a real report.
func (f *execOutputFilter) recordSecurityFact(line string) {
	f.facts = f.facts.Merge(countExecSecurityFacts(line))
}

// countExecSecurityFacts counts isolation failure reports in an already
// complete stderr body. The non-streaming exec paths never build a filter, so
// they scan their captured stderr with this instead of losing the fact.
//
// It matches the message text alone, so a workload that prints the same phrase
// from inside the sandbox can fabricate a fact. The direction is safe: a
// fabricated fact can only make a dimension look less enforced (fail-closed),
// never more, and the engine reports it rather than using it to grant trust.
func countExecSecurityFacts(text string) ExecSecurityFacts {
	var facts ExecSecurityFacts
	if strings.Contains(text, lowerLayerSeccompUnavailableMessage) {
		facts.SeccompUnavailable = strings.Count(text, lowerLayerSeccompUnavailableMessage)
	}
	if strings.Contains(text, lowerLayerNoNewPrivilegesUnavailableMessage) {
		facts.NoNewPrivilegesUnavailable = strings.Count(text, lowerLayerNoNewPrivilegesUnavailableMessage)
	}
	return facts
}

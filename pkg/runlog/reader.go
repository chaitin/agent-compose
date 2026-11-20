package runlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// ReadEntries returns the entries of the run log at path with Seq > afterSeq.
// A torn trailing line, left by a crash mid-write, is skipped.
func ReadEntries(path string, afterSeq uint64) ([]Entry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return DecodeEntries(file, afterSeq)
}

// DecodeEntries reads run log lines from r; see ReadEntries.
func DecodeEntries(r io.Reader, afterSeq uint64) ([]Entry, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), int(DefaultMaxBytes))
	var entries []Entry
	for scanner.Scan() {
		var entry Entry
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		if entry.Seq > afterSeq {
			entries = append(entries, entry)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read run log: %w", err)
	}
	return entries, nil
}

// RenderText concatenates the text view of entries.
func RenderText(entries []Entry) string {
	var b strings.Builder
	for _, entry := range entries {
		b.WriteString(Render(entry))
	}
	return b.String()
}

// TextFrom returns the run's text view starting at byte offset start, and the
// offset just past it. Entries record where their text begins, so the slice
// is exact even when start falls inside an entry.
func TextFrom(entries []Entry, start uint64) (string, uint64) {
	var b strings.Builder
	end := start
	for _, entry := range entries {
		rendered := Render(entry)
		if rendered == "" {
			continue
		}
		entryEnd := entry.TextOffset + uint64(len(rendered))
		if entryEnd > end {
			end = entryEnd
		}
		if entryEnd <= start {
			continue
		}
		if entry.TextOffset >= start {
			b.WriteString(rendered)
			continue
		}
		b.WriteString(rendered[start-entry.TextOffset:])
	}
	return b.String(), end
}

// Summary is what a run list needs without reading the whole log.
type Summary struct {
	Turns           int     `json:"turns,omitempty"`
	ToolCalls       int     `json:"tool_calls,omitempty"`
	ToolFailures    int     `json:"tool_failures,omitempty"`
	InputTokens     int64   `json:"input_tokens,omitempty"`
	OutputTokens    int64   `json:"output_tokens,omitempty"`
	ReasoningTokens int64   `json:"reasoning_tokens,omitempty"`
	CachedTokens    int64   `json:"cached_tokens,omitempty"`
	CostUSD         float64 `json:"cost_usd,omitempty"`
	LastError       string  `json:"last_error,omitempty"`
	Truncated       bool    `json:"truncated,omitempty"`
}

// Summarize folds entries into a Summary. Providers report usage at
// different scopes (step, turn or run), and records of different scopes must
// not be added together, so the totals come from the coarsest scope present.
func Summarize(entries []Entry) Summary {
	var summary Summary
	turns := map[uint32]struct{}{}
	tools := map[string]struct{}{}
	usageByScope := map[string]*Usage{}
	for _, entry := range entries {
		if entry.Turn > 0 && entry.Type != EntryTruncated {
			turns[entry.Turn] = struct{}{}
		}
		switch entry.Type {
		case EntryToolCall:
			if entry.Tool != nil {
				tools[entry.Tool.ID] = struct{}{}
			}
		case EntryToolResult:
			if entry.Tool != nil && entry.Tool.OK != nil && !*entry.Tool.OK {
				summary.ToolFailures++
			}
		case EntryUsage:
			if entry.Usage == nil {
				continue
			}
			scope := entry.Usage.Scope
			total := usageByScope[scope]
			if total == nil {
				total = &Usage{}
				usageByScope[scope] = total
			}
			total.InputTokens += entry.Usage.InputTokens
			total.OutputTokens += entry.Usage.OutputTokens
			total.ReasoningTokens += entry.Usage.ReasoningTokens
			total.CachedTokens += entry.Usage.CachedTokens
			total.CostUSD += entry.Usage.CostUSD
		case EntryError:
			if entry.Severity != "warning" {
				summary.LastError = entry.Text
			}
		case EntryTruncated:
			summary.Truncated = true
		}
	}
	summary.Turns = len(turns)
	summary.ToolCalls = len(tools)
	for _, scope := range []string{"run", "turn", "step", ""} {
		if total := usageByScope[scope]; total != nil {
			summary.InputTokens = total.InputTokens
			summary.OutputTokens = total.OutputTokens
			summary.ReasoningTokens = total.ReasoningTokens
			summary.CachedTokens = total.CachedTokens
			summary.CostUSD = total.CostUSD
			break
		}
	}
	return summary
}

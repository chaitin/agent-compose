package runlog

// Tool IDs are unique within a provider turn, rather than across the run.
type toolIdentity struct {
	turn uint32
	id   string
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
	s := newSummarizer()
	for _, entry := range entries {
		s.add(entry)
	}
	return s.result()
}

// SummarizeLog folds the run log at path into a Summary as it reads it, so
// summarizing a large log does not hold it in memory.
func SummarizeLog(path string) (Summary, error) {
	s := newSummarizer()
	err := ScanEntries(path, func(entry Entry) bool {
		s.add(entry)
		return true
	})
	if err != nil {
		return Summary{}, err
	}
	return s.result(), nil
}

type summarizer struct {
	summary      Summary
	turns        map[uint32]struct{}
	tools        map[toolIdentity]struct{}
	usageByScope map[string]*Usage
}

func newSummarizer() *summarizer {
	return &summarizer{
		turns:        map[uint32]struct{}{},
		tools:        map[toolIdentity]struct{}{},
		usageByScope: map[string]*Usage{},
	}
}

func (s *summarizer) add(entry Entry) {
	if entry.Turn > 0 && entry.Type != EntryTruncated {
		s.turns[entry.Turn] = struct{}{}
	}
	switch entry.Type {
	case EntryToolCall:
		if entry.Tool != nil {
			s.tools[toolIdentity{turn: entry.Turn, id: entry.Tool.ID}] = struct{}{}
		}
	case EntryToolResult:
		if entry.Tool != nil && entry.Tool.OK != nil && !*entry.Tool.OK {
			s.summary.ToolFailures++
		}
	case EntryUsage:
		if entry.Usage == nil {
			return
		}
		scope := entry.Usage.Scope
		total := s.usageByScope[scope]
		if total == nil {
			total = &Usage{}
			s.usageByScope[scope] = total
		}
		total.InputTokens += entry.Usage.InputTokens
		total.OutputTokens += entry.Usage.OutputTokens
		total.ReasoningTokens += entry.Usage.ReasoningTokens
		total.CachedTokens += entry.Usage.CachedTokens
		total.CostUSD += entry.Usage.CostUSD
	case EntryError:
		if entry.Severity != "warning" {
			s.summary.LastError = entry.Text
		}
	case EntryTruncated:
		s.summary.Truncated = true
	}
}

func (s *summarizer) result() Summary {
	summary := s.summary
	summary.Turns = len(s.turns)
	summary.ToolCalls = len(s.tools)
	for _, scope := range []string{"run", "turn", "step", ""} {
		if total := s.usageByScope[scope]; total != nil {
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

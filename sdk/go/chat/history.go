package chat

import (
	"connectrpc.com/connect"
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strconv"
	"strings"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// HistoryOptions bounds one page of a [Conversation.HistoryPage] read.
type HistoryOptions struct {
	// Limit caps how many messages the page returns. Zero uses a sensible
	// default.
	Limit int
	// Before resumes a walk started by an earlier call's [HistoryPage.Cursor],
	// continuing further into the conversation's past. Empty starts from the
	// most recent message.
	Before string
}

// HistoryPage is one page of a conversation's transcript, oldest message
// first.
type HistoryPage struct {
	// Messages is this page, oldest first.
	Messages []Message
	// Cursor resumes the walk further into the past via
	// [HistoryOptions.Before]. Empty means there is nothing older.
	Cursor string
}

// HistoryPage returns one page of the conversation's transcript, walking
// backward from the most recent message.
//
// Unlike [Conversation.History], which fetches every turn the conversation
// has ever had, HistoryPage fetches only what a page needs: opening a long
// conversation should cost a bounded number of requests, not one proportional
// to how many turns it has accumulated. A chat UI that loads the most recent
// messages and reaches further back only when the reader scrolls up should
// use this instead of History.
func (c *Conversation) HistoryPage(ctx context.Context, opts HistoryOptions) (HistoryPage, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = historyPageSize
	}
	runs, err := c.runs(ctx, "History")
	if err != nil {
		return HistoryPage{}, err
	}

	runIndex, upto, fresh := len(runs)-1, uint32(0), true
	if opts.Before != "" {
		decodedIndex, decodedUpto, decodeErr := decodeHistoryCursor(opts.Before)
		if decodeErr != nil || decodedIndex < 0 || decodedIndex >= len(runs) {
			return HistoryPage{}, invalidArgument("History", "cursor does not match this conversation")
		}
		runIndex, upto, fresh = decodedIndex, decodedUpto, false
	}

	var page []Message
	for runIndex >= 0 && len(page) < limit {
		runID := runs[runIndex].GetRunId()
		bound := upto
		if fresh {
			if bound, err = c.runEventTotal(ctx, runID); err != nil {
				return HistoryPage{}, err
			}
		}
		if bound == 0 {
			// Nothing left in this run; move to the one before it.
			runIndex, fresh = runIndex-1, true
			continue
		}
		found, resumeAt, err := c.runMessagesBefore(ctx, runID, bound, limit-len(page))
		if err != nil {
			return HistoryPage{}, err
		}
		page = append(found, page...)
		if len(page) >= limit {
			if resumeAt > 0 || runIndex > 0 {
				return HistoryPage{Messages: page, Cursor: encodeHistoryCursor(runIndex, resumeAt)}, nil
			}
			return HistoryPage{Messages: page}, nil
		}
		runIndex, fresh = runIndex-1, true
	}
	return HistoryPage{Messages: page}, nil
}

// runEventTotal reads how many events a run has recorded, without fetching
// them.
func (c *Conversation) runEventTotal(ctx context.Context, runID string) (uint32, error) {
	response, err := c.agent.client.transport.runs.ListRunEvents(ctx, connect.NewRequest(&agentcomposev2.ListRunEventsRequest{
		RunId: runID, Limit: 1,
	}))
	if err != nil {
		return 0, fromConnect("History", err)
	}
	return response.Msg.GetTotal(), nil
}

// runMessagesBefore returns up to want messages taken from the tail of one
// run's events, considering only events before the exclusive offset bound,
// oldest first. The second result is the offset a later call should treat as
// its own bound to continue further into this run's past; it is 0 once the
// run's start has been reached.
//
// The scanned window doubles until it holds enough messages or reaches the
// run's start, so a run thick with non-conversational events (tool calls,
// usage, status transitions) is not mistaken for one with too few messages
// after a single small probe.
func (c *Conversation) runMessagesBefore(ctx context.Context, runID string, bound uint32, want int) ([]Message, uint32, error) {
	for window := uint32(historyPageSize); ; window *= 2 {
		start := uint32(0)
		if window < bound {
			start = bound - window
		}
		events, err := c.runEventsInRange(ctx, runID, start, bound)
		if err != nil {
			return nil, 0, err
		}
		messages := make([]Message, 0, want)
		resumeAt := start
		for i := len(events) - 1; i >= 0 && len(messages) < want; i-- {
			message, ok := messageFromEvent(events[i])
			if !ok {
				continue
			}
			messages = append(messages, message)
			resumeAt = start + uint32(i)
		}
		if len(messages) >= want || start == 0 {
			slices.Reverse(messages)
			return messages, resumeAt, nil
		}
	}
}

// runEventsInRange reads one run's raw durable events in [start, end), oldest
// first, paging through the daemon's own per-request limit.
func (c *Conversation) runEventsInRange(ctx context.Context, runID string, start, end uint32) ([]*agentcomposev2.RunEvent, error) {
	events := make([]*agentcomposev2.RunEvent, 0, end-start)
	for offset := start; offset < end; {
		limit := end - offset
		if limit > historyPageSize {
			limit = historyPageSize
		}
		response, err := c.agent.client.transport.runs.ListRunEvents(ctx, connect.NewRequest(&agentcomposev2.ListRunEventsRequest{
			RunId: runID, Offset: offset, Limit: limit,
		}))
		if err != nil {
			return nil, fromConnect("History", err)
		}
		page := response.Msg.GetEvents()
		if len(page) == 0 {
			break
		}
		events = append(events, page...)
		offset += uint32(len(page))
	}
	return events, nil
}

// encodeHistoryCursor and decodeHistoryCursor keep HistoryPage.Cursor opaque
// to callers: which run and how far into it is this package's own bookkeeping,
// not a contract to keep stable.
func encodeHistoryCursor(runIndex int, upto uint32) string {
	raw := strconv.Itoa(runIndex) + ":" + strconv.FormatUint(uint64(upto), 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeHistoryCursor(cursor string) (runIndex int, upto uint32, err error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, 0, err
	}
	left, right, ok := strings.Cut(string(decoded), ":")
	if !ok {
		return 0, 0, errors.New("malformed cursor")
	}
	index, err := strconv.Atoi(left)
	if err != nil {
		return 0, 0, err
	}
	offset, err := strconv.ParseUint(right, 10, 32)
	if err != nil {
		return 0, 0, err
	}
	return index, uint32(offset), nil
}

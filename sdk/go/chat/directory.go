package chat

import (
	"connectrpc.com/connect"
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// listPageSize bounds one page of a conversation search. The daemon rejects
// pages above 500.
const listPageSize = 200

// ConversationInfo describes a conversation found on the server, without
// opening it.
//
// It is a summary of what the server knows: which Agent the conversation is
// with, when it was last active, and the labels it was created with. A title,
// an unread marker, or anything else a product invents about a conversation is
// the product's to keep — this package only reports what the daemon holds.
type ConversationInfo struct {
	// ID is the conversation's identity, the value [Agent.Open] takes.
	ID string
	// ProjectID and AgentName identify the counterpart, so a caller listing
	// across several Agents can reopen each conversation with the right one.
	ProjectID string
	AgentName string
	// Labels are the labels the conversation was created with, minus the one
	// this package uses for identity. It is nil for a conversation reached
	// through [Client.Lookup], which does not read them.
	Labels map[string]string
	// LastActive is when the conversation's most recent session started.
	LastActive time.Time
	// Live reports whether that session is still running, which is what
	// separates a conversation that can be resumed with its context intact
	// from one that would be rebuilt.
	Live bool
}

// Lookup reports what the server knows about one conversation, and nothing if
// it has never run or does not match every label in mustMatch.
//
// This is the cheap question, and the one authorization needs: "is there a
// conversation with this ID carrying these labels?" is answered by the run
// list's label filter alone, in a single call, without reading any label back.
// A product can therefore check that a conversation belongs to the user in
// front of it without keeping its own record of who owns what.
func (c *Client) Lookup(ctx context.Context, id string, mustMatch map[string]string) (ConversationInfo, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ConversationInfo{}, false, invalidArgument("Lookup", "conversation ID is required")
	}
	labels := maps.Clone(mustMatch)
	if labels == nil {
		labels = map[string]string{}
	}
	labels[conversationLabel] = id

	latest, err := c.latestMatchingRun(ctx, "Lookup", Search{Labels: labels})
	if err != nil {
		return ConversationInfo{}, false, err
	}
	if latest == nil {
		return ConversationInfo{}, false, nil
	}
	// The ID is what was asked for, so it needs no confirming: a run came back
	// only because it carries that label.
	return ConversationInfo{
		ID:         id,
		ProjectID:  latest.GetProjectId(),
		AgentName:  latest.GetAgentName(),
		LastActive: latest.GetCreatedAt().AsTime(),
		Live:       runIsLive(latest),
	}, true, nil
}

// Search selects which conversations [Client.Conversations] returns.
//
// An empty Search matches every conversation this package created. Fields
// combine with AND.
type Search struct {
	// ProjectID and AgentName narrow the search to one counterpart. Empty
	// means any.
	ProjectID string
	AgentName string
	// Labels narrows to conversations carrying every one of these labels,
	// which is how a product finds the conversations belonging to one of its
	// users.
	Labels map[string]string
	// Limit caps how many runs are examined, not how many conversations come
	// back: a conversation that was rebuilt occupies several runs. It is a
	// budget across every page read, so a conversation whose runs all fall
	// beyond it is not reported at all. Zero examines every matching run,
	// which is what makes an empty Search mean every conversation.
	Limit int
}

// Conversations lists the conversations matching search, most recently active
// first.
//
// This is the expensive question. The daemon's run list returns summaries
// without labels — labels belong to a run's detail — so discovering which
// conversation each run belongs to costs one detail read per run. A product
// that shows a chat list on every page load should keep its own index of the
// conversation IDs it created and call [Agent.Open] directly; this is for
// rebuilding such an index, or for tools that never had one.
func (c *Client) Conversations(ctx context.Context, search Search) ([]ConversationInfo, error) {
	runs, err := c.matchingRuns(ctx, "Conversations", search)
	if err != nil {
		return nil, err
	}
	found := make(map[string]ConversationInfo, len(runs))
	for _, run := range runs {
		labels, err := c.runLabels(ctx, run.GetRunId())
		if err != nil {
			return nil, err
		}
		id := labels[conversationLabel]
		if id == "" {
			// A run this package did not start. It has no conversation
			// identity, and inventing one from its run ID would produce an ID
			// that Open cannot resume.
			continue
		}
		if seen, ok := found[id]; ok && seen.LastActive.After(run.GetCreatedAt().AsTime()) {
			continue
		}
		remaining := maps.Clone(labels)
		delete(remaining, conversationLabel)
		found[id] = ConversationInfo{
			ID:         id,
			ProjectID:  run.GetProjectId(),
			AgentName:  run.GetAgentName(),
			Labels:     remaining,
			LastActive: run.GetCreatedAt().AsTime(),
			Live:       runIsLive(run),
		}
	}

	conversations := slices.Collect(maps.Values(found))
	slices.SortStableFunc(conversations, func(a, b ConversationInfo) int {
		if order := b.LastActive.Compare(a.LastActive); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	return conversations, nil
}

// matchingRuns reads the runs matching search, walking as many pages as
// search.Limit allows.
//
// The daemon caps a page well below what a busy conversation accumulates, so
// reading one page and stopping would silently drop every conversation whose
// runs fall outside it. Limit bounds the walk instead, and what it excludes is
// excluded by the caller's own choice.
func (c *Client) matchingRuns(ctx context.Context, op string, search Search) ([]*agentcomposev2.RunSummary, error) {
	budget := search.Limit
	var runs []*agentcomposev2.RunSummary
	for offset := uint32(0); budget <= 0 || len(runs) < budget; {
		size := uint32(listPageSize)
		if budget > 0 {
			size = min(uint32(budget-len(runs)), listPageSize)
		}
		page, total, err := c.listRunsPage(ctx, op, search, offset, size)
		if err != nil {
			return nil, err
		}
		runs = append(runs, page...)
		offset += uint32(len(page))
		if len(page) == 0 || offset >= total {
			break
		}
	}
	return runs, nil
}

// latestMatchingRun returns the most recent run matching search, or nil when
// there is none.
//
// A single row answers this. The daemon orders a run list by creation time,
// newest first, so the first row is the latest run however many the
// conversation has accumulated; reading a page to pick the newest out of it
// would cost more for the same answer, and reading only the first page of a
// long list would risk a wrong one.
func (c *Client) latestMatchingRun(ctx context.Context, op string, search Search) (*agentcomposev2.RunSummary, error) {
	runs, _, err := c.listRunsPage(ctx, op, search, 0, 1)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	return runs[0], nil
}

// listRunsPage reads one page of the filtered run list, and how many runs match
// it in total.
func (c *Client) listRunsPage(ctx context.Context, op string, search Search, offset, limit uint32) ([]*agentcomposev2.RunSummary, uint32, error) {
	response, err := c.transport.runs.ListRuns(ctx, connect.NewRequest(&agentcomposev2.ListRunsRequest{
		ProjectId: strings.TrimSpace(search.ProjectID),
		AgentName: strings.TrimSpace(search.AgentName),
		Labels:    maps.Clone(search.Labels),
		Offset:    offset,
		Limit:     limit,
	}))
	if err != nil {
		return nil, 0, fromConnect(op, err)
	}
	return response.Msg.GetRuns(), response.Msg.GetTotal(), nil
}

// runLabels reads one run's labels, which only the detail view carries.
func (c *Client) runLabels(ctx context.Context, runID string) (map[string]string, error) {
	response, err := c.transport.runs.GetRun(ctx, connect.NewRequest(&agentcomposev2.GetRunRequest{RunId: runID}))
	if err != nil {
		return nil, fromConnect("Conversations", err)
	}
	return response.Msg.GetRun().GetLabels(), nil
}

// EndSession ends whatever run a conversation currently has, releasing the
// environment that run was holding. It is addressed by ID and needs no
// attached handle, which is what a product needs to retire a conversation it
// is not holding open — after a restart, say, when its own sessions are gone
// but the daemon's runs are not.
//
// The conversation itself is untouched: its history and identity survive, and
// [Agent.Open] resumes it, reporting [Restarted] because the environment is
// gone. Ending a session that has already ended is not an error.
//
// [Conversation.Close] is the right call when you do hold the handle; closing
// already ends the run it holds.
func (c *Client) EndSession(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return invalidArgument("EndSession", "conversation ID is required")
	}
	latest, err := c.latestMatchingRun(ctx, "EndSession", Search{
		Labels: map[string]string{conversationLabel: id},
	})
	if err != nil {
		return err
	}
	if latest == nil {
		return nil
	}
	// The run's status is not consulted: stopping one that has already
	// finished answers plainly and changes nothing, while trusting a status
	// string the daemon might spell differently would leave a live run — and
	// its environment — behind.
	_, err = c.transport.runs.StopRun(ctx, connect.NewRequest(&agentcomposev2.StopRunRequest{
		RunId:  latest.GetRunId(),
		Reason: "conversation session ended",
	}))
	return fromConnect("EndSession", err)
}

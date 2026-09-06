package chat

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"
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
	// this package uses for identity.
	Labels map[string]string
	// LastActive is when the conversation's most recent session started.
	LastActive time.Time
	// Live reports whether that session is still running, which is what
	// separates a conversation that can be resumed with its context intact
	// from one that would be rebuilt.
	Live bool
}

// Search selects which conversations [Client.Conversations] returns.
//
// An empty Search matches every conversation this package created. Fields
// combine with AND.
type Search struct {
	// ID narrows the search to one conversation, which is how a caller asks
	// whether a given conversation exists and is still live without opening
	// it. Combined with Labels it also answers whether it is theirs.
	ID string
	// ProjectID and AgentName narrow the search to one counterpart. Empty
	// means any.
	ProjectID string
	AgentName string
	// Labels narrows to conversations carrying every one of these labels,
	// which is how a product finds the conversations belonging to one of its
	// users.
	Labels map[string]string
	// Limit caps how many runs are examined, not how many conversations come
	// back: a conversation that was rebuilt occupies several runs. Zero uses a
	// sensible default.
	Limit int
}

// Conversations lists the conversations matching search, most recently active
// first.
//
// A conversation is not a server-side object: it is a set of runs sharing an
// identity label, so this reads one page of runs and folds them together. A
// conversation that has been rebuilt appears once, described by its most
// recent run.
func (c *Client) Conversations(ctx context.Context, search Search) ([]ConversationInfo, error) {
	limit := search.Limit
	if limit <= 0 || limit > listPageSize {
		limit = listPageSize
	}
	labels := maps.Clone(search.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	if id := strings.TrimSpace(search.ID); id != "" {
		labels[conversationLabel] = id
	}
	request := wireListRunsRequest{
		ProjectID: strings.TrimSpace(search.ProjectID),
		AgentName: strings.TrimSpace(search.AgentName),
		Labels:    labels,
		Limit:     uint32(limit),
	}
	var response wireListRunsResponse
	if err := c.transport.unary(ctx, "Conversations", "ListRuns", request, &response); err != nil {
		return nil, err
	}

	found := make(map[string]ConversationInfo, len(response.Runs))
	for _, run := range response.Runs {
		id := run.Labels[conversationLabel]
		if id == "" {
			// A run this package did not start. It has no conversation
			// identity, and inventing one from its run ID would produce an ID
			// that Open cannot resume.
			continue
		}
		if seen, ok := found[id]; ok && seen.LastActive.After(run.CreatedAt) {
			continue
		}
		remaining := maps.Clone(run.Labels)
		delete(remaining, conversationLabel)
		found[id] = ConversationInfo{
			ID:         id,
			ProjectID:  run.ProjectID,
			AgentName:  run.AgentName,
			Labels:     remaining,
			LastActive: run.CreatedAt,
			Live:       run.live(),
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

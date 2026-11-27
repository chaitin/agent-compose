package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

// stepLogRunIDPrefix marks the summary a scheduler step is printed under, so
// its lines read <agent>-step-<id> rather than as an Agent Run's.
const stepLogRunIDPrefix = "step:"

// sandboxLogUnit is one execution whose log `logs --sandbox` prints: an Agent
// Run, or a shell/exec step a scheduler run ran in the sandbox.
type sandboxLogUnit struct {
	run  *agentcomposev2.RunSummary
	step *agentcomposev2.SchedulerEvent
	at   time.Time
}

// listSandboxSteps returns the started (or, for a step that predates started
// events naming their step, the finishing) event of each scheduler step in
// the sandbox, oldest first.
func listSandboxSteps(ctx context.Context, client agentcomposev2connect.ProjectServiceClient, projectID, sandboxID string) ([]*agentcomposev2.SchedulerEvent, error) {
	first := map[string]*agentcomposev2.SchedulerEvent{}
	const page = 500
	for offset := uint32(0); ; offset += page {
		resp, err := client.ListProjectSchedulerEvents(ctx, connect.NewRequest(&agentcomposev2.ListProjectSchedulerEventsRequest{
			Project:   &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: projectID}},
			SandboxId: sandboxID, Offset: offset, Limit: page,
		}))
		if err != nil {
			return nil, err
		}
		for _, event := range resp.Msg.GetEvents() {
			cellID := strings.TrimSpace(event.GetLinkedCellId())
			if cellID == "" || !strings.HasPrefix(event.GetType(), "scheduler.command.") {
				continue
			}
			// Events page newest first: keep each step's earliest, which says
			// when it started.
			if known, ok := first[cellID]; !ok || event.GetCreatedAt().AsTime().Before(known.GetCreatedAt().AsTime()) {
				first[cellID] = event
			}
		}
		if uint32(len(resp.Msg.GetEvents())) < page || offset+page >= resp.Msg.GetTotal() {
			break
		}
	}
	steps := make([]*agentcomposev2.SchedulerEvent, 0, len(first))
	for _, event := range first {
		steps = append(steps, event)
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].GetCreatedAt().AsTime().Before(steps[j].GetCreatedAt().AsTime()) })
	return steps, nil
}

// sandboxLogUnits orders a sandbox's Agent Runs and scheduler steps by when
// they started.
func sandboxLogUnits(runs []*agentcomposev2.RunSummary, steps []*agentcomposev2.SchedulerEvent) []sandboxLogUnit {
	units := make([]sandboxLogUnit, 0, len(runs)+len(steps))
	for _, run := range runs {
		at, _ := parseComposeLogSortTimestamp(runLogSortTimestamp(run))
		units = append(units, sandboxLogUnit{run: run, at: at})
	}
	for _, step := range steps {
		units = append(units, sandboxLogUnit{step: step, at: step.GetCreatedAt().AsTime()})
	}
	sort.SliceStable(units, func(i, j int) bool { return units[i].at.Before(units[j].at) })
	return units
}

// writeStepLog prints the log of one scheduler step as it stands, under the
// same line prefix scheme as an Agent Run's. A step from before step logs has
// none and prints nothing.
func writeStepLog(ctx context.Context, out io.Writer, client agentcomposev2connect.RunServiceClient, step *agentcomposev2.SchedulerEvent, options composeLogsOptions) error {
	req := &agentcomposev2.FollowRunLogsRequest{SchedulerRunId: step.GetRunId(), CellId: step.GetLinkedCellId()}
	if options.TailLines >= 0 {
		req.TailLines, req.TailSet = uint32(options.TailLines), true
	}
	stream, err := client.FollowRunLogs(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()
	summary := &agentcomposev2.RunSummary{RunId: stepLogRunIDPrefix + step.GetLinkedCellId(), AgentName: step.GetAgentName()}
	timestamp := formatProtoTimestamp(step.GetCreatedAt())
	writer := newPrefixedRunOutputStreamWriter(out, options.Timestamp)
	for stream.Receive() {
		if err := writer.Write(summary, stream.Msg().GetData(), timestamp); err != nil {
			return err
		}
	}
	if err := stream.Err(); err != nil {
		if connect.CodeOf(err) == connect.CodeNotFound {
			return writer.Finish()
		}
		return fmt.Errorf("read scheduler step %s log: %w", step.GetLinkedCellId(), err)
	}
	return writer.Finish()
}

// writeSandboxLogUnits prints each unit's log in start order.
func writeSandboxLogUnits(ctx context.Context, out io.Writer, clients cliServiceClients, projectID string, units []sandboxLogUnit, options composeLogsOptions) error {
	for _, unit := range units {
		var err error
		if unit.run != nil {
			err = followRunLogStream(ctx, out, clients.run, projectID, unit.run, options)
		} else {
			err = writeStepLog(ctx, out, clients.run, unit.step, options)
		}
		if err != nil {
			var exitErr commandExitError
			if errors.As(err, &exitErr) {
				return err
			}
			return commandExitErrorForConnect(err)
		}
	}
	return nil
}

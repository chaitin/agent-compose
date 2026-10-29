package driver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/sandboxinit"
)

// openSandboxInitInteraction starts a PTY/pipe-backed command through init.
// Drivers differ only in how they build the Client.
func openSandboxInitInteraction(ctx context.Context, client *sandboxinit.Client, sandbox *Sandbox, defaultCwd string, spec RuntimeStartSpec) (RuntimeInteraction, error) {
	command := RuntimeCommandSpec{}
	if spec.Command != nil {
		command = *spec.Command
	}
	if strings.TrimSpace(command.Command) == "" {
		return nil, fmt.Errorf("sandbox init interaction command is required")
	}
	env := command.Env
	if len(spec.Env) > 0 {
		env = mergeStringMaps(spec.Env, command.Env)
	}
	childCtx, cancel := context.WithCancel(ctx)
	if spec.TimeoutMs > 0 {
		childCtx, cancel = context.WithTimeout(ctx, time.Duration(spec.TimeoutMs)*time.Millisecond)
	}
	marker := newDockerExecMarker()
	session, err := client.Exec(childCtx, sandboxinit.Request{
		Command: command.Command,
		Args:    append([]string(nil), command.Args...),
		Env:     env,
		Cwd:     firstNonEmpty(command.Cwd, spec.Cwd, defaultCwd),
		TTY:     spec.TTY,
		Rows:    uint16(spec.Rows),
		Cols:    uint16(spec.Cols),
		Marker:  marker,
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("start sandbox init interaction for sandbox %s: %w", sandbox.Summary.ID, err)
	}
	interaction := &initCommandInteraction{
		ctx:         childCtx,
		cancel:      cancel,
		client:      client,
		sandbox:     sandbox,
		session:     session,
		marker:      marker,
		operationID: spec.OperationID,
		tty:         spec.TTY,
		startedAt:   time.Now(),
		output:      make(chan RuntimeOutputFrame, 64),
		done:        make(chan struct{}),
	}
	interaction.emit(RuntimeOutputFrame{Type: RuntimeOutputStarted, StartedAt: interaction.startedAt})
	go interaction.run()
	return interaction, nil
}

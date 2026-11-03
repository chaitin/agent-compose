//go:build !unix

package main

import (
	"context"
	"fmt"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func terminalFileDescriptor(any) (int, bool) {
	return 0, false
}

func isTerminalFD(int) bool {
	return false
}

func makeTerminalRaw(int) (func() error, error) {
	return nil, fmt.Errorf("raw terminal mode is not supported on this platform")
}

func terminalSizeForFD(int) *agentcomposev2.AttachTerminalSize {
	return nil
}

func startAttachResizePump(context.Context, int, func(*agentcomposev2.AttachTerminalSize) error) func() {
	return func() {}
}

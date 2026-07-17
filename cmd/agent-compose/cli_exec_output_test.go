package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPromptAttachInputPromptAddsLeadingNewlineWhenOutputIsOpen(t *testing.T) {
	var out bytes.Buffer
	prompt := promptAttachInputPrompt{AgentName: "reviewer", SandboxID: "sandbox-123456789abcdef"}
	if err := writePromptAttachInputPrompt(&out, prompt, false); err != nil {
		t.Fatalf("write prompt without newline returned error: %v", err)
	}
	if err := writePromptAttachInputPrompt(&out, prompt, true); err != nil {
		t.Fatalf("write prompt with newline returned error: %v", err)
	}
	if got, want := out.String(), "reviewer@sandbox-1234:> \nreviewer@sandbox-1234:> "; got != want {
		t.Fatalf("prompt output = %q, want %q", got, want)
	}
	if strings.Contains(out.String(), "outputreviewer@sandbox-1234:>") {
		t.Fatalf("prompt was glued to preceding output: %q", out.String())
	}
}

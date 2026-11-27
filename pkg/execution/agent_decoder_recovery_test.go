package execution

import (
	domain "github.com/chaitin/agent-compose/pkg/model"
	"testing"
)

func TestAgentDecoderIsQuiescentOnlyBetweenProtocolLines(t *testing.T) {
	var d AgentOutputDecoder
	emit := func(AgentOutput) error { return nil }
	if !d.Quiescent() {
		t.Fatal("a fresh decoder holds nothing")
	}
	if err := d.Push(domain.ExecChunk{Text: `{"v":1,"type":"result","finalText":"hel`}, emit); err != nil {
		t.Fatal(err)
	}
	// stderr between the halves of a frame does not complete it.
	if err := d.Push(domain.ExecChunk{Text: "warning", Stream: domain.StdioStderr}, emit); err != nil {
		t.Fatal(err)
	}
	if d.Quiescent() {
		t.Fatal("half a frame must not be committed")
	}
	if err := d.Push(domain.ExecChunk{Text: "lo\"}\n"}, emit); err != nil {
		t.Fatal(err)
	}
	if !d.Quiescent() {
		t.Fatal("a complete frame leaves nothing behind")
	}
	d.DiscardGap()
	if d.Quiescent() {
		t.Fatal("a line discarded after a gap must not be committed")
	}
}

func TestAgentDecoderRestoresAnOrdinaryLineInProgress(t *testing.T) {
	var first, resumed AgentOutputDecoder
	var text string
	emit := func(out AgentOutput) error {
		if out.Frame != nil {
			t.Fatalf("ordinary output decoded as a frame: %+v", out.Frame)
		}
		text += out.Chunk.Text
		return nil
	}
	if err := first.Push(domain.ExecChunk{Text: "progress "}, emit); err != nil {
		t.Fatal(err)
	}
	if !first.Quiescent() {
		t.Fatal("ordinary output is delivered as it comes")
	}
	resumed.Restore(first.Snapshot())
	// The rest of the line looks like JSON, but it continues ordinary output.
	if err := resumed.Push(domain.ExecChunk{Text: "{50%}\n"}, emit); err != nil {
		t.Fatal(err)
	}
	if text != "progress {50%}\n" {
		t.Fatalf("text=%q", text)
	}
}

func TestAgentDecoderResynchronizesAfterJournalGap(t *testing.T) {
	for _, prefix := range []string{"broken continuation\n", ""} {
		var d AgentOutputDecoder
		count := 0
		emit := func(out AgentOutput) error {
			if out.Frame == nil {
				t.Fatalf("leaked broken protocol: %+v", out)
			}
			count++
			return nil
		}
		if err := d.Push(domain.ExecChunk{Text: `{"type":"agent_event","event":`}, emit); err != nil {
			t.Fatal(err)
		}
		d.DiscardGap()
		wire := prefix + "{\"type\":\"result\",\"finalText\":\"ok\"}\n"
		for _, b := range []byte(wire) {
			if err := d.Push(domain.ExecChunk{Text: string(b)}, emit); err != nil {
				t.Fatal(err)
			}
		}
		if count != 1 {
			t.Fatalf("prefix=%q count=%d", prefix, count)
		}
	}
}

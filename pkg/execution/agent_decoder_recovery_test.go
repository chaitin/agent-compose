package execution

import (
	domain "github.com/chaitin/agent-compose/pkg/model"
	"testing"
)

func TestAgentDecoderRestoresSplitFrameAcrossStderr(t *testing.T) {
	var first, resumed AgentOutputDecoder
	var frames []AgentFrame
	var text string
	emit := func(out AgentOutput) error {
		if out.Frame != nil {
			frames = append(frames, *out.Frame)
		} else {
			text += out.Chunk.Text
		}
		return nil
	}
	if err := first.Push(domain.ExecChunk{Text: `{"v":1,"type":"result","finalText":"hel`}, emit); err != nil {
		t.Fatal(err)
	}
	if err := first.Push(domain.ExecChunk{Text: "warning", Stream: domain.StdioStderr}, emit); err != nil {
		t.Fatal(err)
	}
	if err := resumed.Restore(first.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := resumed.Push(domain.ExecChunk{Text: "lo\"}\n"}, emit); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || frames[0].FinalText != "hello" || text != "warning" {
		t.Fatalf("frames=%+v text=%q", frames, text)
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

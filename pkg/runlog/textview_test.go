package runlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"strings"
	"testing"
)

// writeMixedLog writes a log with every kind of entry, multi-byte text,
// entries that render nothing and outputs large enough to span read buffers.
func writeMixedLog(t *testing.T, w *Writer, turns int) {
	t.Helper()
	rng := rand.New(rand.NewSource(7))
	if _, err := w.AppendInitialPrompt("hidden prompt"); err != nil {
		t.Fatal(err)
	}
	for turn := 0; turn < turns; turn++ {
		if turn > 0 {
			if _, err := w.AppendUser(fmt.Sprintf("follow up %d", turn)); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 20; i++ {
			size := 1 + rng.Intn(300)
			if i%7 == 0 {
				size = 70 << 10
			}
			data := strings.Repeat(fmt.Sprintf("line %d-%d 三四\n", turn, i), size/16+1)
			if err := w.AppendOutput("stdout", data); err != nil {
				t.Fatal(err)
			}
			id := fmt.Sprintf("t%d-%d", turn, i)
			mustEvent(t, w, fmt.Sprintf(`{"kind":"tool_call","id":%q,"name":"Bash","status":"completed","command":"echo %d"}`, id, i))
			mustEvent(t, w, fmt.Sprintf(`{"kind":"tool_result","id":%q,"ok":true,"output":"done %d"}`, id, i))
			mustEvent(t, w, `{"kind":"usage","scope":"step","inputTokens":1,"outputTokens":1}`)
			mustEvent(t, w, fmt.Sprintf(`{"kind":"text_delta","text":"answer %d 中文 "}`, i))
		}
		if _, err := w.EndTurn("stop", "final answer"); err != nil {
			t.Fatal(err)
		}
	}
}

func fullText(t *testing.T, path string) string {
	t.Helper()
	entries, err := ReadEntries(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	return RenderText(entries)
}

// referenceTail mirrors the legacy transcript tail: the offset after the
// lines-th newline from the end, ignoring a trailing one.
func referenceTail(text string, lines int) uint64 {
	if lines <= 0 || text == "" {
		return uint64(len(text))
	}
	seen := 0
	for i := len(text) - 1; i >= 0; i-- {
		if text[i] != '\n' || i == len(text)-1 {
			continue
		}
		seen++
		if seen == lines {
			return uint64(i + 1)
		}
	}
	return 0
}

func readAllFrom(t *testing.T, v *TextView, off int64, chunk int) string {
	t.Helper()
	var b strings.Builder
	buf := make([]byte, chunk)
	for {
		n, err := v.ReadAt(buf, off)
		b.Write(buf[:n])
		off += int64(n)
		if errors.Is(err, io.EOF) {
			return b.String()
		}
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Fatal("ReadAt made no progress")
		}
	}
}

func TestTextViewMatchesTheRenderedLog(t *testing.T) {
	w, path := openTest(t, Options{})
	writeMixedLog(t, w, 3)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	want := fullText(t, path)
	v, err := OpenTextView(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = v.Close() }()
	if v.Size() != uint64(len(want)) {
		t.Fatalf("size = %d, want %d", v.Size(), len(want))
	}
	for _, chunk := range []int{1 << 10, 64 << 10, 1 << 20} {
		if got := readAllFrom(t, v, 0, chunk); got != want {
			t.Fatalf("sequential read with %d byte chunks differs (got %d bytes, want %d)", chunk, len(got), len(want))
		}
	}
	// Random offsets, including jumps backward and far forward, which miss
	// the cursor and go through the binary search.
	rng := rand.New(rand.NewSource(1))
	buf := make([]byte, 4096)
	for i := 0; i < 500; i++ {
		off := rng.Intn(len(want) + 10)
		n, err := v.ReadAt(buf, int64(off))
		if off >= len(want) {
			if n != 0 || !errors.Is(err, io.EOF) {
				t.Fatalf("ReadAt(%d) past end = %d, %v", off, n, err)
			}
			continue
		}
		end := min(off+len(buf), len(want))
		if string(buf[:n]) != want[off:end] {
			t.Fatalf("ReadAt(%d) = %q, want %q", off, buf[:n], want[off:end])
		}
		if (end == len(want)) != errors.Is(err, io.EOF) {
			t.Fatalf("ReadAt(%d) err = %v at end=%t", off, err, end == len(want))
		}
	}
	for _, lines := range []int{0, 1, 2, 5, 100, 5000, 1 << 30} {
		got, err := v.TailOffset(lines)
		if err != nil || got != referenceTail(want, lines) {
			t.Fatalf("TailOffset(%d) = %d, %v; want %d", lines, got, err, referenceTail(want, lines))
		}
	}
}

func TestTextViewFollowsAGrowingLogAndIgnoresATornTail(t *testing.T) {
	w, path := openTest(t, Options{})
	if err := w.AppendOutput("stdout", "one\n"); err != nil {
		t.Fatal(err)
	}
	v, err := OpenTextView(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = v.Close() }()
	if got := readAllFrom(t, v, 0, 64); got != "one\n" {
		t.Fatalf("first read = %q", got)
	}
	if err := w.AppendOutput("stdout", "two\n"); err != nil {
		t.Fatal(err)
	}
	if got := readAllFrom(t, v, 4, 64); got != "" {
		t.Fatalf("read before refresh = %q", got)
	}
	if err := v.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := readAllFrom(t, v, 4, 64); got != "two\n" || v.Size() != 8 {
		t.Fatalf("read after refresh = %q, size %d", got, v.Size())
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString(`{"seq":3,"text_offset":8,"type":"output","data":"torn`)
	_ = f.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if err := v.Refresh(); err != nil || v.Size() != 8 {
		t.Fatalf("refresh over torn tail: size %d, %v", v.Size(), err)
	}
	if got, err := v.TailOffset(1); err != nil || got != 4 {
		t.Fatalf("TailOffset(1) = %d, %v", got, err)
	}
}

func TestTextViewSeesOnlyTheCheckpointedPrefix(t *testing.T) {
	w, path := openTest(t, Options{})
	if err := w.AppendOutput("stdout", "committed\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Checkpoint(1, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendOutput("stdout", "pending\n"); err != nil {
		t.Fatal(err)
	}
	v, err := OpenTextView(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = v.Close() }()
	if got := readAllFrom(t, v, 0, 64); got != "committed\n" {
		t.Fatalf("text view = %q", got)
	}
}

func TestTextViewSkipsUnreadableLines(t *testing.T) {
	w, path := openTest(t, Options{})
	if err := w.AppendOutput("stdout", "before\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString("not json\n" + strings.Repeat("x", int(maxLogLineBytes)+1) + "\n")
	_ = f.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	w, err = Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AppendOutput("stdout", "after\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	v, err := OpenTextView(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = v.Close() }()
	const want = "before\nafter\n"
	if got := readAllFrom(t, v, 0, 64); got != want || v.Size() != uint64(len(want)) {
		t.Fatalf("text view = %q, size %d", got, v.Size())
	}
	buf := make([]byte, 6)
	if n, _ := v.ReadAt(buf, 7); string(buf[:n]) != "after\n" {
		t.Fatalf("ReadAt past the unreadable lines = %q", buf[:n])
	}
	if got, err := v.TailOffset(1); err != nil || got != 7 {
		t.Fatalf("TailOffset(1) = %d, %v", got, err)
	}
}

func TestReadEntryBatchWalksTheLogInBatches(t *testing.T) {
	w, path := openTest(t, Options{})
	for i := 0; i < 25; i++ {
		if err := w.AppendOutput("stdout", fmt.Sprintf("%d\n", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var seqs []uint64
	var from int64
	var after uint64 = 3
	for {
		batch, next, err := ReadEntryBatch(path, from, after, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch) > 10 {
			t.Fatalf("batch of %d", len(batch))
		}
		from = next
		if len(batch) == 0 {
			break
		}
		for _, e := range batch {
			seqs = append(seqs, e.Seq)
		}
		after = batch[len(batch)-1].Seq
	}
	if len(seqs) != 22 || seqs[0] != 4 || seqs[len(seqs)-1] != 25 {
		t.Fatalf("seqs = %v", seqs)
	}
}

func TestSummarizeLogMatchesSummarize(t *testing.T) {
	w, path := openTest(t, Options{})
	writeMixedLog(t, w, 2)
	mustEvent(t, w, `{"kind":"error","message":"boom"}`)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadEntries(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := SummarizeLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := Summarize(entries); got != want || got.Turns != 2 || got.ToolCalls != 40 || got.LastError != "boom" {
		t.Fatalf("SummarizeLog = %+v, Summarize = %+v", got, want)
	}
}

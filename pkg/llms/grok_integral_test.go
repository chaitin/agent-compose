package llms

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
)

func TestWholeIntegralArgsDropsOnlyZeroFractions(t *testing.T) {
	got := WholeIntegralArgs(`{"cmd":"go test","timeout_ms":66121.0,"yield_time_ms":2500.00,"max_output_tokens":4000.0,"ratio":1.5,"note":"keep 30000.0","exp":2.5e3}`)
	want := `{"cmd":"go test","timeout_ms":66121,"yield_time_ms":2500,"max_output_tokens":4000,"ratio":1.5,"note":"keep 30000.0","exp":2.5e3}`
	if got != want {
		t.Fatalf("rewritten = %s, want %s", got, want)
	}
	if got := WholeIntegralArgs(`{"timeout_ms":66121}`); got != `{"timeout_ms":66121}` {
		t.Fatalf("integer arguments changed: %s", got)
	}
}

func TestRewriteGrokIntegralToolArgsRewritesFunctionCallOnly(t *testing.T) {
	body := []byte(`{"object":"response","model":"grok-4.7","output":[{"type":"function_call","name":"exec_command","arguments":"{\"timeout_ms\":66121.0,\"limit\":1.5}"},{"type":"message","content":[{"text":"30000.0"}]}]}`)
	got := string(RewriteGrokIntegralToolArgs("grok-4.7", body))
	if !strings.Contains(got, `\"timeout_ms\":66121`) || strings.Contains(got, `66121.0`) {
		t.Fatalf("function call arguments were not rewritten: %s", got)
	}
	if !strings.Contains(got, `\"limit\":1.5`) || !strings.Contains(got, "30000.0") {
		t.Fatalf("non-integral or non-argument numbers changed: %s", got)
	}
	if other := RewriteGrokIntegralToolArgs("gpt-5.5", body); !bytes.Equal(other, body) {
		t.Fatalf("non-grok body changed: %s", other)
	}
}

func TestCopyRuntimeResponseBodyForModelRewritesSplitSSEDelta(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"timeout_ms\":66121."}`,
		"",
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"0,\"ratio\":1.5}"}`,
		"",
		`data: {"type":"response.output_item.done","item":{"type":"function_call","arguments":"{\"timeout_ms\":66121.0,\"ratio\":1.5}"}}`,
		"",
		`data: {"type":"response.function_call_arguments.done","arguments":"{\"session_id\":96230.0}"}`,
		"",
		"",
	}, "\n")
	var dst flushBuffer
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   io.NopCloser(strings.NewReader(stream)),
	}
	if err := CopyRuntimeResponseBodyForModel(&dst, resp, "x-ai/grok-4.7"); err != nil {
		t.Fatalf("copy stream: %v", err)
	}
	got := dst.String()
	for _, want := range []string{`"delta":"{\"timeout_ms\":66121"`, `"delta":",\"ratio\":1.5}"`, `\"timeout_ms\":66121`, `\"session_id\":96230`} {
		if !strings.Contains(got, want) {
			t.Fatalf("stream missing %s\n%s", want, got)
		}
	}
	if strings.Contains(got, "66121.0") || strings.Contains(got, "96230.0") || !strings.Contains(got, "1.5") {
		t.Fatalf("stream rewrite changed the wrong numbers:\n%s", got)
	}
	var other flushBuffer
	if err := CopyRuntimeResponseBodyForModel(&other, &http.Response{
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   io.NopCloser(strings.NewReader(stream)),
	}, "gpt-5.5"); err != nil || other.String() != stream {
		t.Fatalf("non-grok stream changed: %q err=%v", other.String(), err)
	}
}

func TestGrokResponsesStreamMatchesTheWholeBodyRewrite(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"timeout_ms\":66121."}`,
		"",
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"0,\"ratio\":1.5}"}`,
		"",
		`data: {"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{\"timeout_ms\":66121.0,\"ratio\":1.5}"}`,
		"",
		"",
	}, "\n")
	got, err := io.ReadAll(GrokResponsesStream(iotest.OneByteReader(strings.NewReader(stream))))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var copied bytes.Buffer
	resp := &http.Response{Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}
	if err := CopyRuntimeResponseBodyForModel(&copied, resp, "grok-4.7"); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if string(got) != copied.String() {
		t.Fatalf("reader = %q\ncopy   = %q", got, copied.String())
	}
	if strings.Contains(string(got), "66121.0") || strings.Contains(string(got), `66121.\"`) {
		t.Fatalf("zero fraction survived: %s", got)
	}
}

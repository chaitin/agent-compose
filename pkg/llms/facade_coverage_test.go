package llms

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
)

func TestRuntimeFacadeHTTPAndBridgeCoverage(t *testing.T) {
	header := http.Header{
		"Authorization":   []string{"Bearer runtime-token"},
		"X-Forward-Token": []string{"secret"},
		"X-Trace":         []string{"trace"},
		"Content-Length":  []string{"10"},
	}
	if BearerToken("Bearer abc") != "abc" || RuntimeFacadeToken(header) != "runtime-token" {
		t.Fatalf("token parsing failed")
	}
	dst := http.Header{}
	CopyRuntimeHeaders(dst, header)
	if dst.Get("X-Trace") != "trace" || dst.Get("Authorization") != "" || dst.Get("X-Forward-Token") != "" {
		t.Fatalf("copied request headers = %#v", dst)
	}
	respHeaders := http.Header{"Content-Type": []string{"text/event-stream"}, "Content-Encoding": []string{"gzip"}, "X-Upstream": []string{"ok"}}
	respDst := http.Header{}
	CopyRuntimeResponseHeaders(respDst, respHeaders)
	if respDst.Get("X-Upstream") != "ok" || respDst.Get("Content-Encoding") != "" || !RuntimeResponseShouldFlush(respHeaders) {
		t.Fatalf("copied response headers = %#v", respDst)
	}
	if !ForbiddenRuntimeHeader("x.api-key") || !ForbiddenRuntimeHeader("authorization") || ForbiddenRuntimeHeader("x-trace") {
		t.Fatalf("ForbiddenRuntimeHeader returned unexpected values")
	}
	if !ForbiddenRuntimeResponseHeader("content-length") || ForbiddenRuntimeResponseHeader("x-ok") {
		t.Fatalf("ForbiddenRuntimeResponseHeader returned unexpected values")
	}
	var copied bytes.Buffer
	if err := CopyRuntimeResponseBody(&copied, &http.Response{Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader("body"))}); err != nil || copied.String() != "body" {
		t.Fatalf("CopyRuntimeResponseBody copied=%q err=%v", copied.String(), err)
	}
	var events []protocolbridge.RawStreamEvent
	if err := ReadRawSSEEvents(strings.NewReader(": comment\nid: 1\nevent: delta\nretry: 100\ndata: hello\ndata: world\n\n"), func(event protocolbridge.RawStreamEvent) error {
		events = append(events, event)
		return nil
	}); err != nil || len(events) != 1 || string(events[0].Data) != "hello\nworld" {
		t.Fatalf("ReadRawSSEEvents events=%#v err=%v", events, err)
	}
	var eofEvent []protocolbridge.RawStreamEvent
	if err := ReadRawSSEEvents(strings.NewReader("data\nretry: nope\ndata: tail"), func(event protocolbridge.RawStreamEvent) error {
		eofEvent = append(eofEvent, event)
		return nil
	}); err != nil || len(eofEvent) != 1 || string(eofEvent[0].Data) != "\ntail" || eofEvent[0].Retry != nil {
		t.Fatalf("ReadRawSSEEvents eof events=%#v err=%v", eofEvent, err)
	}
	var sseOut bytes.Buffer
	retry := 250
	if err := WriteRawSSEEvent(&sseOut, protocolbridge.RawStreamEvent{ID: "id-1", Event: "delta", Data: []byte("one\ntwo"), Retry: &retry}); err != nil {
		t.Fatalf("WriteRawSSEEvent returned error: %v", err)
	}
	if got := sseOut.String(); !strings.Contains(got, "id: id-1") || !strings.Contains(got, "event: delta") || !strings.Contains(got, "retry: 250") || !strings.Contains(got, "data: two") {
		t.Fatalf("sse output = %q", got)
	}
	flushed := &flushBuffer{}
	if err := CopyRuntimeResponseBody(flushed, &http.Response{Header: respHeaders, Body: io.NopCloser(strings.NewReader("stream-body"))}); err != nil || flushed.String() != "stream-body" || flushed.flushes == 0 {
		t.Fatalf("flushed body=%q flushes=%d err=%v", flushed.String(), flushed.flushes, err)
	}
	if err := CopyRuntimeResponseBody(&copied, nil); err != nil {
		t.Fatalf("CopyRuntimeResponseBody nil response returned error: %v", err)
	}

	target := ResolvedTarget{Provider: Provider{ID: "provider", ProviderType: ProviderFamilyOpenAI, UseGenericResponsesTextParts: true}, Model: Model{Name: "gpt-override"}, WireAPI: APIProtocolResponses}
	rewritten, err := RewriteRuntimeRequestForUpstream([]byte(`{"model":"old","input":[{"role":"developer","content":[{"type":"input_text","text":"hi"}]}]}`), target, protocolbridge.ProtocolOpenAIResponses)
	if err != nil || !strings.Contains(string(rewritten), "gpt-override") || !strings.Contains(string(rewritten), `"type":"text"`) {
		t.Fatalf("RewriteRuntimeRequestForUpstream body=%s err=%v", rewritten, err)
	}
	chatBody, err := RewriteRuntimeRequestForUpstream([]byte(`{"model":"old","messages":[{"role":"developer","content":"hi"}]}`), ResolvedTarget{Model: Model{Name: "gpt"}}, protocolbridge.ProtocolOpenAIChat)
	if err != nil || !strings.Contains(string(chatBody), `"role":"system"`) {
		t.Fatalf("chat rewrite body=%s err=%v", chatBody, err)
	}
	if _, err := RewriteRuntimeRequestForUpstream([]byte(`{bad`), target, protocolbridge.ProtocolOpenAIResponses); err == nil {
		t.Fatalf("expected rewrite JSON error")
	}
	defaultTypeBody, err := RewriteRuntimeRequestForUpstream([]byte(`{"input":[{"content":[{"text":"hi"}]}]}`), ResolvedTarget{}, protocolbridge.ProtocolOpenAIResponses)
	if err != nil || !strings.Contains(string(defaultTypeBody), `"type":"input_text"`) {
		t.Fatalf("default responses text type body=%s err=%v", defaultTypeBody, err)
	}
	if normalizeRuntimeRawResponsesInput(map[string]json.RawMessage{"input": json.RawMessage(`{"bad":true}`)}, false) {
		t.Fatalf("invalid responses input changed")
	}
	if normalizeRuntimeRawRoleItems(map[string]json.RawMessage{"messages": json.RawMessage(`{"bad":true}`)}, "messages") {
		t.Fatalf("invalid role items changed")
	}
	req := &protocolbridge.LLMRequest{Model: "old", Prompt: []protocolbridge.Message{{Role: protocolbridge.RoleDeveloper, Parts: []protocolbridge.Part{{Text: &protocolbridge.TextPart{Text: "hi"}}}}}}
	if encoded, err := EncodeRuntimeUpstreamRequest(protocolbridge.ProtocolOpenAIResponses, protocolbridge.ProtocolOpenAIChat, target, req); err != nil || !strings.Contains(string(encoded), "gpt-override") {
		t.Fatalf("EncodeRuntimeUpstreamRequest body=%s err=%v", encoded, err)
	}
	clientBody, err := EncodeRuntimeClientResponse(protocolbridge.ProtocolOpenAIResponses, protocolbridge.ProtocolOpenAIChat, target, []byte(`{"id":"chatcmpl","model":"old","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	if err != nil || !strings.Contains(string(clientBody), "gpt-override") || !strings.Contains(string(clientBody), "hi") {
		t.Fatalf("EncodeRuntimeClientResponse body=%s err=%v", clientBody, err)
	}
	if _, err := EncodeRuntimeClientResponse(protocolbridge.ProtocolOpenAIResponses, "bad", target, []byte(`{}`)); err == nil {
		t.Fatalf("expected unsupported client response protocol error")
	}
	if _, _, err := RuntimeStreamBridge(protocolbridge.ProtocolOpenAIResponses, protocolbridge.ProtocolOpenAIResponses, "gpt"); err != nil {
		t.Fatalf("RuntimeStreamBridge same protocol returned error: %v", err)
	}
	if _, _, err := RuntimeStreamBridge(protocolbridge.ProtocolOpenAIResponses, protocolbridge.ProtocolOpenAIChat, "gpt"); err != nil {
		t.Fatalf("RuntimeStreamBridge shared family returned error: %v", err)
	}
	if _, _, err := RuntimeStreamBridge(protocolbridge.ProtocolOpenAIResponses, protocolbridge.Protocol("bad"), "gpt"); err == nil {
		t.Fatalf("expected unsupported stream bridge error")
	}
	if !ProtocolsShareFamily(protocolbridge.ProtocolOpenAIResponses, protocolbridge.ProtocolOpenAIChat) || ProtocolFamily("bad") != "" {
		t.Fatalf("protocol family helpers failed")
	}
}

type flushBuffer struct {
	bytes.Buffer
	flushes int
}

func (b *flushBuffer) Flush() {
	b.flushes++
}

func TestIntegrationRuntimeFacadeHTTPAndBridgeCoverage(t *testing.T) {
	TestRuntimeFacadeHTTPAndBridgeCoverage(t)
	TestWriteCodexAndOpenCodeMCPConfigPushToGuest(t)
	TestWriteCodexMCPConfigClearsGuestWhenAllServersRemoved(t)
	TestWriteCodexMCPConfigSkipsGuestPushWhenNothingWasEverWritten(t)
	TestWriteCodexMCPConfigRetriesGuestClearAfterTransientPushFailure(t)
}

func TestE2ERuntimeFacadeHTTPAndBridgeCoverage(t *testing.T) {
	TestRuntimeFacadeHTTPAndBridgeCoverage(t)
	TestWriteCodexAndOpenCodeMCPConfigPushToGuest(t)
	TestWriteCodexMCPConfigClearsGuestWhenAllServersRemoved(t)
	TestWriteCodexMCPConfigSkipsGuestPushWhenNothingWasEverWritten(t)
	TestWriteCodexMCPConfigRetriesGuestClearAfterTransientPushFailure(t)
}

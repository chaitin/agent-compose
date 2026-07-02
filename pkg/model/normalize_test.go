package model

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeSessionEnvItems(t *testing.T) {
	items := []SessionEnvVar{
		{Name: " B ", Value: "first", Secret: true},
		{Name: "A", Value: "one"},
		{Name: "B", Value: "second"},
		{Name: "path", Value: "lower"},
		{Name: " ", Value: "skip"},
		{Name: "\t", Value: "skip"},
	}
	got := NormalizeSessionEnvItems(items)
	want := []SessionEnvVar{
		{Name: "A", Value: "one"},
		{Name: "B", Value: "second"},
		{Name: "path", Value: "lower"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeSessionEnvItems() = %#v, want %#v", got, want)
	}
	if got := NormalizeSessionEnvItems(nil); got != nil {
		t.Fatalf("NormalizeSessionEnvItems(nil) = %#v, want nil", got)
	}
	if got := NormalizeSessionEnvItems([]SessionEnvVar{{Name: " "}}); got != nil {
		t.Fatalf("NormalizeSessionEnvItems(blank) = %#v, want nil", got)
	}
}

func TestMergeSessionEnvItems(t *testing.T) {
	globalItems := []SessionEnvVar{
		{Name: "A", Value: "global-a", Secret: true},
		{Name: "C", Value: "global-c"},
		{Name: "PATH", Value: "upper"},
	}
	sessionItems := []SessionEnvVar{
		{Name: " A ", Value: "session-a"},
		{Name: "B", Value: "session-b"},
		{Name: "path", Value: "lower"},
	}
	got := MergeSessionEnvItems(globalItems, sessionItems)
	want := []SessionEnvVar{
		{Name: "A", Value: "session-a"},
		{Name: "B", Value: "session-b"},
		{Name: "C", Value: "global-c"},
		{Name: "PATH", Value: "upper"},
		{Name: "path", Value: "lower"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeSessionEnvItems() = %#v, want %#v", got, want)
	}
	if got := MergeSessionEnvItems(nil, nil); got != nil {
		t.Fatalf("MergeSessionEnvItems(nil, nil) = %#v, want nil", got)
	}
}

func TestNormalizeAgentProvider(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: " ", want: ""},
		{name: "codex", input: " CODEX ", want: "codex"},
		{name: "claude hyphen alias", input: "claude-code", want: "claude"},
		{name: "claude underscore alias", input: "CLAUDE_CODE", want: "claude"},
		{name: "gemini hyphen alias", input: "gemini-cli", want: "gemini"},
		{name: "gemini underscore alias", input: "GEMINI_CLI", want: "gemini"},
		{name: "opencode hyphen alias", input: "open-code", want: "opencode"},
		{name: "opencode underscore alias", input: "OPEN_CODE", want: "opencode"},
		{name: "unknown preserved", input: " Custom.Provider ", want: "custom.provider"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeAgentProvider(tt.input); got != tt.want {
				t.Fatalf("NormalizeAgentProvider(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateTopicEventName(t *testing.T) {
	valid := []string{"webhook.github.push", "runtime_job-1", "agent-compose.session.created", " A_1 "}
	for _, topic := range valid {
		if err := ValidateTopicEventName(topic); err != nil {
			t.Fatalf("ValidateTopicEventName(%q) returned error: %v", topic, err)
		}
	}

	tooLong := strings.Repeat("a", 129)
	invalid := []string{"", " ", "webhook/github", "bad topic", "bad:topic", tooLong}
	for _, topic := range invalid {
		if err := ValidateTopicEventName(topic); err == nil {
			t.Fatalf("ValidateTopicEventName(%q) returned nil error", topic)
		}
	}
}

func TestTopicEventPayloadSHA256(t *testing.T) {
	got := TopicEventPayloadSHA256(`{"ok":true}`)
	want := "sha256:4062edaf750fb8074e7e83e0c9028c94e32468a8b6f1614774328ef045150f93"
	if got != want {
		t.Fatalf("TopicEventPayloadSHA256() = %q, want %q", got, want)
	}
	if got == TopicEventPayloadSHA256(`{"ok":false}`) {
		t.Fatalf("TopicEventPayloadSHA256 returned same hash for different payloads")
	}
	if TopicEventPayloadSHA256(`{"ok":true}`) == TopicEventPayloadSHA256(`{ "ok" : true }`) {
		t.Fatalf("TopicEventPayloadSHA256 should hash the raw stored payload string")
	}
}

func TestNormalizeTopicEventSource(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: " WEBHOOK ", want: TopicEventSourceWebhook},
		{input: "LOADER", want: TopicEventSourceLoader},
		{input: "system", want: TopicEventSourceSystem},
		{input: "", want: ""},
		{input: "bad", want: ""},
	}
	for _, tt := range tests {
		if got := NormalizeTopicEventSource(tt.input); got != tt.want {
			t.Fatalf("NormalizeTopicEventSource(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeTopicEventDispatchStatus(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "", want: TopicEventDispatchPending},
		{input: " ", want: TopicEventDispatchPending},
		{input: " PENDING ", want: TopicEventDispatchPending},
		{input: "PUBLISHING_TO_BUS", want: TopicEventDispatchPublishing},
		{input: "published_to_bus", want: TopicEventDispatchPublishedToBus},
		{input: "NO_SUBSCRIBER", want: TopicEventDispatchNoSubscriber},
		{input: "retrying", want: TopicEventDispatchRetrying},
		{input: "DEAD_LETTER", want: TopicEventDispatchDeadLetter},
		{input: "bad", want: ""},
	}
	for _, tt := range tests {
		if got := NormalizeTopicEventDispatchStatus(tt.input); got != tt.want {
			t.Fatalf("NormalizeTopicEventDispatchStatus(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeEventDeliveryStatus(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: " MATCHED ", want: EventDeliveryStatusMatched},
		{input: "run_started", want: EventDeliveryStatusRunStarted},
		{input: "RUN_SUCCEEDED", want: EventDeliveryStatusRunSucceeded},
		{input: "run_failed", want: EventDeliveryStatusRunFailed},
		{input: "SKIPPED", want: EventDeliveryStatusSkipped},
		{input: "", want: ""},
		{input: "bad", want: ""},
	}
	for _, tt := range tests {
		if got := NormalizeEventDeliveryStatus(tt.input); got != tt.want {
			t.Fatalf("NormalizeEventDeliveryStatus(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

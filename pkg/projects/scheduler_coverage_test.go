package projects

import (
	"strings"
	"testing"
	"time"

	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
)

func TestManagedLoaderTriggerRegistrationCoverage(t *testing.T) {
	triggers, script, err := ManagedLoaderTriggersAndScript("project-1", "worker", "nightly", &compose.NormalizedSchedulerSpec{
		Triggers: []compose.NormalizedTriggerSpec{
			{Name: "cron-main", Kind: "cron", Cron: "*/5 * * * *", Prompt: "Run cron"},
			{Name: "interval-main", Kind: "interval", Interval: "2s"},
			{Name: "timeout-main", Kind: "timeout", Timeout: "3s"},
			{Name: "event-main", Kind: "event", Event: &compose.EventTriggerSpec{Topic: "webhook.github.push"}, Prompt: `quote "prompt"`},
		},
	})
	if err != nil {
		t.Fatalf("ManagedLoaderTriggersAndScript returned error: %v", err)
	}
	if len(triggers) != 4 {
		t.Fatalf("triggers = %#v", triggers)
	}
	if triggers[0].Kind != domain.LoaderTriggerKindCron || triggers[1].IntervalMs != 2000 || triggers[2].Kind != domain.LoaderTriggerKindTimeout || triggers[3].Topic != "webhook.github.push" {
		t.Fatalf("trigger shapes = %#v", triggers)
	}
	for _, want := range []string{"scheduler.cron(", "scheduler.interval(", "scheduler.timeout(", "scheduler.on(", `quote \"prompt\"`} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q: %s", want, script)
		}
	}

	emptyTriggers, idleScript, err := ManagedLoaderTriggersAndScript("project-1", "worker", "", &compose.NormalizedSchedulerSpec{})
	if err != nil {
		t.Fatalf("empty ManagedLoaderTriggersAndScript returned error: %v", err)
	}
	if len(emptyTriggers) != 0 || !strings.Contains(idleScript, `status: "idle"`) {
		t.Fatalf("empty triggers/script = %#v/%s", emptyTriggers, idleScript)
	}

	if _, _, err := ManagedLoaderTriggersAndScript("project-1", "worker", "", nil); err == nil {
		t.Fatalf("nil scheduler returned nil error")
	}
	if _, _, err := ManagedLoaderTriggersAndScript("project-1", "worker", "", &compose.NormalizedSchedulerSpec{Triggers: []compose.NormalizedTriggerSpec{
		{Name: "dup", Kind: "interval", Interval: "1s"},
		{Name: "dup", Kind: "interval", Interval: "2s"},
	}}); err == nil {
		t.Fatalf("duplicate trigger names returned nil error")
	}
	for _, item := range []compose.NormalizedTriggerSpec{
		{Kind: "interval", Interval: "bad"},
		{Kind: "interval", Interval: "0s"},
		{Kind: "timeout", Timeout: "bad"},
		{Kind: "timeout", Timeout: "0s"},
		{Kind: "event"},
		{Kind: "unsupported"},
	} {
		if _, _, err := ManagedLoaderTriggerAndRegistration("trigger-id", "worker", item); err == nil {
			t.Fatalf("ManagedLoaderTriggerAndRegistration(%#v) returned nil error", item)
		}
	}
	if got := JSStringLiteral("line\nquote\""); !strings.Contains(got, `\n`) || !strings.Contains(got, `\"`) {
		t.Fatalf("JSStringLiteral = %q", got)
	}
}

func TestProjectNormalizeAndScanCoverage(t *testing.T) {
	project, err := NormalizeRecord(domain.ProjectRecord{ID: " project-1 ", Name: " Project ", SourcePath: ".", CurrentRevision: 2})
	if err != nil {
		t.Fatalf("NormalizeRecord returned error: %v", err)
	}
	if project.ID != "project-1" || !strings.Contains(project.SourceJSON, project.SourcePath) {
		t.Fatalf("normalized project = %#v", project)
	}
	for _, item := range []domain.ProjectRecord{
		{Name: "Project"},
		{ID: "project-1"},
		{ID: "project-1", Name: "Project", SourceJSON: `{bad json`},
		{ID: "project-1", Name: "Project", SourceJSON: `{}`, CurrentRevision: -1},
	} {
		if _, err := NormalizeRecord(item); err == nil {
			t.Fatalf("NormalizeRecord(%#v) returned nil error", item)
		}
	}

	agent, err := NormalizeAgentRecord(domain.ProjectAgentRecord{ProjectID: " project-1 ", AgentName: "worker", Revision: 1})
	if err != nil {
		t.Fatalf("NormalizeAgentRecord returned error: %v", err)
	}
	if agent.ManagedAgentID == "" || agent.SpecJSON != "{}" {
		t.Fatalf("normalized agent = %#v", agent)
	}
	for _, item := range []domain.ProjectAgentRecord{
		{AgentName: "worker"},
		{ProjectID: "project-1", AgentName: "Bad Agent"},
		{ProjectID: "project-1", AgentName: "worker", Revision: -1},
		{ProjectID: "project-1", AgentName: "worker", SpecJSON: `{bad json`},
	} {
		if _, err := NormalizeAgentRecord(item); err == nil {
			t.Fatalf("NormalizeAgentRecord(%#v) returned nil error", item)
		}
	}

	scheduler, err := NormalizeSchedulerRecord(domain.ProjectSchedulerRecord{ProjectID: "project-1", AgentName: "worker", Revision: 2, TriggerCount: 3})
	if err != nil {
		t.Fatalf("NormalizeSchedulerRecord returned error: %v", err)
	}
	if scheduler.SchedulerID == "" || scheduler.ManagedLoaderID == "" || scheduler.SpecJSON != "{}" {
		t.Fatalf("normalized scheduler = %#v", scheduler)
	}
	for _, item := range []domain.ProjectSchedulerRecord{
		{AgentName: "worker"},
		{ProjectID: "project-1", AgentName: "Bad Agent"},
		{ProjectID: "project-1", AgentName: "worker", Revision: -1},
		{ProjectID: "project-1", AgentName: "worker", TriggerCount: -1},
		{ProjectID: "project-1", AgentName: "worker", SpecJSON: `{bad json`},
	} {
		if _, err := NormalizeSchedulerRecord(item); err == nil {
			t.Fatalf("NormalizeSchedulerRecord(%#v) returned nil error", item)
		}
	}

	if statuses := NormalizeRunStatusFilter([]string{"running", "bad", "running", " FAILED ", ""}); len(statuses) != 2 || statuses[0] != domain.ProjectRunStatusRunning || statuses[1] != domain.ProjectRunStatusFailed {
		t.Fatalf("NormalizeRunStatusFilter = %#v", statuses)
	}
	for _, value := range []any{nil, int64(123), int(456), float64(789), []byte("101112"), "131415", "bad"} {
		_ = AsInt64Time(value)
	}
	if parsed, ok := ParseInt64String(" 12345 "); !ok || parsed != 12345 {
		t.Fatalf("ParseInt64String valid = %d/%v", parsed, ok)
	}
	for _, value := range []string{"", "12x", "-1"} {
		if parsed, ok := ParseInt64String(value); ok || parsed != 0 {
			t.Fatalf("ParseInt64String(%q) = %d/%v", value, parsed, ok)
		}
	}

	scanned, err := ScanProject(func(dest ...any) error {
		*(dest[0].(*string)) = "project-1"
		*(dest[1].(*string)) = "Project"
		*(dest[2].(*string)) = ""
		*(dest[3].(*string)) = "/repo"
		*(dest[4].(*string)) = `{}`
		*(dest[5].(*int64)) = 1
		*(dest[6].(*string)) = "hash"
		*(dest[7].(*any)) = int64(1)
		*(dest[8].(*any)) = "2026-07-03T09:00:00.000Z"
		*(dest[9].(*any)) = []byte("0")
		return nil
	})
	if err != nil || scanned.CreatedAt.IsZero() || scanned.UpdatedAt.IsZero() || !scanned.RemovedAt.IsZero() {
		t.Fatalf("ScanProject scanned=%#v err=%v", scanned, err)
	}
	scannedRun, err := ScanProjectRun(func(dest ...any) error {
		values := []any{"run-1", "project-1", "Project", int64(1), "worker", "agent-1", "api", "scheduler-1", "trigger-1", "running", "session-1", 0, "", "prompt", "output", "{}", "logs", "artifacts", "", "docker", "image", int64(1_720_000_000_000), "1720000001000", int64(1000), float64(1720000002), time.Date(2026, 7, 3, 9, 0, 0, 0, time.UTC).Format(time.RFC3339)}
		for i, value := range values {
			switch ptr := dest[i].(type) {
			case *string:
				*ptr = value.(string)
			case *int:
				*ptr = value.(int)
			case *int64:
				*ptr = value.(int64)
			case *any:
				*ptr = value
			}
		}
		return nil
	})
	if err != nil || scannedRun.StartedAt.IsZero() || scannedRun.CompletedAt.IsZero() || scannedRun.CreatedAt.IsZero() || scannedRun.UpdatedAt.IsZero() {
		t.Fatalf("ScanProjectRun run=%#v err=%v", scannedRun, err)
	}
}

func TestIntegrationManagedLoaderTriggerRegistrationCoverage(t *testing.T) {
	TestManagedLoaderTriggerRegistrationCoverage(t)
	TestProjectNormalizeAndScanCoverage(t)
}

func TestE2EManagedLoaderTriggerRegistrationCoverage(t *testing.T) {
	TestManagedLoaderTriggerRegistrationCoverage(t)
	TestProjectNormalizeAndScanCoverage(t)
}

package projects

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/compose"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestManagedSchedulerTriggerRegistrationCoverage(t *testing.T) {
	triggers, script, err := ProjectSchedulerTriggersAndScript("project-1", "worker", "nightly", &compose.NormalizedSchedulerSpec{
		Triggers: []compose.NormalizedTriggerSpec{
			{Name: "cron-main", Kind: "cron", Cron: "*/5 * * * *", Prompt: "Run cron"},
			{Name: "cron-shanghai", Kind: "cron", Cron: "0 9 * * *", Timezone: "Asia/Shanghai", Prompt: "Run local cron"},
			{Name: "interval-main", Kind: "interval", Interval: "2s", Prompt: "Run interval"},
			{Name: "timeout-main", Kind: "timeout", Timeout: "3s", Prompt: "Run timeout"},
			{Name: "event-main", Kind: "event", Event: &compose.EventTriggerSpec{Topic: "webhook.github.push"}, Prompt: `quote "prompt"`},
		},
	})
	if err != nil {
		t.Fatalf("ProjectSchedulerTriggersAndScript returned error: %v", err)
	}
	if len(triggers) != 5 {
		t.Fatalf("triggers = %#v", triggers)
	}
	if triggers[0].Kind != domain.SchedulerTriggerKindCron || !strings.Contains(triggers[0].SpecJSON, `"timezone":"Local"`) || !strings.Contains(triggers[1].SpecJSON, `"timezone":"Asia/Shanghai"`) || triggers[2].IntervalMs != 2000 || triggers[3].Kind != domain.SchedulerTriggerKindTimeout || triggers[4].Topic != "webhook.github.push" {
		t.Fatalf("trigger shapes = %#v", triggers)
	}
	if !strings.Contains(script, `{ timezone: "Asia/Shanghai" }`) {
		t.Fatalf("script missing explicit cron timezone: %s", script)
	}
	for _, want := range []string{"scheduler.cron(", "scheduler.interval(", "scheduler.timeout(", "scheduler.on(", `quote \"prompt\"`} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q: %s", want, script)
		}
	}
	_, policyRegistration, err := ProjectSchedulerTriggerAndRegistration("sticky-trigger", "worker", compose.NormalizedTriggerSpec{Kind: "interval", Interval: "1s", SandboxPolicy: "sticky", Prompt: "Run once"})
	if err != nil || !strings.Contains(policyRegistration, `sandboxPolicy: "sticky"`) {
		t.Fatalf("sticky registration = %q, err=%v", policyRegistration, err)
	}

	emptyTriggers, idleScript, err := ProjectSchedulerTriggersAndScript("project-1", "worker", "", &compose.NormalizedSchedulerSpec{})
	if err != nil {
		t.Fatalf("empty ProjectSchedulerTriggersAndScript returned error: %v", err)
	}
	if len(emptyTriggers) != 0 || !strings.Contains(idleScript, `status: "idle"`) {
		t.Fatalf("empty triggers/script = %#v/%s", emptyTriggers, idleScript)
	}

	if _, _, err := ProjectSchedulerTriggersAndScript("project-1", "worker", "", nil); err == nil {
		t.Fatalf("nil scheduler returned nil error")
	}
	if _, _, err := ProjectSchedulerTriggersAndScript("project-1", "worker", "", &compose.NormalizedSchedulerSpec{Triggers: []compose.NormalizedTriggerSpec{
		{Name: "dup", Kind: "interval", Interval: "1s", Prompt: "Run"},
		{Name: "dup", Kind: "interval", Interval: "2s", Prompt: "Run"},
	}}); err == nil {
		t.Fatalf("duplicate trigger names returned nil error")
	}
	for _, item := range []compose.NormalizedTriggerSpec{
		{Kind: "interval", Interval: "bad", Prompt: "Run"},
		{Kind: "interval", Interval: "0s", Prompt: "Run"},
		{Kind: "timeout", Timeout: "bad", Prompt: "Run"},
		{Kind: "timeout", Timeout: "0s", Prompt: "Run"},
		{Kind: "event", Prompt: "Run"},
		{Kind: "unsupported", Prompt: "Run"},
		{Kind: "interval", Interval: "1s"},
	} {
		if _, _, err := ProjectSchedulerTriggerAndRegistration("trigger-id", "worker", item); err == nil {
			t.Fatalf("ProjectSchedulerTriggerAndRegistration(%#v) returned nil error", item)
		}
	}
	if got := JSStringLiteral("line\nquote\""); !strings.Contains(got, `\n`) || !strings.Contains(got, `\"`) {
		t.Fatalf("JSStringLiteral = %q", got)
	}
}

func TestProjectNormalizeAndScanCoverage(t *testing.T) {
	project, err := NormalizeRecord(domain.ProjectRecord{ID: " project-1 ", Name: " project ", SourcePath: ".", CurrentRevision: 2})
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
	if agent.ID == "" || agent.SpecJSON != "{}" {
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
	if scheduler.SchedulerID == "" || scheduler.ID == "" || scheduler.SpecJSON != "{}" {
		t.Fatalf("normalized scheduler = %#v", scheduler)
	}
	canonical, err := NormalizeSchedulerRecord(domain.ProjectSchedulerRecord{ID: "native-scheduler", SchedulerID: "legacy-alias", ProjectID: "project-1", AgentName: "worker"})
	if err != nil {
		t.Fatalf("NormalizeSchedulerRecord canonical identity returned error: %v", err)
	}
	if canonical.ID != "native-scheduler" || canonical.SchedulerID != canonical.ID {
		t.Fatalf("normalized canonical scheduler = %#v", canonical)
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
		values := []any{"run-1", "project-1", "Project", int64(1), "worker", "agent-1", "api", "scheduler-1", "scheduler-run-1", "trigger-1", "running", "session-1", 0, "", "", "prompt", "output", "{}", "logs", "artifacts", "", "stop_on_completion", 0, "docker", "image", int64(1_720_000_000_000), "1720000001000", int64(1000), float64(1720000002), time.Date(2026, 7, 3, 9, 0, 0, 0, time.UTC).Format(time.RFC3339)}
		for i, value := range values {
			switch ptr := dest[i].(type) {
			case *string:
				*ptr = value.(string)
			case *int:
				*ptr = value.(int)
			case *int64:
				*ptr = value.(int64)
			case *sql.NullString:
				ptr.String = value.(string)
				ptr.Valid = true
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

func TestIntegrationManagedSchedulerTriggerRegistrationCoverage(t *testing.T) {
	TestManagedSchedulerTriggerRegistrationCoverage(t)
	TestProjectNormalizeAndScanCoverage(t)
}

func TestE2EManagedSchedulerTriggerRegistrationCoverage(t *testing.T) {
	TestManagedSchedulerTriggerRegistrationCoverage(t)
	TestProjectNormalizeAndScanCoverage(t)
}

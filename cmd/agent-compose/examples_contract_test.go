package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/schedulers"
)

type exampleContract struct {
	agentCount int
	schedulers int
}

func TestExampleFilesContract(t *testing.T) {
	root := repoRootForComposeEnvTest(t)
	examplesRoot := filepath.Join(root, "examples", "agent-compose")
	want := map[string]exampleContract{
		"docker-minimal":              {agentCount: 1},
		"docker-scheduler-cron":       {agentCount: 1, schedulers: 1},
		"docker-scheduler-script-url": {agentCount: 1, schedulers: 1},
		"docker-scheduler-timeout":    {agentCount: 1, schedulers: 1},
	}

	entries, err := os.ReadDir(examplesRoot)
	if err != nil {
		t.Fatalf("read examples directory: %v", err)
	}
	gotNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			gotNames = append(gotNames, entry.Name())
		}
	}
	sort.Strings(gotNames)
	wantNames := make([]string, 0, len(want))
	for name := range want {
		wantNames = append(wantNames, name)
	}
	sort.Strings(wantNames)
	if strings.Join(gotNames, "\n") != strings.Join(wantNames, "\n") {
		t.Fatalf("example directories = %v, want %v", gotNames, wantNames)
	}

	engine := &schedulers.QJSSchedulerEngine{}
	for _, name := range wantNames {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(examplesRoot, name)
			for _, readme := range []string{"README.md", "README.zh-CN.md"} {
				assertExampleFileExists(t, filepath.Join(dir, readme))
			}
			_, normalized, err := loadResolvedNormalizedCompose(context.Background(), cliOptions{
				ComposeFile: filepath.Join(dir, "agent-compose.yml"),
			})
			if err != nil {
				t.Fatalf("normalize example: %v", err)
			}
			contract := want[name]
			if normalized.Name != name || len(normalized.Agents) != contract.agentCount {
				t.Fatalf("normalized project name/agents = %q/%d, want %q/%d", normalized.Name, len(normalized.Agents), name, contract.agentCount)
			}
			schedulerCount := 0
			for _, agent := range normalized.Agents {
				if !agent.Enabled {
					t.Fatalf("agent %s is unexpectedly disabled", agent.Name)
				}
				if agent.Driver == nil || agent.Driver.Name != "docker" {
					t.Fatalf("agent %s driver = %#v, want docker", agent.Name, agent.Driver)
				}
				if agent.Scheduler == nil {
					continue
				}
				schedulerCount++
				if agent.Scheduler.SandboxPolicy != domain.SchedulerSandboxPolicyNew || agent.Scheduler.ConcurrencyPolicy != domain.SchedulerConcurrencyPolicySkip {
					t.Fatalf("agent %s scheduler policies = %q/%q, want new/skip", agent.Name, agent.Scheduler.SandboxPolicy, agent.Scheduler.ConcurrencyPolicy)
				}
				if strings.TrimSpace(agent.Scheduler.Script) != "" {
					if _, err := engine.Validate(context.Background(), domain.SchedulerRuntimeScheduler, agent.Scheduler.Script); err != nil {
						t.Fatalf("validate scheduler script: %v", err)
					}
				}
			}
			if schedulerCount != contract.schedulers {
				t.Fatalf("scheduler count = %d, want %d", schedulerCount, contract.schedulers)
			}
		})
	}

	validateStandaloneSchedulerExamples(t, root, engine)
	assertExampleDocsDoNotUseStaleContracts(t, root)
}

func validateStandaloneSchedulerExamples(t *testing.T, root string, engine *schedulers.QJSSchedulerEngine) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root, "examples", "scheduler-script", "*.js"))
	if err != nil {
		t.Fatalf("glob scheduler examples: %v", err)
	}
	if len(files) != 6 {
		t.Fatalf("standalone scheduler example count = %d, want 6", len(files))
	}
	for _, file := range files {
		script, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if _, err := engine.Validate(context.Background(), domain.SchedulerRuntimeScheduler, string(script)); err != nil {
			t.Fatalf("validate %s: %v", file, err)
		}
	}
}

func assertExampleDocsDoNotUseStaleContracts(t *testing.T, root string) {
	t.Helper()
	stale := []string{
		"exec --agent",
		"SCHEDULER  LATEST RUN",
		"RunLoaderNow",
		"project_scheduler",
		"agent_definition",
		"Loader 脚本模板",
	}
	err := filepath.WalkDir(filepath.Join(root, "examples"), func(file string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "README") || filepath.Ext(entry.Name()) != ".md" {
			return nil
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		for _, text := range stale {
			if strings.Contains(string(data), text) {
				return fmt.Errorf("%s contains stale CLI/API text %q", file, text)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertExampleFileExists(t *testing.T, path string) {
	t.Helper()
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		t.Fatalf("required example file %s is unavailable: %v", path, err)
	}
}

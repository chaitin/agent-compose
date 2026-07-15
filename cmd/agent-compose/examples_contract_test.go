package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"agent-compose/pkg/loaders"
)

type exampleContract struct {
	driver     string
	agentCount int
	schedulers int
}

func TestExampleFilesContract(t *testing.T) {
	root := repoRootForComposeEnvTest(t)
	examplesRoot := filepath.Join(root, "examples", "agent-compose")
	want := map[string]exampleContract{
		"boxlite-minimal":                 {driver: "boxlite", agentCount: 1},
		"docker-build":                    {driver: "docker", agentCount: 1},
		"docker-env-secrets":              {driver: "docker", agentCount: 1},
		"docker-minimal":                  {driver: "docker", agentCount: 1},
		"docker-multi-agent":              {driver: "docker", agentCount: 2},
		"docker-scheduler-cron":           {driver: "docker", agentCount: 1, schedulers: 1},
		"docker-scheduler-script-runtime": {driver: "docker", agentCount: 1, schedulers: 1},
		"docker-scheduler-script-url":     {driver: "docker", agentCount: 1, schedulers: 1},
		"docker-scheduler-timeout":        {driver: "docker", agentCount: 1, schedulers: 1},
		"docker-volume-persistence":       {driver: "docker", agentCount: 1},
		"docker-workspace-lifecycle":      {driver: "docker", agentCount: 1},
		"microsandbox-minimal":            {driver: "microsandbox", agentCount: 1},
	}

	entries, err := os.ReadDir(examplesRoot)
	if err != nil {
		t.Fatalf("read examples directory: %v", err)
	}
	var gotNames []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		gotNames = append(gotNames, entry.Name())
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

	engine := &loaders.QJSLoaderEngine{}
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
			schedulers := 0
			for _, agent := range normalized.Agents {
				if agent.Driver == nil || agent.Driver.Name != contract.driver {
					t.Fatalf("agent %s driver = %#v, want %s", agent.Name, agent.Driver, contract.driver)
				}
				if agent.Scheduler == nil {
					continue
				}
				schedulers++
				if agent.Scheduler.SandboxPolicy != "new" {
					t.Fatalf("agent %s scheduler sandbox policy = %q, want new", agent.Name, agent.Scheduler.SandboxPolicy)
				}
				if strings.TrimSpace(agent.Scheduler.Script) != "" {
					if _, err := engine.Validate(context.Background(), "scheduler", agent.Scheduler.Script); err != nil {
						t.Fatalf("validate scheduler script: %v", err)
					}
				}
			}
			if schedulers != contract.schedulers {
				t.Fatalf("scheduler count = %d, want %d", schedulers, contract.schedulers)
			}
			if name == "docker-env-secrets" {
				redacted, err := normalized.MarshalCanonicalYAML(true)
				if err != nil {
					t.Fatalf("marshal redacted config: %v", err)
				}
				if strings.Contains(string(redacted), "safe-example-secret") {
					t.Fatalf("redacted config leaked example secret: %s", redacted)
				}
			}
		})
	}

	validateStandaloneSchedulerExamples(t, root, engine)
	assertExampleDocsDoNotUseStaleCLI(t, root)
}

func validateStandaloneSchedulerExamples(t *testing.T, root string, engine *loaders.QJSLoaderEngine) {
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
		if _, err := engine.Validate(context.Background(), "scheduler", string(script)); err != nil {
			t.Fatalf("validate %s: %v", file, err)
		}
	}
}

func assertExampleDocsDoNotUseStaleCLI(t *testing.T, root string) {
	t.Helper()
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
		for _, stale := range []string{"exec --agent", "SCHEDULER  LATEST RUN", "RunLoaderNow"} {
			if strings.Contains(string(data), stale) {
				return fmt.Errorf("%s contains stale CLI/API text %q", file, stale)
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

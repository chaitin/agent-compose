package compose

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestInspectBundleValidatesManifestAndReferencedFiles(t *testing.T) {
	dir := t.TempDir()
	mustMkdirAll(t, filepath.Join(dir, "services"))
	mustMkdirAll(t, filepath.Join(dir, "schemas"))
	mustWriteFile(t, filepath.Join(dir, "services", "review.js"), []byte("export async function main() {}\n"))
	mustWriteFile(t, filepath.Join(dir, "schemas", "input.json"), []byte(`{"type":"object"}`))
	mustWriteFile(t, filepath.Join(dir, "agent-compose.yml"), []byte(`
name: valid-bundle
agents:
  reviewer:
    provider: codex
services:
  risk-review:
    entry: services/review.js
    input_schema: schemas/input.json
triggers:
  on-push:
    type: event
    event:
      topic: git.push
    target:
      service: risk-review
`))

	inspect, err := InspectBundle(dir)
	if err != nil {
		t.Fatalf("InspectBundle returned error: %v", err)
	}
	if inspect.Project != "valid-bundle" || inspect.AgentCount != 1 || inspect.ServiceCount != 1 || inspect.TriggerCount != 1 {
		t.Fatalf("inspect = %#v", inspect)
	}
	if inspect.Spec == nil || len(inspect.Spec.Services) != 1 {
		t.Fatalf("inspect spec = %#v", inspect.Spec)
	}
}

func TestInspectBundleResolvesRelativeFilesFromManifestDirectory(t *testing.T) {
	root := t.TempDir()
	bundleDir := filepath.Join(root, "bundle")
	otherDir := filepath.Join(root, "other")
	mustMkdirAll(t, filepath.Join(bundleDir, "services"))
	mustMkdirAll(t, filepath.Join(bundleDir, "schemas"))
	mustMkdirAll(t, filepath.Join(otherDir, "services"))
	mustMkdirAll(t, filepath.Join(otherDir, "schemas"))
	mustWriteFile(t, filepath.Join(bundleDir, "services", "review.js"), []byte("export async function main() {}\n"))
	mustWriteFile(t, filepath.Join(bundleDir, "schemas", "input.json"), []byte(`{"type":"object"}`))
	mustWriteFile(t, filepath.Join(otherDir, "services", "review.js"), []byte("export async function main() {}\n"))
	mustWriteFile(t, filepath.Join(otherDir, "schemas", "input.json"), []byte(`{"type":`))
	mustWriteFile(t, filepath.Join(bundleDir, "agent-compose.yml"), []byte(`
name: manifest-root-bundle
services:
  risk-review:
    entry: services/review.js
    input_schema: schemas/input.json
`))

	previous, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve current directory: %v", err)
	}
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})

	inspect, err := InspectBundle(bundleDir)
	if err != nil {
		t.Fatalf("InspectBundle returned error: %v", err)
	}
	if inspect.Project != "manifest-root-bundle" || inspect.ServiceCount != 1 {
		t.Fatalf("inspect = %#v", inspect)
	}
}

func TestInspectBundleValidatesServiceEntryExample(t *testing.T) {
	inspect, err := InspectBundle(filepath.Join("..", "..", "examples", "service-entry"))
	if err != nil {
		t.Fatalf("InspectBundle returned error: %v", err)
	}
	if inspect.Project != "service-entry-demo" || inspect.AgentCount != 1 || inspect.ServiceCount != 1 || inspect.TriggerCount != 1 {
		t.Fatalf("inspect = %#v", inspect)
	}
	if inspect.Spec == nil || len(inspect.Spec.Services) != 1 || inspect.Spec.Services[0].Name != "risk-review" {
		t.Fatalf("inspect spec = %#v", inspect.Spec)
	}
}

func TestInspectBundleValidatesAllExamples(t *testing.T) {
	examplesRoot := filepath.Join("..", "..", "examples")
	manifestDirSet := make(map[string]struct{})
	err := filepath.WalkDir(examplesRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		switch filepath.Base(path) {
		case "agent-compose.yml", "agent-compose.yaml", "agent-compose.json":
			manifestDirSet[filepath.Dir(path)] = struct{}{}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk examples returned error: %v", err)
	}
	manifestDirs := make([]string, 0, len(manifestDirSet))
	for dir := range manifestDirSet {
		manifestDirs = append(manifestDirs, dir)
	}
	slices.Sort(manifestDirs)
	if len(manifestDirs) < 4 {
		t.Fatalf("example bundle count = %d, want at least 4", len(manifestDirs))
	}
	for _, dir := range manifestDirs {
		rel, err := filepath.Rel(examplesRoot, dir)
		if err != nil {
			t.Fatalf("resolve example path %q: %v", dir, err)
		}
		t.Run(rel, func(t *testing.T) {
			if _, err := InspectBundle(dir); err != nil {
				t.Fatalf("InspectBundle(%s) returned error: %v", rel, err)
			}
		})
	}
}

func TestInspectBundleRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "agent-compose.yml"), []byte(`
name: unknown-bundle
unexpected: true
`))

	_, err := InspectBundle(dir)
	if err == nil {
		t.Fatalf("expected InspectBundle to fail")
	}
	if got := err.Error(); !strings.Contains(got, "unexpected") || !strings.Contains(got, "unknown field") {
		t.Fatalf("error = %q, want unknown field", got)
	}
}

func TestInspectBundleRejectsMissingServiceEntry(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "agent-compose.yml"), []byte(`
name: missing-service-entry
services:
  risk-review:
    description: no entry
`))

	_, err := InspectBundle(dir)
	if err == nil {
		t.Fatalf("expected InspectBundle to fail")
	}
	if got := err.Error(); !strings.Contains(got, "services.risk-review.entry") || !strings.Contains(got, "required") {
		t.Fatalf("error = %q, want missing entry", got)
	}
}

func TestInspectBundleRejectsInvalidSchemaFileJSON(t *testing.T) {
	dir := t.TempDir()
	mustMkdirAll(t, filepath.Join(dir, "services"))
	mustMkdirAll(t, filepath.Join(dir, "schemas"))
	mustWriteFile(t, filepath.Join(dir, "services", "review.js"), []byte("export async function main() {}\n"))
	mustWriteFile(t, filepath.Join(dir, "schemas", "input.json"), []byte(`{"type":`))
	mustWriteFile(t, filepath.Join(dir, "agent-compose.yml"), []byte(`
name: invalid-schema-bundle
services:
  risk-review:
    entry: services/review.js
    input_schema: schemas/input.json
`))

	_, err := InspectBundle(dir)
	if err == nil {
		t.Fatalf("expected InspectBundle to fail")
	}
	if got := err.Error(); !strings.Contains(got, "services.risk-review.input_schema") || !strings.Contains(got, "valid JSON") {
		t.Fatalf("error = %q, want invalid schema file JSON", got)
	}
}

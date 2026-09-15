package compose

import (
	"strings"
	"testing"
)

func TestNormalizeHTTPWorkspaceZIP(t *testing.T) {
	spec := mustParseCompose(t, `name: http-workspace
workspaces:
  demo:
    provider: http
    url: https://example.com/demo.zip
    format: zip
    path: project
    target: src
`)
	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	got := normalized.Workspaces["demo"]
	if got.Provider != "http" || got.Format != "zip" || got.Path != "project" || got.Target != "src" {
		t.Fatalf("workspace = %#v", got)
	}
}

func TestNormalizeHTTPWorkspaceRejectsMount(t *testing.T) {
	spec := mustParseCompose(t, `name: http-workspace
workspaces:
  demo:
    provider: http
    url: https://example.com/demo.zip
    format: zip
    mode: mount
`)
	_, err := Normalize(spec, NormalizeOptions{})
	if err == nil || !strings.Contains(err.Error(), "workspace mount mode requires provider file") {
		t.Fatalf("error = %v", err)
	}
}

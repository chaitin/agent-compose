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
    url: " https://example.com/demo.zip "
    format: zip
    path: project
    target: src
    token: ${ARCHIVE_TOKEN}
`)
	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	got := normalized.Workspaces["demo"]
	if got.Provider != "http" || got.Format != "zip" || got.Path != "project" || got.Target != "src" {
		t.Fatalf("workspace = %#v", got)
	}
	if got.URL != "https://example.com/demo.zip" {
		t.Fatalf("workspace url = %q, want the trimmed url", got.URL)
	}
	if got.Token != "${ARCHIVE_TOKEN}" {
		t.Fatalf("workspace token = %q, want the credential reference preserved", got.Token)
	}
}

// TestNormalizeHTTPWorkspaceRejectsUnsupportedDelivery documents that http
// workspaces rely on the shared delivery validation: only copy delivery is
// supported, so a mount request is rejected there rather than by a
// provider-local rule that could drift from the other providers.
func TestNormalizeHTTPWorkspaceRejectsUnsupportedDelivery(t *testing.T) {
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

func TestNormalizeHTTPWorkspaceRejectsUnsupportedFields(t *testing.T) {
	for _, test := range []struct{ name, spec, want string }{
		{
			name: "missing url",
			spec: "    provider: http\n    format: zip\n",
			want: "http workspace url is required",
		},
		{
			name: "missing format",
			spec: "    provider: http\n    url: https://example.com/demo.zip\n",
			want: "http workspace format must be zip",
		},
		{
			name: "unsupported format",
			spec: "    provider: http\n    url: https://example.com/demo.tar\n    format: tar\n",
			want: "http workspace format must be zip",
		},
		{
			name: "unsupported ref",
			spec: "    provider: http\n    url: https://example.com/demo.zip\n    format: zip\n    ref: main\n",
			want: "http workspace does not support ref",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := mustParseCompose(t, "name: http-workspace\nworkspaces:\n  demo:\n"+test.spec)
			_, err := Normalize(spec, NormalizeOptions{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

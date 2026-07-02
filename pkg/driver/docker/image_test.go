package dockerdriver

import (
	"strings"
	"testing"

	typesimage "github.com/docker/docker/api/types/image"
)

func TestMatchLocalDockerImageRefUsesExactLocalTag(t *testing.T) {
	images := []typesimage.Summary{{
		ID:       "sha256:111",
		RepoTags: []string{"agent-compose-guest:latest"},
	}}

	got, ok := matchLocalDockerImageRef("agent-compose-guest:latest", images)
	if !ok {
		t.Fatalf("expected local image match")
	}
	if got != "agent-compose-guest:latest" {
		t.Fatalf("unexpected local image ref: got %q want %q", got, "agent-compose-guest:latest")
	}
}

func TestMatchLocalDockerImageRefMatchesShortLocalTagForRegistryRequest(t *testing.T) {
	images := []typesimage.Summary{{
		ID:       "sha256:111",
		RepoTags: []string{"agent-compose-guest:latest"},
	}}

	got, ok := matchLocalDockerImageRef("registry.example.com/agent-compose-guest:latest", images)
	if !ok {
		t.Fatalf("expected local image match for registry request")
	}
	if got != "agent-compose-guest:latest" {
		t.Fatalf("unexpected local image ref: got %q want %q", got, "agent-compose-guest:latest")
	}
}

func TestMatchLocalDockerImageRefRejectsTagMismatch(t *testing.T) {
	images := []typesimage.Summary{{
		ID:       "sha256:111",
		RepoTags: []string{"agent-compose-guest:dev"},
	}}

	if got, ok := matchLocalDockerImageRef("agent-compose-guest:latest", images); ok {
		t.Fatalf("expected no local image match, got %q", got)
	}
}

func TestMatchLocalDockerImageRefRejectsAmbiguousBasenameMatches(t *testing.T) {
	images := []typesimage.Summary{
		{
			ID:       "sha256:111",
			RepoTags: []string{"registry-a.example.com/team-a/agent-compose-guest:latest"},
		},
		{
			ID:       "sha256:222",
			RepoTags: []string{"registry-b.example.com/team-b/agent-compose-guest:latest"},
		},
	}

	if got, ok := matchLocalDockerImageRef("registry.example.com/team-c/agent-compose-guest:latest", images); ok {
		t.Fatalf("expected ambiguous local image match to fail, got %q", got)
	}
}

func TestDockerImageRefMatchingInternals(t *testing.T) {
	testDockerImageRefMatchingInternals(t)
}

func testDockerImageRefMatchingInternals(t *testing.T) {
	t.Helper()
	requested, err := parseDockerImageRef("docker.io/library/alpine:3.20")
	if err != nil {
		t.Fatalf("parseDockerImageRef(requested) returned error: %v", err)
	}
	candidate, err := parseDockerImageRef("alpine:3.20")
	if err != nil {
		t.Fatalf("parseDockerImageRef(candidate) returned error: %v", err)
	}
	if requested.familiar != "alpine:3.20" || requested.trimmedPath != "alpine" || requested.basename != "alpine" || requested.tag != "3.20" {
		t.Fatalf("unexpected parsed requested ref: %+v", requested)
	}
	if score := scoreDockerImageRefMatch(requested, candidate); score != 120 {
		t.Fatalf("scoreDockerImageRefMatch(exact familiar) = %d, want 120", score)
	}

	registryCandidate, err := parseDockerImageRef("registry.example.com/team/alpine:3.20")
	if err != nil {
		t.Fatalf("parseDockerImageRef(registry candidate) returned error: %v", err)
	}
	if score := scoreDockerImageRefMatch(requested, registryCandidate); score != 80 {
		t.Fatalf("scoreDockerImageRefMatch(basename) = %d, want 80", score)
	}

	tagMismatch, err := parseDockerImageRef("alpine:latest")
	if err != nil {
		t.Fatalf("parseDockerImageRef(tag mismatch) returned error: %v", err)
	}
	if score := scoreDockerImageRefMatch(requested, tagMismatch); score != 0 {
		t.Fatalf("scoreDockerImageRefMatch(tag mismatch) = %d, want 0", score)
	}
}

func TestConsumeDockerPullStream(t *testing.T) {
	testConsumeDockerPullStream(t)
}

func testConsumeDockerPullStream(t *testing.T) {
	t.Helper()
	if err := consumeDockerPullStream(strings.NewReader(`{"status":"Pulling fs layer"}` + "\n" + `{"stream":"done"}`)); err != nil {
		t.Fatalf("consumeDockerPullStream(success) returned error: %v", err)
	}
	if err := consumeDockerPullStream(strings.NewReader(`{"errorDetail":{"message":"denied"}}`)); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("consumeDockerPullStream(error detail) = %v, want denied", err)
	}
	if err := consumeDockerPullStream(strings.NewReader(`{`)); err == nil {
		t.Fatalf("consumeDockerPullStream(invalid json) returned nil error")
	}
}

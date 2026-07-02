package agentcompose

import "testing"

func TestServiceProjectServiceCachesHandler(t *testing.T) {
	service := &Service{}
	first := service.projectService()
	if first == nil {
		t.Fatalf("projectService returned nil")
	}
	second := service.projectService()
	if second != first {
		t.Fatalf("projectService rebuilt handler: first=%p second=%p", first, second)
	}

	prebuilt := NewProjectServiceFromDeps(nil)
	service = &Service{projectHandlers: prebuilt}
	if got := service.projectService(); got != prebuilt {
		t.Fatalf("projectService ignored prebuilt handler: got=%p want=%p", got, prebuilt)
	}
}

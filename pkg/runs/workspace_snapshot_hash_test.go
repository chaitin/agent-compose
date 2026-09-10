package runs

import (
	domain "github.com/chaitin/agent-compose/pkg/model"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestWorkspaceHashIncludesEverySemanticFieldExceptGeneration(t *testing.T) {
	workspace := &domain.SandboxWorkspace{ID: "id", Name: "name", Type: "file", ConfigJSON: "{}", SnapshotID: "generation-one"}
	hash := func(workspace *domain.SandboxWorkspace) string {
		t.Helper()
		value, err := stickyProjectRunConfigHash("base", domain.ProjectRunRecord{}, Preparation{Workspace: workspace}, stickySandboxSpec{})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	original := hash(workspace)
	semantic := map[string]bool{"ID": true, "Name": true, "Type": true, "ConfigJSON": true, "SnapshotID": false, "SnapshotLease": false}
	typ := reflect.TypeFor[domain.SandboxWorkspace]()
	if typ.NumField() != len(semantic) {
		t.Fatal("review new workspace fields' sticky hash contract")
	}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		included, ok := semantic[name]
		if !ok {
			t.Fatalf("missing hash field %s", name)
		}
		t.Run(name, func(t *testing.T) {
			changed := *workspace
			if name == "SnapshotLease" {
				changed.SnapshotLease = io.NopCloser(strings.NewReader("lease"))
			} else {
				reflect.ValueOf(&changed).Elem().Field(i).SetString("changed")
			}
			if (hash(&changed) != original) != included {
				t.Fatalf("field %s hash inclusion should be %v", name, included)
			}
		})
	}
}

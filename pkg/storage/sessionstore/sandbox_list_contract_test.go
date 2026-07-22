package sessionstore

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	domain "agent-compose/pkg/model"
)

func TestFromConfigPreservesFilesystemListing(t *testing.T) {
	store := newTestStore(t)
	sandbox := seedSandboxDir(t, store, "from-config-list", time.Unix(100, 0).UTC())
	store.recordIndex(sandbox)
	fallback := FromConfig(store.config)
	if err := fallback.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	result, err := fallback.ListSandboxes(context.Background(), domain.SandboxListOptions{})
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if got := ids(result.Sandboxes); len(got) != 1 || got[0] != sandbox.Summary.ID {
		t.Fatalf("sandboxes = %v, want [%s]", got, sandbox.Summary.ID)
	}
}

func TestIndexedListMatchesFilesystemContract(t *testing.T) {
	store := newTestStore(t)
	base := time.Unix(1_000, 0).UTC()
	sandboxes := []*domain.Sandbox{
		seedSandboxDir(t, store, "unicode", base.Add(3*time.Second+300*time.Microsecond)),
		seedSandboxDir(t, store, "manual", base.Add(2*time.Second+200*time.Microsecond)),
		seedSandboxDir(t, store, "script", base.Add(time.Second+100*time.Microsecond)),
		seedSandboxDir(t, store, "tie-a", base.Add(500*time.Microsecond)),
		seedSandboxDir(t, store, "tie-z", base.Add(500*time.Microsecond)),
	}
	sandboxes[0].Summary.Title = "Ärger in 東京"
	sandboxes[0].Summary.TriggerSource = "script:Über"
	sandboxes[0].WorkspaceID = "MÜNCHEN-top"
	sandboxes[0].Workspace = &domain.SandboxWorkspace{ID: "東京-nested", Name: "Größe", Type: "TÝPE"}
	sandboxes[1].Summary.Title = "literal % and _"
	sandboxes[1].Summary.TriggerSource = domain.SandboxTypeManual
	sandboxes[1].Summary.VMStatus = "running"
	sandboxes[2].Summary.Title = "ordinary"
	sandboxes[2].Summary.TriggerSource = "script:loader"
	for _, sandbox := range sandboxes {
		if err := store.SaveSandbox(sandbox); err != nil {
			t.Fatalf("SaveSandbox(%s): %v", sandbox.Summary.ID, err)
		}
	}

	reference := FromConfig(store.config)
	options := []domain.SandboxListOptions{
		{Limit: 2},
		{Offset: -5, Limit: 1},
		{Limit: 0},
		{Offset: 3, Limit: 2},
		{Offset: 5, Limit: 2},
		{Offset: 10, Limit: 2},
		{TitleQuery: "ärGER"},
		{TitleQuery: "%"},
		{TitleQuery: "_"},
		{TriggerSourceQuery: "üBER"},
		{WorkspaceQuery: "münchen"},
		{WorkspaceQuery: "東京"},
		{WorkspaceQuery: "GRÖẞE"},
		{WorkspaceQuery: "týpe"},
		{SandboxType: domain.SandboxTypeScript},
		{SandboxType: domain.SandboxTypeManual},
		{SandboxType: "scheduled"},
		{Driver: "DOCKER"},
		{VMStatus: "RUNNING"},
		{CreatedFrom: base.Add(2*time.Second + 250*time.Microsecond)},
		{CreatedTo: base.Add(2*time.Second + 250*time.Microsecond)},
		{UpdatedFrom: base.Add(2*time.Second + 250*time.Microsecond)},
		{UpdatedTo: base.Add(2*time.Second + 250*time.Microsecond)},
		{SandboxType: domain.SandboxTypeScript, TitleQuery: "ärger", WorkspaceQuery: "東京"},
		{BeforeUpdatedAt: sandboxes[1].Summary.UpdatedAt, BeforeID: sandboxes[1].Summary.ID},
		{BeforeUpdatedAt: sandboxes[1].Summary.UpdatedAt},
		{BeforeUpdatedAt: sandboxes[1].Summary.UpdatedAt, BeforeID: sandboxes[1].Summary.ID, Offset: 1, Limit: 1},
		{BeforeUpdatedAt: sandboxes[3].Summary.UpdatedAt, BeforeID: sandboxes[3].Summary.ID},
		{BeforeUpdatedAt: sandboxes[4].Summary.UpdatedAt, BeforeID: sandboxes[4].Summary.ID},
	}
	for index, option := range options {
		t.Run(fmt.Sprintf("case-%02d", index), func(t *testing.T) {
			got, err := store.ListSandboxes(context.Background(), option)
			if err != nil {
				t.Fatalf("indexed list: %v", err)
			}
			want, err := reference.ListSandboxes(context.Background(), option)
			if err != nil {
				t.Fatalf("filesystem list: %v", err)
			}
			if !reflect.DeepEqual(ids(got.Sandboxes), ids(want.Sandboxes)) ||
				got.TotalCount != want.TotalCount || got.HasMore != want.HasMore || got.NextOffset != want.NextOffset {
				t.Fatalf("indexed=%v total=%d more=%v next=%d; filesystem=%v total=%d more=%v next=%d",
					ids(got.Sandboxes), got.TotalCount, got.HasMore, got.NextOffset,
					ids(want.Sandboxes), want.TotalCount, want.HasMore, want.NextOffset)
			}
		})
	}
}

func TestIndexedListDoesNotPreallocateRequestedLimit(t *testing.T) {
	store := newTestStore(t)
	sandbox := seedSandboxDir(t, store, "large-limit", time.Unix(100, 0).UTC())
	store.recordIndex(sandbox)

	result, err := store.ListSandboxes(context.Background(), domain.SandboxListOptions{Limit: 1 << 30})
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if got := ids(result.Sandboxes); len(got) != 1 || got[0] != sandbox.Summary.ID {
		t.Fatalf("sandboxes = %v, want [%s]", got, sandbox.Summary.ID)
	}
}

func TestListSandboxesClampsOffsetBeyondEndAfterGhostPruning(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	for index, id := range []string{"first", "second"} {
		sandbox := seedSandboxDir(t, store, id, time.Unix(int64(100+index), 0).UTC())
		store.recordIndex(sandbox)
	}
	if err := store.index.Upsert(ctx, sb("ghost", time.Unix(102, 0).UTC())); err != nil {
		t.Fatalf("seed ghost: %v", err)
	}

	for _, offset := range []int{2, 10} {
		t.Run(fmt.Sprintf("offset-%d", offset), func(t *testing.T) {
			result, err := store.ListSandboxes(ctx, domain.SandboxListOptions{Offset: offset, Limit: 1})
			if err != nil {
				t.Fatalf("list sandboxes: %v", err)
			}
			if len(result.Sandboxes) != 0 || result.TotalCount != 2 || result.HasMore || result.NextOffset != 2 {
				t.Fatalf("result=%#v, want empty total=2 hasMore=false next=2", result)
			}
		})
	}
}

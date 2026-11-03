package main

import (
	"errors"
	"strings"
	"testing"
)

func TestCollectAllPagesReadsUntilReportedTotal(t *testing.T) {
	var offsets []uint32
	total, err := collectAllPages("widget list", func(offset uint32) (int, uint32, error) {
		offsets = append(offsets, offset)
		switch offset {
		case 0:
			return 2, 5, nil
		case 2:
			return 3, 5, nil
		default:
			t.Fatalf("unexpected offset %d", offset)
			return 0, 0, nil
		}
	})
	if err != nil {
		t.Fatalf("collectAllPages() error = %v", err)
	}
	if total != 5 {
		t.Fatalf("total = %d, want 5", total)
	}
	if len(offsets) != 2 || offsets[0] != 0 || offsets[1] != 2 {
		t.Fatalf("offsets = %v, want [0 2]", offsets)
	}
}

func TestCollectAllPagesStopsWhenTheDaemonReportsNoTotal(t *testing.T) {
	// A zero total after a non-empty page ends paging, matching the callers
	// that predate this helper. This pins the behavior so a future change does
	// not silently turn "no total reported" into an unbounded read.
	calls := 0
	total, err := collectAllPages("widget list", func(offset uint32) (int, uint32, error) {
		calls++
		return 2, 0, nil
	})
	if err != nil {
		t.Fatalf("collectAllPages() error = %v", err)
	}
	if total != 0 {
		t.Fatalf("total = %d, want 0", total)
	}
	if calls != 1 {
		t.Fatalf("fetch calls = %d, want 1", calls)
	}
}

func TestCollectAllPagesStopsOnEmptyFirstPage(t *testing.T) {
	total, err := collectAllPages("widget list", func(offset uint32) (int, uint32, error) {
		return 0, 0, nil
	})
	if err != nil {
		t.Fatalf("collectAllPages() error = %v", err)
	}
	if total != 0 {
		t.Fatalf("total = %d, want 0", total)
	}
}

func TestCollectAllPagesRejectsAStalledPage(t *testing.T) {
	_, err := collectAllPages("widget list", func(offset uint32) (int, uint32, error) {
		if offset == 0 {
			return 2, 5, nil
		}
		return 0, 5, nil
	})
	if err == nil || !strings.Contains(err.Error(), "widget list pagination did not advance") {
		t.Fatalf("error = %v, want the pagination sentinel", err)
	}
}

func TestCollectAllPagesPropagatesFetchError(t *testing.T) {
	sentinel := errors.New("daemon unavailable")
	_, err := collectAllPages("widget list", func(offset uint32) (int, uint32, error) {
		if offset == 0 {
			return 2, 5, nil
		}
		return 0, 0, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want %v", err, sentinel)
	}
}

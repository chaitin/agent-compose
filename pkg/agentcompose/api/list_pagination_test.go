package api

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/apierror"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/errdetail"
)

func TestListPaginationConstraints(t *testing.T) {
	tests := []struct {
		name       string
		offset     uint32
		limit      uint32
		wantOffset int
		wantLimit  int
		wantError  bool
	}{
		{name: "default limit", offset: 7, wantOffset: 7, wantLimit: defaultListLimit},
		{name: "maximum limit", limit: maxListLimit, wantLimit: maxListLimit},
		{name: "limit too large", limit: maxListLimit + 1, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			offset, limit, err := listPagination(test.offset, test.limit)
			if test.wantError && connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("error code = %s, want %s: %v", connect.CodeOf(err), connect.CodeInvalidArgument, err)
			}
			if !test.wantError && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if err == nil && (offset != test.wantOffset || limit != test.wantLimit) {
				t.Fatalf("pagination = (%d, %d), want (%d, %d)", offset, limit, test.wantOffset, test.wantLimit)
			}
		})
	}
}

func TestPaginateListReturnsExactTotalAndEmptyPageBeyondEnd(t *testing.T) {
	page, total, err := paginateList([]string{"a", "b", "c"}, 1, 1)
	if err != nil || total != 3 || len(page) != 1 || page[0] != "b" {
		t.Fatalf("page=%v total=%d err=%v", page, total, err)
	}
	page, total, err = paginateList([]string{"a", "b", "c"}, 3, 1)
	if err != nil || total != 3 || len(page) != 0 {
		t.Fatalf("empty page=%v total=%d err=%v", page, total, err)
	}
}

func TestListPaginationCarriesInvalidRequestReason(t *testing.T) {
	_, _, err := listPagination(0, maxListLimit+1)
	if err == nil {
		t.Fatal("expected an error for an oversized limit")
	}
	detail, ok := errdetail.From(err)
	if !ok {
		t.Fatal("pagination error carries no detail")
	}
	if ReasonFromProto(detail.Reason) != apierror.ReasonInvalidRequest {
		t.Fatalf("reason = %d, want invalid_request", ReasonFromProto(detail.Reason))
	}
	if EffectFromProto(detail.Effect) != apierror.EffectUnspecified {
		t.Fatalf("effect = %d, want unspecified", EffectFromProto(detail.Effect))
	}
}

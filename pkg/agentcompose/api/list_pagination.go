package api

import (
	"fmt"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/apierror"
)

const (
	defaultListLimit = 100
	maxListLimit     = 500
)

func listPagination(offset, limit uint32) (int, int, error) {
	if limit == 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		return 0, 0, NewOperationError(connect.CodeInvalidArgument, apierror.ReasonInvalidRequest, apierror.EffectUnspecified, fmt.Errorf("limit must be 0 or at most %d", maxListLimit))
	}
	return int(offset), int(limit), nil
}

func paginateList[T any](items []T, offset, limit uint32) ([]T, uint32, error) {
	start, size, err := listPagination(offset, limit)
	if err != nil {
		return nil, 0, err
	}
	total := len(items)
	if start >= total {
		return nil, uint32(total), nil
	}
	end := min(start+size, total)
	return items[start:end], uint32(total), nil
}

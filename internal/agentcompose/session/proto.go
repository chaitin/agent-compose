package session

import (
	"fmt"
	"strings"
	"time"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func ListOptionsFromProto(req *agentcomposev1.ListSessionsRequest) (ListOptions, error) {
	if req == nil {
		return ListOptions{}, nil
	}
	createdFrom, err := ParseOptionalRFC3339(req.GetCreatedFrom(), "created_from")
	if err != nil {
		return ListOptions{}, err
	}
	createdTo, err := ParseOptionalRFC3339(req.GetCreatedTo(), "created_to")
	if err != nil {
		return ListOptions{}, err
	}
	updatedFrom, err := ParseOptionalRFC3339(req.GetUpdatedFrom(), "updated_from")
	if err != nil {
		return ListOptions{}, err
	}
	updatedTo, err := ParseOptionalRFC3339(req.GetUpdatedTo(), "updated_to")
	if err != nil {
		return ListOptions{}, err
	}
	return ListOptions{
		SessionType:        req.GetSessionType(),
		TriggerSourceQuery: req.GetTriggerSourceQuery(),
		TitleQuery:         req.GetTitleQuery(),
		WorkspaceQuery:     req.GetWorkspaceQuery(),
		Driver:             req.GetDriver(),
		VMStatus:           req.GetVmStatus(),
		CreatedFrom:        createdFrom,
		CreatedTo:          createdTo,
		UpdatedFrom:        updatedFrom,
		UpdatedTo:          updatedTo,
		Offset:             int(req.GetOffset()),
		Limit:              int(req.GetLimit()),
	}, nil
}

func ParseOptionalRFC3339(raw, field string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s: %w", field, err)
	}
	return value.UTC(), nil
}

package connecttransport

import (
	"fmt"
	"strings"
	"time"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func sessionListOptionsFromProto(req *agentcomposev1.ListSessionsRequest) (SessionListOptions, error) {
	if req == nil {
		return SessionListOptions{}, nil
	}
	createdFrom, err := parseOptionalRFC3339(req.GetCreatedFrom(), "created_from")
	if err != nil {
		return SessionListOptions{}, err
	}
	createdTo, err := parseOptionalRFC3339(req.GetCreatedTo(), "created_to")
	if err != nil {
		return SessionListOptions{}, err
	}
	updatedFrom, err := parseOptionalRFC3339(req.GetUpdatedFrom(), "updated_from")
	if err != nil {
		return SessionListOptions{}, err
	}
	updatedTo, err := parseOptionalRFC3339(req.GetUpdatedTo(), "updated_to")
	if err != nil {
		return SessionListOptions{}, err
	}
	return SessionListOptions{
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

func parseOptionalRFC3339(raw, field string) (time.Time, error) {
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

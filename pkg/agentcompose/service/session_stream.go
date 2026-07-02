package agentcompose

import (
	"agent-compose/pkg/agentcompose/sessions"

	"github.com/samber/do/v2"
)

func NewSessionStreamBroker(di do.Injector) (*sessions.StreamBroker, error) {
	return sessions.NewStreamBroker(di)
}

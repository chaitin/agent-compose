package agentcompose

import (
	"agent-compose/pkg/sessions"

	"github.com/samber/do/v2"
)

func NewSessionStreamBroker(di do.Injector) (*sessions.StreamBroker, error) {
	return sessions.NewStreamBroker(di)
}

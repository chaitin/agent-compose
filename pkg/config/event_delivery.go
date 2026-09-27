package config

import (
	"fmt"
	"os"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

const eventDeliveryScopeName = "EVENT_DELIVERY_SCOPE"

// loadEventDeliveryScope reads the operator-set bound on which Projects
// receive a published event. It defaults to the publisher's own Project; a
// daemon shared by a single user may widen it to the whole daemon.
func loadEventDeliveryScope() (domain.EventDeliveryScope, error) {
	scope, err := domain.ParseEventDeliveryScope(os.Getenv(eventDeliveryScopeName))
	if err != nil {
		return "", fmt.Errorf("%s: %w", eventDeliveryScopeName, err)
	}
	return scope, nil
}

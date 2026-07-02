package capability

import (
	"context"

	modeldomain "agent-compose/internal/model"
	filestore "agent-compose/internal/persistence/filestore"
	sessiondomain "agent-compose/internal/session"
	capapi "agent-compose/pkg/capability"
)

type SessionEnvVar = modeldomain.SessionEnvVar
type SessionTag = modeldomain.SessionTag
type Session = modeldomain.Session
type SessionEvent = modeldomain.SessionEvent
type Store = filestore.Store
type SessionStreamBroker = sessiondomain.SessionStreamBroker

type CapabilityProvider interface {
	Status(context.Context) capapi.Status
	ListCapsets(context.Context) ([]capapi.Capset, error)
	Catalog(context.Context, string) (capapi.Catalog, error)
	CapabilityGuide(ctx context.Context, capsetID string) ([]byte, error)
	ProxyTarget() string
}

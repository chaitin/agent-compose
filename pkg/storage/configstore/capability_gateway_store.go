package configstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/capabilitygateway"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// capabilityGatewayStore owns the capability gateway settings row.
type capabilityGatewayStore struct {
	db     *sql.DB
	client *dao.Client
}

// capabilityGatewayRowID pins the settings to a single row.
const capabilityGatewayRowID = 1

// GetCapabilityGateway returns the stored OctoBus connection. An empty addr
// means the gateway is not configured.
func (s *capabilityGatewayStore) GetCapabilityGateway(ctx context.Context) (domain.CapabilityGatewaySettings, error) {
	entity, err := s.client.CapabilityGateway.Query().
		Select(capabilitygateway.FieldAddr, capabilitygateway.FieldToken).
		Where(capabilitygateway.IDEQ(capabilityGatewayRowID)).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return domain.CapabilityGatewaySettings{}, nil
		}
		return domain.CapabilityGatewaySettings{}, fmt.Errorf("query capability gateway: %w", err)
	}
	return domain.CapabilityGatewaySettings{Addr: entity.Addr, Token: entity.Token}, nil
}

// SaveCapabilityGateway upserts the OctoBus connection settings.
func (s *capabilityGatewayStore) SaveCapabilityGateway(ctx context.Context, settings domain.CapabilityGatewaySettings) (domain.CapabilityGatewaySettings, error) {
	settings.Addr = strings.TrimSpace(settings.Addr)
	settings.Token = strings.TrimSpace(settings.Token)
	updatedAt := time.Now().UTC().Unix()
	if err := s.client.CapabilityGateway.Create().
		SetID(capabilityGatewayRowID).
		SetAddr(settings.Addr).
		SetToken(settings.Token).
		SetUpdatedAt(updatedAt).
		OnConflict(entsql.ConflictColumns(capabilitygateway.FieldID), entsql.ResolveWithNewValues()).
		UpdateNewValues().
		Exec(ctx); err != nil {
		return domain.CapabilityGatewaySettings{}, fmt.Errorf("save capability gateway: %w", err)
	}
	return settings, nil
}

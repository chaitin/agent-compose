package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// capabilityGatewayStore owns the capability gateway settings row.
type capabilityGatewayStore struct {
	db *sql.DB
}

// capabilityGatewayRowID pins the settings to a single row.
const capabilityGatewayRowID = 1

// GetCapabilityGateway returns the stored OctoBus connection. An empty addr
// means the gateway is not configured.
func (s *capabilityGatewayStore) GetCapabilityGateway(ctx context.Context) (domain.CapabilityGatewaySettings, error) {
	row := s.db.QueryRowContext(ctx, `SELECT addr, token, admin_token FROM capability_gateway WHERE id = ?`, capabilityGatewayRowID)
	var settings domain.CapabilityGatewaySettings
	switch err := row.Scan(&settings.Addr, &settings.Token, &settings.AdminToken); {
	case errors.Is(err, sql.ErrNoRows):
		return domain.CapabilityGatewaySettings{}, nil
	case err != nil:
		return domain.CapabilityGatewaySettings{}, fmt.Errorf("query capability gateway: %w", err)
	}
	return settings, nil
}

// SaveCapabilityGateway upserts the OctoBus connection settings.
func (s *capabilityGatewayStore) SaveCapabilityGateway(ctx context.Context, settings domain.CapabilityGatewaySettings) (domain.CapabilityGatewaySettings, error) {
	settings.Addr = strings.TrimSpace(settings.Addr)
	settings.Token = strings.TrimSpace(settings.Token)
	settings.AdminToken = strings.TrimSpace(settings.AdminToken)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO capability_gateway(id, addr, token, admin_token, updated_at) VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET addr = excluded.addr, token = excluded.token, admin_token = excluded.admin_token, updated_at = excluded.updated_at`,
		capabilityGatewayRowID, settings.Addr, settings.Token, settings.AdminToken, time.Now().UTC().Unix()); err != nil {
		return domain.CapabilityGatewaySettings{}, fmt.Errorf("save capability gateway: %w", err)
	}
	return settings, nil
}

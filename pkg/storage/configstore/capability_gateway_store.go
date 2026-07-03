package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	domain "agent-compose/pkg/model"
)

// capabilityGatewayRowID pins the settings to a single row.
const capabilityGatewayRowID = 1

func (s *ConfigStore) ensureCapabilityGatewaySchema(ctx context.Context) error {
	const createStmt = `CREATE TABLE IF NOT EXISTS capability_gateway (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		addr TEXT NOT NULL DEFAULT '',
		token TEXT NOT NULL DEFAULT '',
		updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
	);`
	if _, err := s.db.ExecContext(ctx, createStmt); err != nil {
		return fmt.Errorf("create capability gateway schema: %w", err)
	}
	return nil
}

func (s *ConfigStore) EnsureCapabilityGatewaySchema(ctx context.Context) error {
	return s.ensureCapabilityGatewaySchema(ctx)
}

// GetCapabilityGateway returns the stored OctoBus connection. An empty addr
// means the gateway is not configured.
func (s *ConfigStore) GetCapabilityGateway(ctx context.Context) (domain.CapabilityGatewaySettings, error) {
	row := s.db.QueryRowContext(ctx, `SELECT addr, token FROM capability_gateway WHERE id = ?`, capabilityGatewayRowID)
	var settings domain.CapabilityGatewaySettings
	switch err := row.Scan(&settings.Addr, &settings.Token); {
	case errors.Is(err, sql.ErrNoRows):
		return domain.CapabilityGatewaySettings{}, nil
	case err != nil:
		return domain.CapabilityGatewaySettings{}, fmt.Errorf("query capability gateway: %w", err)
	}
	return settings, nil
}

// SaveCapabilityGateway upserts the OctoBus connection settings.
func (s *ConfigStore) SaveCapabilityGateway(ctx context.Context, settings domain.CapabilityGatewaySettings) (domain.CapabilityGatewaySettings, error) {
	settings.Addr = strings.TrimSpace(settings.Addr)
	settings.Token = strings.TrimSpace(settings.Token)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO capability_gateway(id, addr, token, updated_at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET addr = excluded.addr, token = excluded.token, updated_at = excluded.updated_at`,
		capabilityGatewayRowID, settings.Addr, settings.Token, time.Now().UTC().Unix()); err != nil {
		return domain.CapabilityGatewaySettings{}, fmt.Errorf("save capability gateway: %w", err)
	}
	return settings, nil
}

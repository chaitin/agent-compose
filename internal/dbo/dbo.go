package dbo

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"

	"github.com/samber/do/v2"
	_ "modernc.org/sqlite"

	"agent-compose/internal/config"
)

type DBResource struct {
	DB     *sql.DB
	logger *slog.Logger
}

func (r *DBResource) Shutdown(ctx context.Context) error {
	if r == nil || r.DB == nil {
		return nil
	}

	r.logger.Info("shutting down database connection")
	if err := r.DB.Close(); err != nil {
		return err
	}
	return nil
}

func NewDBResource(di do.Injector) (*DBResource, error) {
	logger := do.MustInvoke[*slog.Logger](di)

	conf, err := do.Invoke[*config.Config](di)
	if err != nil {
		return nil, fmt.Errorf("failed to NewDb due to config dependency injection error")
	}

	db, err := sql.Open("sqlite", conf.DbAddr)
	if err != nil {
		logger.Error("open slite failed", "error", err)
		os.Exit(10)
	}

	logger.Info("database connected successfully")

	return &DBResource{DB: db, logger: logger}, nil
}

func NewDb(di do.Injector) (*sql.DB, error) {
	resource := do.MustInvoke[*DBResource](di)
	return resource.DB, nil
}

func Setup(di do.Injector) {
	do.Provide(di, NewDBResource)
	do.Provide(di, NewDb)
	// TODO: db healthcheck
}

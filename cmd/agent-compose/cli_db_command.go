package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/samber/do/v2"
	"github.com/spf13/cobra"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/storage/pgcopy"
	"github.com/chaitin/agent-compose/pkg/storage/postgres"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
)

func newCLIDBCommand(options *cliOptions) *cobra.Command {
	command := &cobra.Command{
		Use:   "db",
		Short: "Inspect, migrate, and copy the daemon database",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(
		newCLIDBStatusCommand(options),
		newCLIDBMigrateCommand(options),
		newCLIDBCopyCommand(options),
	)
	return command
}

type dbStatusOutput struct {
	Driver     string                 `json:"driver"`
	SQLitePath string                 `json:"sqlite_path,omitempty"`
	Target     string                 `json:"target,omitempty"`
	Applied    int64                  `json:"applied_version"`
	Latest     int64                  `json:"latest_version"`
	Pending    int                    `json:"pending"`
	Migrations []dbMigrationStatusOut `json:"migrations,omitempty"`
}

type dbMigrationStatusOut struct {
	Version int64  `json:"version"`
	Name    string `json:"name"`
	Applied bool   `json:"applied"`
}

func newCLIDBStatusCommand(options *cliOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the configured database and pending migrations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			config, err := loadLocalDatabaseConfig()
			if err != nil {
				return err
			}
			status, err := inspectDatabase(cmd.Context(), config)
			if err != nil {
				return err
			}
			if options.JSON {
				data, err := json.MarshalIndent(status, "", "  ")
				if err != nil {
					return err
				}
				return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
			}
			return writeDatabaseStatus(cmd.OutOrStdout(), status)
		},
	}
}

func newCLIDBMigrateCommand(options *cliOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Apply pending database migrations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			config, err := loadLocalDatabaseConfig()
			if err != nil {
				return err
			}
			status, err := migrateDatabase(cmd.Context(), config)
			if err != nil {
				return err
			}
			if options.JSON {
				data, err := json.MarshalIndent(status, "", "  ")
				if err != nil {
					return err
				}
				return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "database migrated: driver=%s applied=%d latest=%d\n", status.Driver, status.Applied, status.Latest)
			return err
		},
	}
}

func newCLIDBCopyCommand(options *cliOptions) *cobra.Command {
	command := &cobra.Command{
		Use:   "copy",
		Short: "Copy an offline SQLite data.db into an empty PostgreSQL database",
		Long: "Copy an offline SQLite data.db into an empty, migrated PostgreSQL database.\n" +
			"The daemon must be stopped. The SQLite source is opened read-only and is never modified.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			config, err := loadLocalDatabaseConfig()
			if err != nil {
				return err
			}
			source, _ := cmd.Flags().GetString("source")
			targetURL, _ := cmd.Flags().GetString("to")
			batchSize, _ := cmd.Flags().GetInt("batch-size")
			if strings.TrimSpace(source) == "" {
				source = config.DbAddr
			}
			if strings.TrimSpace(targetURL) == "" {
				targetURL = config.DBURL
			}
			if strings.TrimSpace(targetURL) == "" {
				return fmt.Errorf("db copy requires DB_DRIVER=postgres with DB_URL, or --to <postgres-url>")
			}
			if _, err := os.Stat(source); err != nil {
				return fmt.Errorf("read SQLite source %s: %w", source, err)
			}
			postgresConfig := postgresConfigFromApp(config)
			postgresConfig.URL = targetURL
			target, err := postgres.Open(cmd.Context(), postgresConfig)
			if err != nil {
				return err
			}
			defer func() { _ = target.Close() }()
			result, err := pgcopy.Copy(cmd.Context(), source, target.DB(), pgcopy.Options{BatchSize: batchSize})
			if err != nil {
				return err
			}
			var rows int64
			for _, table := range result.Tables {
				rows += table.Rows
			}
			if options.JSON {
				data, err := json.MarshalIndent(result, "", "  ")
				if err != nil {
					return err
				}
				return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "copied %d tables, %d rows; event sequence=%d\nnext: set DB_DRIVER=postgres and DB_URL, then start the daemon\n", len(result.Tables), rows, result.EventSequence)
			return err
		},
	}
	command.Flags().String("source", "", "SQLite data.db path (default: the daemon's DATA_ROOT/data.db)")
	command.Flags().String("to", "", "PostgreSQL URL (default: DB_URL)")
	command.Flags().Int("batch-size", 0, "Rows per INSERT batch (default: 200)")
	return command
}

func loadLocalDatabaseConfig() (*appconfig.Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("load .env: %w", sanitizeDotenvError(err))
	}
	di := do.New()
	do.ProvideValue(di, slog.Default())
	appconfig.Setup(di)
	config, err := do.Invoke[*appconfig.Config](di)
	if err != nil {
		return nil, fmt.Errorf("load database configuration: %w", err)
	}
	return config, nil
}

func inspectDatabase(ctx context.Context, config *appconfig.Config) (dbStatusOutput, error) {
	latest, err := storagesqlite.LatestMigrationVersion()
	if err != nil {
		return dbStatusOutput{}, err
	}
	if config.DBDriver == appconfig.DatabaseDriverPostgres {
		latest, err := postgres.LatestMigrationVersion()
		if err != nil {
			return dbStatusOutput{}, err
		}
		pool, err := postgres.Connect(ctx, postgresConfigFromApp(config))
		if err != nil {
			return dbStatusOutput{}, err
		}
		defer func() { _ = pool.Close() }()
		migrations, err := postgres.Inspect(ctx, pool)
		if err != nil {
			return dbStatusOutput{}, err
		}
		status := dbStatusOutput{Driver: string(appconfig.DatabaseDriverPostgres), Target: redactDatabaseURL(config.DBURL), Latest: latest}
		for index, item := range migrations {
			status.Migrations = append(status.Migrations, dbMigrationStatusOut{Version: item.Version, Name: item.Name, Applied: item.Applied})
			if item.Applied {
				status.Applied = item.Version
			} else {
				status.Pending = len(migrations) - index
				break
			}
		}
		return status, nil
	}

	status := dbStatusOutput{Driver: string(appconfig.DatabaseDriverSQLite), SQLitePath: config.DbAddr, Latest: latest}
	version, err := sqliteAppliedVersion(ctx, config.DbAddr)
	if err != nil {
		return dbStatusOutput{}, err
	}
	status.Applied = version
	if version < latest {
		status.Pending = 1
	}
	return status, nil
}

func migrateDatabase(ctx context.Context, config *appconfig.Config) (dbStatusOutput, error) {
	if config.DBDriver == appconfig.DatabaseDriverPostgres {
		database, err := postgres.Open(ctx, postgresConfigFromApp(config))
		if err != nil {
			return dbStatusOutput{}, err
		}
		if err := database.Close(); err != nil {
			return dbStatusOutput{}, err
		}
		return inspectDatabase(ctx, config)
	}
	database, err := storagesqlite.OpenWithMaxOpenConns(config.DbAddr, config.DbTimeout, config.EffectiveSQLiteMaxOpenConns())
	if err != nil {
		return dbStatusOutput{}, err
	}
	if err := database.Close(); err != nil {
		return dbStatusOutput{}, err
	}
	return inspectDatabase(ctx, config)
}

func sqliteAppliedVersion(ctx context.Context, path string) (int64, error) {
	uri := &url.URL{Scheme: "file", Path: path}
	query := uri.Query()
	query.Set("mode", "ro")
	uri.RawQuery = query.Encode()
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return 0, fmt.Errorf("open SQLite database: %w", err)
	}
	defer func() { _ = db.Close() }()
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&exists); err != nil {
		return 0, fmt.Errorf("inspect SQLite migration history: %w", err)
	}
	if exists == 0 {
		return 0, nil
	}
	return storagesqlite.AppliedMigrationVersion(ctx, db)
}

func postgresConfigFromApp(config *appconfig.Config) postgres.Config {
	return postgres.Config{
		URL:             config.DBURL,
		MaxOpenConns:    config.DBMaxOpenConns,
		MaxIdleConns:    config.DBMaxIdleConns,
		ConnMaxLifetime: config.DBConnMaxLifetime,
		ConnectTimeout:  config.DBConnectTimeout,
	}
}

func redactDatabaseURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "<invalid>"
	}
	return parsed.Host + parsed.Path
}

func writeDatabaseStatus(out interface{ Write([]byte) (int, error) }, status dbStatusOutput) error {
	var builder strings.Builder
	builder.WriteString("driver: " + status.Driver + "\n")
	if status.SQLitePath != "" {
		builder.WriteString("sqlite: " + status.SQLitePath + "\n")
	}
	if status.Target != "" {
		builder.WriteString("target: " + status.Target + "\n")
	}
	fmt.Fprintf(&builder, "schema: applied=%d latest=%d pending=%d\n", status.Applied, status.Latest, status.Pending)
	for _, migration := range status.Migrations {
		state := "pending"
		if migration.Applied {
			state = "applied"
		}
		fmt.Fprintf(&builder, "  %s %s\n", migration.Name, state)
	}
	_, err := out.Write([]byte(builder.String()))
	return err
}

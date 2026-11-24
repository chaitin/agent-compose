package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// DatabaseDriver selects the storage backend. SQLite remains the default so
// an upgrade without new configuration keeps the existing data.db.
type DatabaseDriver string

const (
	DatabaseDriverSQLite   DatabaseDriver = "sqlite"
	DatabaseDriverPostgres DatabaseDriver = "postgres"

	DefaultDatabaseMaxOpenConns    = 10
	DefaultDatabaseMaxIdleConns    = 5
	DefaultDatabaseConnMaxLifetime = 30 * time.Minute
	DefaultDatabaseConnectTimeout  = 10 * time.Second

	maxDatabaseMaxOpenConns = 100
)

// ParseDatabaseDriver accepts the canonical names plus the PostgreSQL spelling
// used by some deployment templates.
func ParseDatabaseDriver(raw string) (DatabaseDriver, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(DatabaseDriverSQLite):
		return DatabaseDriverSQLite, nil
	case string(DatabaseDriverPostgres), "postgresql", "postgre":
		return DatabaseDriverPostgres, nil
	default:
		return "", fmt.Errorf("DB_DRIVER must be sqlite or postgres, got %q", raw)
	}
}

func loadDatabaseDriver() (DatabaseDriver, error) {
	return ParseDatabaseDriver(os.Getenv("DB_DRIVER"))
}

// loadDatabaseURL reads DB_URL only in PostgreSQL mode. A URL left over in a
// shared .env must not silently choose a backend, so SQLite logs and ignores it.
func loadDatabaseURL(driver DatabaseDriver, logger *slog.Logger) (string, error) {
	raw := strings.TrimSpace(os.Getenv("DB_URL"))
	if driver != DatabaseDriverPostgres {
		if raw != "" {
			logger.Warn("DB_URL is ignored because DB_DRIVER is sqlite")
		}
		return "", nil
	}
	if raw == "" {
		return "", fmt.Errorf("DB_DRIVER=postgres requires DB_URL")
	}
	if err := validateDatabaseURL(raw); err != nil {
		return "", err
	}
	return raw, nil
}

// validateDatabaseURL rejects values that pgx would not treat as a PostgreSQL
// URL. The value is never included in the returned error: it may contain a
// password.
func validateDatabaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("DB_URL must be a valid postgres:// URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "postgres", "postgresql":
	default:
		return fmt.Errorf("DB_URL must use the postgres:// or postgresql:// scheme")
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return fmt.Errorf("DB_URL must include a host")
	}
	if strings.TrimSpace(parsed.Path) == "" || strings.TrimSpace(strings.TrimPrefix(parsed.Path, "/")) == "" {
		return fmt.Errorf("DB_URL must include a database name in its path")
	}
	return nil
}

type databasePoolSettings struct {
	maxOpenConns    int
	maxIdleConns    int
	connMaxLifetime time.Duration
	connectTimeout  time.Duration
}

func loadDatabasePoolSettings(driver DatabaseDriver) (databasePoolSettings, error) {
	settings := databasePoolSettings{
		maxOpenConns:    DefaultDatabaseMaxOpenConns,
		maxIdleConns:    DefaultDatabaseMaxIdleConns,
		connMaxLifetime: DefaultDatabaseConnMaxLifetime,
		connectTimeout:  DefaultDatabaseConnectTimeout,
	}
	if driver != DatabaseDriverPostgres {
		return settings, nil
	}
	var err error
	if settings.maxOpenConns, err = databasePositiveIntEnv("DB_MAX_OPEN_CONNS", settings.maxOpenConns, maxDatabaseMaxOpenConns); err != nil {
		return databasePoolSettings{}, err
	}
	if settings.maxIdleConns, err = databaseNonNegativeIntEnv("DB_MAX_IDLE_CONNS", settings.maxIdleConns); err != nil {
		return databasePoolSettings{}, err
	}
	if settings.maxIdleConns > settings.maxOpenConns {
		return databasePoolSettings{}, fmt.Errorf("DB_MAX_IDLE_CONNS must not exceed DB_MAX_OPEN_CONNS")
	}
	if settings.connMaxLifetime, err = databaseNonNegativeDurationEnv("DB_CONN_MAX_LIFETIME", settings.connMaxLifetime); err != nil {
		return databasePoolSettings{}, err
	}
	if settings.connectTimeout, err = databasePositiveDurationEnv("DB_CONNECT_TIMEOUT", settings.connectTimeout); err != nil {
		return databasePoolSettings{}, err
	}
	return settings, nil
}

func databasePositiveIntEnv(name string, defaultValue, maxValue int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > maxValue {
		return 0, fmt.Errorf("%s must be an integer between 1 and %d", name, maxValue)
	}
	return value, nil
}

func databaseNonNegativeIntEnv(name string, defaultValue int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", name)
	}
	return value, nil
}

func databasePositiveDurationEnv(name string, defaultValue time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return value, nil
}

func databaseNonNegativeDurationEnv(name string, defaultValue time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s must be a non-negative duration", name)
	}
	return value, nil
}

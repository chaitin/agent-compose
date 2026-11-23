package config

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samber/do/v2"
)

func TestNewConfigDatabaseDriver(t *testing.T) {
	tests := []struct {
		name       string
		driver     string
		url        string
		wantDriver DatabaseDriver
		wantURL    string
		wantErr    string
	}{
		{name: "default is sqlite", wantDriver: DatabaseDriverSQLite},
		{name: "explicit sqlite", driver: "sqlite", wantDriver: DatabaseDriverSQLite},
		{name: "postgres", driver: "postgres", url: "postgres://user:secret@db.example.com:5432/agent_compose?sslmode=require", wantDriver: DatabaseDriverPostgres, wantURL: "postgres://user:secret@db.example.com:5432/agent_compose?sslmode=require"},
		{name: "postgres alias", driver: "postgre", url: "postgresql://db.example.com/agent_compose", wantDriver: DatabaseDriverPostgres, wantURL: "postgresql://db.example.com/agent_compose"},
		{name: "postgresql name", driver: "postgresql", url: "postgres://db.example.com/agent_compose", wantDriver: DatabaseDriverPostgres, wantURL: "postgres://db.example.com/agent_compose"},
		{name: "unknown driver", driver: "mysql", wantErr: "DB_DRIVER"},
		{name: "postgres without url", driver: "postgres", wantErr: "requires DB_URL"},
		{name: "wrong scheme", driver: "postgres", url: "mysql://db.example.com/agent_compose", wantErr: "postgres://"},
		{name: "missing host", driver: "postgres", url: "postgres:///agent_compose", wantErr: "host"},
		{name: "missing database", driver: "postgres", url: "postgres://db.example.com", wantErr: "database name"},
		{name: "sqlite ignores url", url: "postgres://db.example.com/agent_compose", wantDriver: DatabaseDriverSQLite},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setDatabaseTestEnv(t)
			t.Setenv("DATA_ROOT", filepath.Join(t.TempDir(), "data"))
			t.Setenv("DB_DRIVER", test.driver)
			t.Setenv("DB_URL", test.url)

			config, err := NewConfig(newConfigTestInjector())
			if test.wantErr != "" {
				if err == nil {
					t.Fatal("NewConfig returned nil error")
				}
				if !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %q, want it to contain %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewConfig returned error: %v", err)
			}
			if config.DBDriver != test.wantDriver {
				t.Fatalf("DBDriver = %q, want %q", config.DBDriver, test.wantDriver)
			}
			if config.DBURL != test.wantURL {
				t.Fatalf("DBURL = %q, want %q", config.DBURL, test.wantURL)
			}
		})
	}
}

func TestNewConfigDatabasePool(t *testing.T) {
	t.Run("defaults in sqlite mode", func(t *testing.T) {
		setDatabaseTestEnv(t)
		t.Setenv("DATA_ROOT", filepath.Join(t.TempDir(), "data"))
		config, err := NewConfig(newConfigTestInjector())
		if err != nil {
			t.Fatalf("NewConfig returned error: %v", err)
		}
		if config.DBMaxOpenConns != DefaultDatabaseMaxOpenConns ||
			config.DBMaxIdleConns != DefaultDatabaseMaxIdleConns ||
			config.DBConnMaxLifetime != DefaultDatabaseConnMaxLifetime ||
			config.DBConnectTimeout != DefaultDatabaseConnectTimeout {
			t.Fatalf("unexpected defaults: %+v", config)
		}
	})

	t.Run("configured in postgres mode", func(t *testing.T) {
		setDatabaseTestEnv(t)
		t.Setenv("DATA_ROOT", filepath.Join(t.TempDir(), "data"))
		t.Setenv("DB_DRIVER", "postgres")
		t.Setenv("DB_URL", "postgres://db.example.com/agent_compose")
		t.Setenv("DB_MAX_OPEN_CONNS", "20")
		t.Setenv("DB_MAX_IDLE_CONNS", "7")
		t.Setenv("DB_CONN_MAX_LIFETIME", "45m")
		t.Setenv("DB_CONNECT_TIMEOUT", "3s")
		config, err := NewConfig(newConfigTestInjector())
		if err != nil {
			t.Fatalf("NewConfig returned error: %v", err)
		}
		if config.DBMaxOpenConns != 20 || config.DBMaxIdleConns != 7 ||
			config.DBConnMaxLifetime != 45*time.Minute || config.DBConnectTimeout != 3*time.Second {
			t.Fatalf("unexpected pool settings: open=%d idle=%d lifetime=%s timeout=%s",
				config.DBMaxOpenConns, config.DBMaxIdleConns, config.DBConnMaxLifetime, config.DBConnectTimeout)
		}
	})

	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "open zero", key: "DB_MAX_OPEN_CONNS", value: "0"},
		{name: "open above maximum", key: "DB_MAX_OPEN_CONNS", value: "101"},
		{name: "idle negative", key: "DB_MAX_IDLE_CONNS", value: "-1"},
		{name: "idle above open", key: "DB_MAX_IDLE_CONNS", value: "11"},
		{name: "lifetime invalid", key: "DB_CONN_MAX_LIFETIME", value: "soon"},
		{name: "timeout zero", key: "DB_CONNECT_TIMEOUT", value: "0s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			setDatabaseTestEnv(t)
			t.Setenv("DATA_ROOT", filepath.Join(t.TempDir(), "data"))
			t.Setenv("DB_DRIVER", "postgres")
			t.Setenv("DB_URL", "postgres://db.example.com/agent_compose")
			t.Setenv(test.key, test.value)
			if _, err := NewConfig(newConfigTestInjector()); err == nil {
				t.Fatal("NewConfig returned nil error")
			}
		})
	}
}

func setDatabaseTestEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"DB_DRIVER", "DB_URL", "DB_MAX_OPEN_CONNS", "DB_MAX_IDLE_CONNS",
		"DB_CONN_MAX_LIFETIME", "DB_CONNECT_TIMEOUT",
	} {
		t.Setenv(key, "")
	}
}

func newConfigTestInjector() do.Injector {
	di := do.New()
	do.ProvideValue(di, slog.Default())
	return di
}

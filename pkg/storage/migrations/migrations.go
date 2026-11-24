// Package migrations loads and validates the immutable SQL assets shared by
// the SQLite and PostgreSQL backends. Each backend keeps its own migration
// transaction, locking, DDL, and history table; only the asset format and the
// applied-history contract are common.
package migrations

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const migrationsDir = "migrations"

var filenamePattern = regexp.MustCompile(`^([0-9]{6})_([a-z0-9]+(?:_[a-z0-9]+)*)\.sql$`)

// Asset is one immutable embedded migration.
type Asset struct {
	Version   int64
	Name      string
	Statement string
	Checksum  string
}

// Applied is one recorded migration-history row.
type Applied struct {
	Version  int64
	Name     string
	Checksum string
}

// Load reads, checksums, sorts, and de-duplicates the migration assets under
// migrationsDir. label names the backend in errors, for example "SQLite".
func Load(migrationFS fs.FS, label string) ([]Asset, error) {
	entries, err := fs.ReadDir(migrationFS, migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("read embedded %s migrations: %w", label, err)
	}
	items := make([]Asset, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		match := filenamePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("invalid %s migration filename %q", label, entry.Name())
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid %s migration version in %q", label, entry.Name())
		}
		data, err := fs.ReadFile(migrationFS, migrationsDir+"/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read %s migration %q: %w", label, entry.Name(), err)
		}
		if strings.TrimSpace(string(data)) == "" {
			return nil, fmt.Errorf("%s migration %q is empty", label, entry.Name())
		}
		sum := sha256.Sum256(data)
		items = append(items, Asset{
			Version:   version,
			Name:      strings.TrimSuffix(entry.Name(), ".sql"),
			Statement: string(data),
			Checksum:  hex.EncodeToString(sum[:]),
		})
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no embedded %s migrations found", label)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Version < items[j].Version })
	for index := 1; index < len(items); index++ {
		if items[index-1].Version == items[index].Version {
			return nil, fmt.Errorf("duplicate %s migration version %06d", label, items[index].Version)
		}
	}
	return items, nil
}

// ValidateApplied checks that the recorded history is an exact prefix of the
// embedded assets, so released migration files stay immutable.
func ValidateApplied(label string, available []Asset, applied []Applied) error {
	if len(applied) > len(available) {
		return fmt.Errorf("%s schema is newer than this binary: database has %d migrations, binary has %d", label, len(applied), len(available))
	}
	for index, actual := range applied {
		expected := available[index]
		if actual.Version != expected.Version {
			return fmt.Errorf("%s migration history is not an embedded prefix: database version %06d, expected %06d", label, actual.Version, expected.Version)
		}
		if actual.Name != expected.Name {
			return fmt.Errorf("%s migration %06d name mismatch: database %q, binary %q", label, actual.Version, actual.Name, expected.Name)
		}
		if actual.Checksum != expected.Checksum {
			return fmt.Errorf("%s migration %s checksum mismatch", label, actual.Name)
		}
	}
	return nil
}

// ScanApplied reads version/name/checksum rows into history entries.
func ScanApplied(rows *sql.Rows, label string) ([]Applied, error) {
	var items []Applied
	for rows.Next() {
		var item Applied
		if err := rows.Scan(&item.Version, &item.Name, &item.Checksum); err != nil {
			return nil, fmt.Errorf("scan applied %s migration: %w", label, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied %s migrations: %w", label, err)
	}
	return items, nil
}

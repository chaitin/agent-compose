package migrations

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoadValidation(t *testing.T) {
	tests := []struct {
		name    string
		files   fstest.MapFS
		wantErr string
	}{
		{
			name:    "no SQL files",
			files:   fstest.MapFS{"migrations/README.md": {Data: []byte("docs")}},
			wantErr: "no embedded SQLite migrations",
		},
		{
			name:    "invalid filename",
			files:   fstest.MapFS{"migrations/1_bad.sql": {Data: []byte("SELECT 1")}},
			wantErr: "invalid SQLite migration filename",
		},
		{
			name:    "empty SQL",
			files:   fstest.MapFS{"migrations/000001_empty.sql": {Data: []byte(" \n")}},
			wantErr: "is empty",
		},
		{
			name: "duplicate version",
			files: fstest.MapFS{
				"migrations/000001_one.sql": {Data: []byte("SELECT 1")},
				"migrations/000001_two.sql": {Data: []byte("SELECT 2")},
			},
			wantErr: "duplicate SQLite migration version",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(test.files, "SQLite")
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Load error = %v, want containing %q", err, test.wantErr)
			}
		})
	}

	items, err := Load(fstest.MapFS{
		"migrations/000002_second.sql": {Data: []byte("SELECT 2")},
		"migrations/000001_first.sql":  {Data: []byte("SELECT 1")},
	}, "SQLite")
	if err != nil {
		t.Fatalf("load sorted migrations: %v", err)
	}
	if items[0].Version != 1 || items[1].Version != 2 {
		t.Fatalf("migration order = %d, %d", items[0].Version, items[1].Version)
	}
}

func TestValidateApplied(t *testing.T) {
	available := []Asset{
		{Version: 1, Name: "000001_first", Checksum: "one"},
		{Version: 3, Name: "000003_third", Checksum: "three"},
	}
	tests := []struct {
		name    string
		applied []Applied
		wantErr string
	}{
		{name: "valid prefix", applied: []Applied{{Version: 1, Name: "000001_first", Checksum: "one"}}},
		{name: "newer database", applied: []Applied{{Version: 1}, {Version: 3}, {Version: 4}}, wantErr: "newer than this binary"},
		{name: "non-prefix", applied: []Applied{{Version: 3, Name: "000003_third", Checksum: "three"}}, wantErr: "not an embedded prefix"},
		{name: "renamed", applied: []Applied{{Version: 1, Name: "renamed", Checksum: "one"}}, wantErr: "name mismatch"},
		{name: "modified", applied: []Applied{{Version: 1, Name: "000001_first", Checksum: "changed"}}, wantErr: "checksum mismatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateApplied("SQLite", available, test.applied)
			if test.wantErr == "" && err != nil {
				t.Fatalf("ValidateApplied: %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

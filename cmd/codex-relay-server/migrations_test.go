package main

import (
	"strings"
	"testing"
)

func TestParseMigrationFilename(t *testing.T) {
	version, name, err := parseMigrationFilename("001_init.sql")
	if err != nil || version != 1 || name != "init" {
		t.Fatalf("unexpected migration filename result: version=%d name=%q err=%v", version, name, err)
	}
	for _, filename := range []string{"init.sql", "000_init.sql", "001.sql", "001_init.txt"} {
		if _, _, err := parseMigrationFilename(filename); err == nil {
			t.Fatalf("expected invalid migration filename: %s", filename)
		}
	}
}

func TestLoadSQLMigrations(t *testing.T) {
	migrations, err := loadSQLMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if len(migrations) != 3 || migrations[0].version != 1 || migrations[0].name != "init" || migrations[1].version != 2 || migrations[1].name != "user_admin" || migrations[2].version != 3 || migrations[2].name != "port_mappings" {
		t.Fatalf("unexpected migrations: %#v", migrations)
	}
	if len(migrations[0].statements) != 7 {
		t.Fatalf("expected seven initial schema statements, got %d", len(migrations[0].statements))
	}
	if len(migrations[0].checksum) != 64 || strings.Trim(migrations[0].checksum, "0123456789abcdef") != "" {
		t.Fatalf("unexpected migration checksum: %q", migrations[0].checksum)
	}
	if len(migrations[1].statements) != 2 || len(migrations[1].checksum) != 64 || strings.Trim(migrations[1].checksum, "0123456789abcdef") != "" {
		t.Fatalf("unexpected user admin migration: %#v", migrations[1])
	}
	if len(migrations[2].statements) != 1 || len(migrations[2].checksum) != 64 || strings.Trim(migrations[2].checksum, "0123456789abcdef") != "" {
		t.Fatalf("unexpected port mapping migration: %#v", migrations[2])
	}
}

func TestSplitMigrationStatements(t *testing.T) {
	statements := splitMigrationStatements(" CREATE TABLE one (id INT);\n\n; ALTER TABLE one ADD value TEXT; ")
	if len(statements) != 2 || statements[0] != "CREATE TABLE one (id INT)" || statements[1] != "ALTER TABLE one ADD value TEXT" {
		t.Fatalf("unexpected split statements: %#v", statements)
	}
}

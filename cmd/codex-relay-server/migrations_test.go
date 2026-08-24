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

func TestMigrationChecksumAcceptsLegacyLineEndings(t *testing.T) {
	canonical, legacy := migrationChecksums([]byte("CREATE TABLE example (id INT);\r\n"))
	if len(legacy) == 0 {
		t.Fatal("expected a legacy CRLF checksum")
	}
	migration := sqlMigration{checksum: canonical, legacyChecksums: legacy}
	if !migration.matchesChecksum(canonical) {
		t.Fatal("canonical checksum must be accepted")
	}
	for checksum := range legacy {
		if !migration.matchesChecksum(checksum) {
			t.Fatal("legacy CRLF checksum must be accepted")
		}
	}
	if migration.matchesChecksum("not-a-checksum") {
		t.Fatal("unexpected checksum must be rejected")
	}
}

func TestSplitMigrationStatements(t *testing.T) {
	statements := splitMigrationStatements(" CREATE TABLE one (id INT);\n\n; ALTER TABLE one ADD value TEXT; ")
	if len(statements) != 2 || statements[0] != "CREATE TABLE one (id INT)" || statements[1] != "ALTER TABLE one ADD value TEXT" {
		t.Fatalf("unexpected split statements: %#v", statements)
	}
}

func TestRelayStoreCompactsOversizedToolOutput(t *testing.T) {
	store := newRelayStore(t.TempDir())
	stored := store.appendEvent("user", "device", Event{
		SessionID: "thread",
		Type:      "tool.output",
		Payload:   map[string]interface{}{"text": strings.Repeat("界", maxRelayedToolOutputBytes)},
	})
	text := stored.Payload["text"].(string)
	if len(text) > maxRelayedToolOutputBytes || !strings.HasSuffix(text, relayedToolOutputTruncatedNote) {
		t.Fatalf("stored output was not compacted: bytes=%d", len(text))
	}
	if !stored.Payload["truncated"].(bool) {
		t.Fatalf("compacted event should be marked: %#v", stored.Payload)
	}

	history := store.eventsForUser("user", "thread", 0, 6)
	if len(history) != 1 || len(history[0].Payload["text"].(string)) > maxRelayedToolOutputBytes {
		t.Fatalf("history returned oversized output: %#v", history)
	}
}

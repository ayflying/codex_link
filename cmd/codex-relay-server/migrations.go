package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

const (
	migrationLockName = "codex_link_schema_migrations"
	migrationTableSQL = `CREATE TABLE IF NOT EXISTS schema_migrations (
  version BIGINT NOT NULL PRIMARY KEY,
  name VARCHAR(255) NOT NULL,
  checksum CHAR(64) NOT NULL,
  applied_at DATETIME(6) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`
)

//go:embed migrations/*.sql
var mysqlMigrations embed.FS

type sqlMigration struct {
	version         int64
	name            string
	checksum        string
	legacyChecksums map[string]struct{}
	statements      []string
}

type appliedMigration struct {
	version  int64
	name     string
	checksum string
}

func (s *mysqlRelayStore) migrate() (int64, error) {
	migrations, err := loadSQLMigrations()
	if err != nil {
		return 0, err
	}
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("获取 MySQL 迁移连接失败: %w", err)
	}
	defer conn.Close()

	var locked int
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 30)", migrationLockName).Scan(&locked); err != nil {
		return 0, fmt.Errorf("获取 MySQL 迁移锁失败: %w", err)
	}
	if locked != 1 {
		return 0, fmt.Errorf("获取 MySQL 迁移锁超时")
	}
	defer func() { _, _ = conn.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", migrationLockName) }()

	if _, err := conn.ExecContext(ctx, migrationTableSQL); err != nil {
		return 0, fmt.Errorf("创建 MySQL 迁移表失败: %w", err)
	}
	applied, err := readAppliedMigrations(ctx, conn)
	if err != nil {
		return 0, err
	}
	known := make(map[int64]sqlMigration, len(migrations))
	for _, migration := range migrations {
		known[migration.version] = migration
	}
	for _, record := range applied {
		migration, ok := known[record.version]
		if !ok {
			return 0, fmt.Errorf("数据库包含未随当前程序发布的迁移版本 %d", record.version)
		}
		if migration.name != record.name || !migration.matchesChecksum(record.checksum) {
			return 0, fmt.Errorf("迁移版本 %d 的文件内容已变更，请恢复原迁移文件并新增迁移修正", record.version)
		}
	}

	currentVersion := int64(0)
	for _, migration := range migrations {
		if _, ok := applied[migration.version]; ok {
			currentVersion = migration.version
			continue
		}
		for _, statement := range migration.statements {
			if _, err := conn.ExecContext(ctx, statement); err != nil {
				return 0, fmt.Errorf("执行 MySQL 迁移 %d_%s 失败: %w", migration.version, migration.name, err)
			}
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, UTC_TIMESTAMP(6))`, migration.version, migration.name, migration.checksum); err != nil {
			return 0, fmt.Errorf("记录 MySQL 迁移 %d_%s 失败: %w", migration.version, migration.name, err)
		}
		currentVersion = migration.version
	}
	return currentVersion, nil
}

func loadSQLMigrations() ([]sqlMigration, error) {
	entries, err := fs.ReadDir(mysqlMigrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("读取 MySQL 迁移目录失败: %w", err)
	}
	migrations := make([]sqlMigration, 0, len(entries))
	seen := map[int64]struct{}{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, name, err := parseMigrationFilename(entry.Name())
		if err != nil {
			return nil, err
		}
		if _, exists := seen[version]; exists {
			return nil, fmt.Errorf("迁移版本重复: %d", version)
		}
		raw, err := fs.ReadFile(mysqlMigrations, path.Join("migrations", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("读取 MySQL 迁移 %s 失败: %w", entry.Name(), err)
		}
		statements := splitMigrationStatements(string(raw))
		if len(statements) == 0 {
			return nil, fmt.Errorf("MySQL 迁移 %s 没有可执行语句", entry.Name())
		}
		checksum, legacyChecksums := migrationChecksums(raw)
		migrations = append(migrations, sqlMigration{
			version:         version,
			name:            name,
			checksum:        checksum,
			legacyChecksums: legacyChecksums,
			statements:      statements,
		})
		seen[version] = struct{}{}
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	if len(migrations) == 0 {
		return nil, fmt.Errorf("MySQL 迁移目录为空")
	}
	return migrations, nil
}

func (migration sqlMigration) matchesChecksum(checksum string) bool {
	if checksum == migration.checksum {
		return true
	}
	_, ok := migration.legacyChecksums[checksum]
	return ok
}

func migrationChecksums(raw []byte) (string, map[string]struct{}) {
	canonicalRaw := []byte(strings.ReplaceAll(string(raw), "\r\n", "\n"))
	canonicalDigest := sha256.Sum256(canonicalRaw)
	canonical := hex.EncodeToString(canonicalDigest[:])
	legacy := map[string]struct{}{}
	for _, candidate := range [][]byte{
		raw,
		[]byte(strings.ReplaceAll(string(canonicalRaw), "\n", "\r\n")),
	} {
		digest := sha256.Sum256(candidate)
		checksum := hex.EncodeToString(digest[:])
		if checksum != canonical {
			legacy[checksum] = struct{}{}
		}
	}
	return canonical, legacy
}

func parseMigrationFilename(filename string) (int64, string, error) {
	if !strings.HasSuffix(filename, ".sql") {
		return 0, "", fmt.Errorf("MySQL 迁移文件名无效: %s", filename)
	}
	base := strings.TrimSuffix(filename, ".sql")
	separator := strings.IndexByte(base, '_')
	if separator <= 0 || separator == len(base)-1 {
		return 0, "", fmt.Errorf("MySQL 迁移文件名无效: %s", filename)
	}
	version, err := strconv.ParseInt(base[:separator], 10, 64)
	if err != nil || version <= 0 {
		return 0, "", fmt.Errorf("MySQL 迁移版本无效: %s", filename)
	}
	return version, base[separator+1:], nil
}

func splitMigrationStatements(raw string) []string {
	statements := make([]string, 0)
	for _, statement := range strings.Split(raw, ";") {
		statement = strings.TrimSpace(statement)
		if statement != "" {
			statements = append(statements, statement)
		}
	}
	return statements
}

func readAppliedMigrations(ctx context.Context, conn *sql.Conn) (map[int64]appliedMigration, error) {
	rows, err := conn.QueryContext(ctx, "SELECT version, name, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, fmt.Errorf("读取 MySQL 迁移记录失败: %w", err)
	}
	defer rows.Close()
	applied := map[int64]appliedMigration{}
	for rows.Next() {
		var migration appliedMigration
		if err := rows.Scan(&migration.version, &migration.name, &migration.checksum); err != nil {
			return nil, fmt.Errorf("解析 MySQL 迁移记录失败: %w", err)
		}
		if _, exists := applied[migration.version]; exists {
			return nil, fmt.Errorf("数据库迁移记录包含重复版本 %d", migration.version)
		}
		applied[migration.version] = migration
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取 MySQL 迁移记录失败: %w", err)
	}
	return applied, nil
}

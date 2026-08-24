package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-sql-driver/mysql"
)

const userMutationLockName = "codex_link_user_mutation"

type mysqlRelayStore struct {
	db            *sql.DB
	uploadDir     string
	mu            sync.Mutex
	subscribers   map[string]map[chan Event]struct{}
	schemaVersion atomic.Int64
}

type accessTokenRecord struct {
	ID               string
	UserID           string
	Name             string
	Value            string
	Hash             string
	Prefix           string
	CreatedAt        string
	UpdatedAt        string
	RefreshedAt      string
	LastUsedAt       string
	LastUsedDeviceID string
}

type PortMapping struct {
	ID            string `json:"id"`
	UserID        string `json:"userId"`
	DeviceID      string `json:"deviceId"`
	DeviceName    string `json:"deviceName"`
	Name          string `json:"name"`
	TargetHost    string `json:"targetHost"`
	TargetPort    int    `json:"targetPort"`
	ListenPort    int    `json:"listenPort"`
	Protocol      string `json:"protocol"`
	Enabled       bool   `json:"enabled"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
	Listening     bool   `json:"listening"`
	P2PConnected  bool   `json:"p2pConnected"`
	LastError     string `json:"lastError,omitempty"`
	ListenAddress string `json:"listenAddress,omitempty"`
}

type portMappingInput struct {
	DeviceID   string `json:"deviceId"`
	Name       string `json:"name"`
	TargetHost string `json:"targetHost"`
	TargetPort int    `json:"targetPort"`
	ListenPort int    `json:"listenPort"`
	Protocol   string `json:"protocol"`
	Enabled    *bool  `json:"enabled"`
}

func validatePortMappingInput(input portMappingInput) error {
	if strings.TrimSpace(input.DeviceID) == "" {
		return errors.New("请选择目标设备")
	}
	if strings.TrimSpace(input.Name) == "" || len([]rune(strings.TrimSpace(input.Name))) > 120 {
		return errors.New("映射名称不能为空且不能超过 120 个字符")
	}
	if strings.TrimSpace(input.TargetHost) == "" || len([]rune(strings.TrimSpace(input.TargetHost))) > 255 {
		return errors.New("目标地址无效")
	}
	if input.TargetPort < 1 || input.TargetPort > 65535 {
		return errors.New("目标端口必须是 1 到 65535")
	}
	if input.ListenPort < 1 || input.ListenPort > 65535 {
		return errors.New("公开端口必须是 1 到 65535")
	}
	protocol := strings.ToLower(strings.TrimSpace(input.Protocol))
	if protocol != "tcp" {
		return errors.New("当前只支持 TCP 映射")
	}
	return nil
}

func normalizePortMappingInput(input portMappingInput) portMappingInput {
	input.DeviceID = strings.TrimSpace(input.DeviceID)
	input.Name = strings.TrimSpace(input.Name)
	input.TargetHost = strings.TrimSpace(input.TargetHost)
	input.Protocol = strings.ToLower(strings.TrimSpace(input.Protocol))
	if input.TargetHost == "" {
		input.TargetHost = "127.0.0.1"
	}
	if input.Protocol == "" {
		input.Protocol = "tcp"
	}
	if input.Enabled == nil {
		enabled := true
		input.Enabled = &enabled
	}
	return input
}

func newMySQLRelayStore(uploadDir string) (*mysqlRelayStore, error) {
	if err := os.MkdirAll(uploadDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建图片目录失败: %w", err)
	}
	config := mysql.Config{
		User:                 env("DB_USER", "codex_link"),
		Passwd:               os.Getenv("DB_PASSWORD"),
		Net:                  "tcp",
		Addr:                 env("DB_HOST", "mysql") + ":" + env("DB_PORT", "3306"),
		DBName:               env("DB_NAME", "codex_link"),
		AllowNativePasswords: true,
		ParseTime:            true,
		Loc:                  time.UTC,
		Params:               map[string]string{"charset": "utf8mb4", "collation": "utf8mb4_unicode_ci"},
	}
	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("打开 MySQL 连接失败: %w", err)
	}
	db.SetMaxOpenConns(12)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	store := &mysqlRelayStore{db: db, uploadDir: uploadDir, subscribers: map[string]map[chan Event]struct{}{}}
	if err := store.waitReady(); err != nil {
		_ = db.Close()
		return nil, err
	}
	version, err := store.migrate()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	store.schemaVersion.Store(version)
	return store, nil
}

func (s *mysqlRelayStore) waitReady() error {
	var lastErr error
	for attempt := 0; attempt < 30; attempt++ {
		if err := s.db.Ping(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("连接 MySQL 超时: %w", lastErr)
}

func (s *mysqlRelayStore) close() error { return s.db.Close() }

func (s *mysqlRelayStore) ping() error { return s.db.Ping() }

func (s *mysqlRelayStore) register(username, password string) (User, error) {
	username = strings.TrimSpace(username)
	if len([]rune(username)) < 3 || len([]rune(username)) > 48 {
		return User{}, errors.New("用户名长度应为 3 到 48 个字符")
	}
	if len([]rune(password)) < 8 {
		return User{}, errors.New("密码至少 8 个字符")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	user := User{ID: randomID(), Username: username, Salt: randomID(), Iterations: passwordIterations, CreatedAt: now, UpdatedAt: now}
	user.PasswordHash = hashPassword(password, user.Salt, user.Iterations)
	err := s.withUserMutationLock(func(ctx context.Context, conn *sql.Conn) error {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var userCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&userCount); err != nil {
			return err
		}
		user.IsAdmin = userCount == 0
		_, err = tx.ExecContext(ctx, `INSERT INTO users (id, username, username_key, is_admin, password_hash, salt, iterations, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			user.ID, user.Username, strings.ToLower(username), user.IsAdmin, user.PasswordHash, user.Salt, user.Iterations, user.CreatedAt, user.UpdatedAt)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
				return errors.New("用户名已存在")
			}
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *mysqlRelayStore) authenticate(username, password string) (User, bool) {
	var user User
	err := s.db.QueryRow(`SELECT id, username, is_admin, password_hash, salt, iterations, created_at, updated_at FROM users WHERE username_key = ?`, strings.ToLower(strings.TrimSpace(username))).Scan(
		&user.ID, &user.Username, &user.IsAdmin, &user.PasswordHash, &user.Salt, &user.Iterations, &user.CreatedAt, &user.UpdatedAt)
	if err != nil || user.PasswordHash == "" {
		return User{}, false
	}
	iterations := user.Iterations
	if iterations <= 0 {
		iterations = passwordIterations
	}
	candidate := hashPassword(password, user.Salt, iterations)
	return user, subtleCompare(candidate, user.PasswordHash)
}

func (s *mysqlRelayStore) userByID(userID string) (User, bool) {
	var user User
	err := s.db.QueryRow(`SELECT id, username, is_admin, password_hash, salt, iterations, created_at, updated_at FROM users WHERE id = ?`, userID).Scan(
		&user.ID, &user.Username, &user.IsAdmin, &user.PasswordHash, &user.Salt, &user.Iterations, &user.CreatedAt, &user.UpdatedAt)
	return user, err == nil
}

func (s *mysqlRelayStore) withUserMutationLock(fn func(context.Context, *sql.Conn) error) error {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var locked int
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 30)", userMutationLockName).Scan(&locked); err != nil {
		return err
	}
	if locked != 1 {
		return errors.New("用户管理操作正在进行，请稍后重试")
	}
	defer func() { _, _ = conn.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", userMutationLockName) }()
	return fn(ctx, conn)
}

func (s *mysqlRelayStore) listAdminUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT id, username, is_admin, created_at, updated_at FROM users ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		var user User
		if err := rows.Scan(&user.ID, &user.Username, &user.IsAdmin, &user.CreatedAt, &user.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *mysqlRelayStore) setUserAdmin(actorID, userID string, isAdmin bool) error {
	return s.withUserMutationLock(func(ctx context.Context, conn *sql.Conn) error {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := ensureAdmin(tx, ctx, actorID); err != nil {
			return err
		}
		if actorID == userID {
			return errors.New("当前登录账号不能在系统管理中修改")
		}
		var currentAdmin bool
		if err := tx.QueryRowContext(ctx, `SELECT is_admin FROM users WHERE id = ?`, userID).Scan(&currentAdmin); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("用户不存在")
			}
			return err
		}
		if currentAdmin && !isAdmin {
			var adminCount int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin = TRUE`).Scan(&adminCount); err != nil {
				return err
			}
			if adminCount <= 1 {
				return errors.New("系统至少需要一名管理员")
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE users SET is_admin = ?, updated_at = ? WHERE id = ?`, isAdmin, time.Now().UTC().Format(time.RFC3339Nano), userID)
		if err != nil {
			return err
		}
		return tx.Commit()
	})
}

func (s *mysqlRelayStore) deleteAdminUser(actorID, userID string) error {
	return s.withUserMutationLock(func(ctx context.Context, conn *sql.Conn) error {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := ensureAdmin(tx, ctx, actorID); err != nil {
			return err
		}
		if actorID == userID {
			return errors.New("当前登录账号不能在系统管理中修改")
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
		if err != nil {
			return err
		}
		if count, _ := result.RowsAffected(); count == 0 {
			return errors.New("用户不存在")
		}
		var adminCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin = TRUE`).Scan(&adminCount); err != nil {
			return err
		}
		if adminCount == 0 {
			_, err = tx.ExecContext(ctx, `UPDATE users SET is_admin = TRUE WHERE id = (SELECT id FROM (SELECT id FROM users ORDER BY created_at ASC, id ASC LIMIT 1) AS oldest_user)`)
			if err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

func ensureAdmin(tx *sql.Tx, ctx context.Context, userID string) error {
	var isAdmin bool
	if err := tx.QueryRowContext(ctx, `SELECT is_admin FROM users WHERE id = ?`, userID).Scan(&isAdmin); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("当前账号不是管理员")
		}
		return err
	}
	if !isAdmin {
		return errors.New("当前账号不是管理员")
	}
	return nil
}

func (s *mysqlRelayStore) changePassword(userID, current, next string) error {
	if len([]rune(next)) < 8 {
		return errors.New("新密码至少 8 个字符")
	}
	user, ok := s.userByID(userID)
	if !ok || !subtleCompare(hashPassword(current, user.Salt, user.Iterations), user.PasswordHash) {
		return errors.New("当前密码不正确")
	}
	salt := randomID()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`UPDATE users SET password_hash = ?, salt = ?, iterations = ?, updated_at = ? WHERE id = ?`, hashPassword(next, salt, passwordIterations), salt, passwordIterations, now, userID)
	return err
}

func (s *mysqlRelayStore) createWebSession(userID string) (string, error) {
	token := randomToken(32)
	_, err := s.db.Exec(`INSERT INTO web_sessions (token_hash, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`, hashToken(token), userID, time.Now().UTC().Add(30*24*time.Hour), time.Now().UTC())
	return token, err
}

func (s *mysqlRelayStore) removeWebSession(token string) {
	_, _ = s.db.Exec(`DELETE FROM web_sessions WHERE token_hash = ?`, hashToken(token))
}

func (s *mysqlRelayStore) userForWebSession(token string) (User, bool) {
	var user User
	err := s.db.QueryRow(`SELECT u.id, u.username, u.is_admin, u.password_hash, u.salt, u.iterations, u.created_at, u.updated_at FROM web_sessions ws JOIN users u ON u.id = ws.user_id WHERE ws.token_hash = ? AND ws.expires_at > UTC_TIMESTAMP(6)`, hashToken(token)).Scan(
		&user.ID, &user.Username, &user.IsAdmin, &user.PasswordHash, &user.Salt, &user.Iterations, &user.CreatedAt, &user.UpdatedAt)
	return user, err == nil
}

func (s *mysqlRelayStore) createToken(userID, name string) (accessTokenRecord, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Codex Token " + time.Now().Format("20060102-150405")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	token := "crs_" + randomToken(32)
	record := accessTokenRecord{ID: randomID(), UserID: userID, Name: name, Value: token, Hash: hashToken(token), Prefix: token[:min(12, len(token))], CreatedAt: now, UpdatedAt: now}
	_, err := s.db.Exec(`INSERT INTO access_tokens (id, user_id, name, token_value, token_hash, token_prefix, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, record.UserID, record.Name, record.Value, record.Hash, record.Prefix, record.CreatedAt, record.UpdatedAt)
	return record, err
}

func (s *mysqlRelayStore) listTokens(userID string) ([]accessTokenRecord, error) {
	rows, err := s.db.Query(`SELECT id, user_id, name, token_value, token_hash, token_prefix, created_at, updated_at, COALESCE(refreshed_at, ''), COALESCE(last_used_at, ''), COALESCE(last_used_device_id, '') FROM access_tokens WHERE user_id = ? ORDER BY updated_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []accessTokenRecord{}
	for rows.Next() {
		var record accessTokenRecord
		if err := rows.Scan(&record.ID, &record.UserID, &record.Name, &record.Value, &record.Hash, &record.Prefix, &record.CreatedAt, &record.UpdatedAt, &record.RefreshedAt, &record.LastUsedAt, &record.LastUsedDeviceID); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func tokenJSON(record accessTokenRecord) map[string]interface{} {
	return map[string]interface{}{"id": record.ID, "name": record.Name, "token": record.Value, "prefix": record.Prefix, "createdAt": record.CreatedAt, "updatedAt": record.UpdatedAt, "refreshedAt": record.RefreshedAt, "lastUsedAt": record.LastUsedAt, "lastUsedDeviceId": record.LastUsedDeviceID}
}

func (s *mysqlRelayStore) refreshToken(userID, tokenID string) (accessTokenRecord, error) {
	token := "crs_" + randomToken(32)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.Exec(`UPDATE access_tokens SET token_value = ?, token_hash = ?, token_prefix = ?, updated_at = ?, refreshed_at = ? WHERE id = ? AND user_id = ?`, token, hashToken(token), token[:min(12, len(token))], now, now, tokenID, userID)
	if err != nil {
		return accessTokenRecord{}, err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return accessTokenRecord{}, errors.New("Token 不存在")
	}
	var record accessTokenRecord
	err = s.db.QueryRow(`SELECT id, user_id, name, token_value, token_hash, token_prefix, created_at, updated_at, COALESCE(refreshed_at, ''), COALESCE(last_used_at, ''), COALESCE(last_used_device_id, '') FROM access_tokens WHERE id = ? AND user_id = ?`, tokenID, userID).Scan(&record.ID, &record.UserID, &record.Name, &record.Value, &record.Hash, &record.Prefix, &record.CreatedAt, &record.UpdatedAt, &record.RefreshedAt, &record.LastUsedAt, &record.LastUsedDeviceID)
	return record, err
}

func (s *mysqlRelayStore) deleteToken(userID, tokenID string) error {
	result, err := s.db.Exec(`DELETE FROM access_tokens WHERE id = ? AND user_id = ?`, tokenID, userID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return errors.New("Token 不存在")
	}
	return nil
}

func (s *mysqlRelayStore) userForAPIToken(token string) (User, accessTokenRecord, bool) {
	hash := hashToken(token)
	var user User
	var record accessTokenRecord
	err := s.db.QueryRow(`SELECT u.id, u.username, u.is_admin, u.password_hash, u.salt, u.iterations, u.created_at, u.updated_at, t.id, t.user_id, t.name, t.token_value, t.token_hash, t.token_prefix, t.created_at, t.updated_at, COALESCE(t.refreshed_at, ''), COALESCE(t.last_used_at, ''), COALESCE(t.last_used_device_id, '') FROM access_tokens t JOIN users u ON u.id = t.user_id WHERE t.token_hash = ?`, hash).Scan(
		&user.ID, &user.Username, &user.IsAdmin, &user.PasswordHash, &user.Salt, &user.Iterations, &user.CreatedAt, &user.UpdatedAt, &record.ID, &record.UserID, &record.Name, &record.Value, &record.Hash, &record.Prefix, &record.CreatedAt, &record.UpdatedAt, &record.RefreshedAt, &record.LastUsedAt, &record.LastUsedDeviceID)
	if err != nil {
		return User{}, accessTokenRecord{}, false
	}
	return user, record, true
}

func (s *mysqlRelayStore) markTokenUsed(tokenID, deviceID string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = s.db.Exec(`UPDATE access_tokens SET last_used_at = ?, last_used_device_id = ? WHERE id = ?`, now, deviceID, tokenID)
}

func (s *mysqlRelayStore) loginDevice(userID, tokenID, tokenPrefix, deviceID, name string) (Device, error) {
	if deviceID == "" {
		deviceID = randomID()
	}
	if strings.TrimSpace(name) == "" {
		name = "Codex 客户端"
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return Device{}, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO devices (id, user_id, token_id, name, token_prefix, created_at, updated_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE user_id = VALUES(user_id), token_id = VALUES(token_id), name = VALUES(name), token_prefix = VALUES(token_prefix), updated_at = VALUES(updated_at), last_seen_at = VALUES(last_seen_at)`, deviceID, userID, tokenID, name, tokenPrefix, now, now, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "foreign key") {
			return Device{}, errors.New("Token 无效或已删除")
		}
		return Device{}, err
	}
	if _, err = tx.Exec(`UPDATE access_tokens SET last_used_at = ?, last_used_device_id = ? WHERE id = ? AND user_id = ?`, now, deviceID, tokenID, userID); err != nil {
		return Device{}, err
	}
	if err = tx.Commit(); err != nil {
		return Device{}, err
	}
	return Device{ID: deviceID, UserID: userID, TokenID: tokenID, TokenPrefix: tokenPrefix, Name: name, CreatedAt: now, UpdatedAt: now, LastSeenAt: now}, nil
}

func (s *mysqlRelayStore) verifyAgent(deviceID, token string) (Device, accessTokenRecord, bool) {
	user, tokenRecord, ok := s.userForAPIToken(token)
	if !ok {
		return Device{}, accessTokenRecord{}, false
	}
	var device Device
	err := s.db.QueryRow(`SELECT id, user_id, COALESCE(token_id, ''), name, COALESCE(token_prefix, ''), created_at, updated_at, COALESCE(last_seen_at, '') FROM devices WHERE id = ? AND user_id = ? AND token_id = ?`, deviceID, user.ID, tokenRecord.ID).Scan(&device.ID, &device.UserID, &device.TokenID, &device.Name, &device.TokenPrefix, &device.CreatedAt, &device.UpdatedAt, &device.LastSeenAt)
	if err != nil {
		return Device{}, accessTokenRecord{}, false
	}
	s.markTokenUsed(tokenRecord.ID, deviceID)
	return device, tokenRecord, true
}

func (s *mysqlRelayStore) devicesForUser(userID string, online func(string) bool) []map[string]interface{} {
	rows, err := s.db.Query(`SELECT d.id, d.name, d.token_id, COALESCE(t.name, ''), COALESCE(d.token_prefix, ''), d.created_at, d.updated_at, COALESCE(d.last_seen_at, '') FROM devices d LEFT JOIN access_tokens t ON t.id = d.token_id WHERE d.user_id = ? ORDER BY d.updated_at DESC`, userID)
	if err != nil {
		return []map[string]interface{}{}
	}
	defer rows.Close()
	result := []map[string]interface{}{}
	for rows.Next() {
		var id, name, tokenID, tokenName, prefix, createdAt, updatedAt, lastSeenAt string
		if rows.Scan(&id, &name, &tokenID, &tokenName, &prefix, &createdAt, &updatedAt, &lastSeenAt) != nil {
			continue
		}
		result = append(result, map[string]interface{}{"id": id, "name": name, "tokenId": tokenID, "tokenName": tokenName, "tokenPrefix": prefix, "online": online(id), "createdAt": createdAt, "updatedAt": updatedAt, "lastSeenAt": lastSeenAt})
	}
	return result
}

func (s *mysqlRelayStore) listPortMappings(userID string) ([]PortMapping, error) {
	return s.queryPortMappings(`WHERE p.user_id = ?`, userID)
}

func (s *mysqlRelayStore) listAllPortMappings() ([]PortMapping, error) {
	return s.queryPortMappings("", nil)
}

func (s *mysqlRelayStore) queryPortMappings(where string, arg interface{}) ([]PortMapping, error) {
	query := `SELECT p.id, p.user_id, p.device_id, d.name, p.name, p.target_host, p.target_port, p.listen_port, p.protocol, p.enabled, p.created_at, p.updated_at
		FROM port_mappings p JOIN devices d ON d.id = p.device_id ` + where + ` ORDER BY p.created_at ASC, p.id ASC`
	var rows *sql.Rows
	var err error
	if arg == nil {
		rows, err = s.db.Query(query)
	} else {
		rows, err = s.db.Query(query, arg)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PortMapping{}
	for rows.Next() {
		var mapping PortMapping
		if err := rows.Scan(&mapping.ID, &mapping.UserID, &mapping.DeviceID, &mapping.DeviceName, &mapping.Name, &mapping.TargetHost, &mapping.TargetPort, &mapping.ListenPort, &mapping.Protocol, &mapping.Enabled, &mapping.CreatedAt, &mapping.UpdatedAt); err != nil {
			return nil, err
		}
		mapping.ListenAddress = fmt.Sprintf("0.0.0.0:%d", mapping.ListenPort)
		result = append(result, mapping)
	}
	return result, rows.Err()
}

func (s *mysqlRelayStore) getPortMapping(userID, mappingID string) (PortMapping, error) {
	mappings, err := s.listPortMappings(userID)
	if err != nil {
		return PortMapping{}, err
	}
	for _, mapping := range mappings {
		if mapping.ID == mappingID {
			return mapping, nil
		}
	}
	return PortMapping{}, errors.New("端口映射不存在")
}

func (s *mysqlRelayStore) createPortMapping(userID string, input portMappingInput) (PortMapping, error) {
	input = normalizePortMappingInput(input)
	if err := validatePortMappingInput(input); err != nil {
		return PortMapping{}, err
	}
	var found int
	if err := s.db.QueryRow(`SELECT 1 FROM devices WHERE id = ? AND user_id = ?`, input.DeviceID, userID).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PortMapping{}, errors.New("目标设备不存在或不属于当前用户")
		}
		return PortMapping{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id := randomID()
	_, err := s.db.Exec(`INSERT INTO port_mappings (id, user_id, device_id, name, target_host, target_port, listen_port, protocol, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, userID, input.DeviceID, input.Name, input.TargetHost, input.TargetPort, input.ListenPort, input.Protocol, *input.Enabled, now, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return PortMapping{}, errors.New("公开端口已经被其他映射占用")
		}
		return PortMapping{}, err
	}
	return s.getPortMapping(userID, id)
}

func (s *mysqlRelayStore) updatePortMapping(userID, mappingID string, input portMappingInput) (PortMapping, error) {
	input = normalizePortMappingInput(input)
	if err := validatePortMappingInput(input); err != nil {
		return PortMapping{}, err
	}
	if _, err := s.getPortMapping(userID, mappingID); err != nil {
		return PortMapping{}, err
	}
	var found int
	if err := s.db.QueryRow(`SELECT 1 FROM devices WHERE id = ? AND user_id = ?`, input.DeviceID, userID).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PortMapping{}, errors.New("目标设备不存在或不属于当前用户")
		}
		return PortMapping{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`UPDATE port_mappings SET device_id = ?, name = ?, target_host = ?, target_port = ?, listen_port = ?, protocol = ?, enabled = ?, updated_at = ? WHERE id = ? AND user_id = ?`, input.DeviceID, input.Name, input.TargetHost, input.TargetPort, input.ListenPort, input.Protocol, *input.Enabled, now, mappingID, userID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return PortMapping{}, errors.New("公开端口已经被其他映射占用")
		}
		return PortMapping{}, err
	}
	return s.getPortMapping(userID, mappingID)
}

func (s *mysqlRelayStore) deletePortMapping(userID, mappingID string) error {
	result, err := s.db.Exec(`DELETE FROM port_mappings WHERE id = ? AND user_id = ?`, mappingID, userID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return errors.New("端口映射不存在")
	}
	return nil
}

func (s *mysqlRelayStore) deviceOwnedBy(userID, deviceID string) bool {
	var found int
	err := s.db.QueryRow(`SELECT 1 FROM devices WHERE id = ? AND user_id = ?`, deviceID, userID).Scan(&found)
	return err == nil && found == 1
}

func (s *mysqlRelayStore) deleteDevice(userID, deviceID string) error {
	result, err := s.db.Exec(`DELETE FROM devices WHERE id = ? AND user_id = ?`, deviceID, userID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return errors.New("设备不存在")
	}
	return nil
}

func (s *mysqlRelayStore) upsertSessions(userID, deviceID string, sessions []Session) {
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	for _, session := range sessions {
		if session.ID == "" {
			continue
		}
		if _, err = tx.Exec(`INSERT INTO sessions (user_id, id, device_id, title, mode, status, created_at, updated_at, cwd, note) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE device_id = VALUES(device_id), title = VALUES(title), mode = VALUES(mode), status = VALUES(status), created_at = VALUES(created_at), updated_at = VALUES(updated_at), cwd = VALUES(cwd), note = VALUES(note)`, userID, session.ID, deviceID, session.Title, session.Mode, session.Status, session.CreatedAt, session.UpdatedAt, session.Cwd, session.Note); err != nil {
			return
		}
	}
	_ = tx.Commit()
}

func (s *mysqlRelayStore) removeSession(userID, sessionID string) {
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	if _, err = tx.Exec(`DELETE FROM events WHERE user_id = ? AND session_id = ?`, userID, sessionID); err == nil {
		_, err = tx.Exec(`DELETE FROM sessions WHERE user_id = ? AND id = ?`, userID, sessionID)
	}
	if err != nil {
		_ = tx.Rollback()
		return
	}
	_ = tx.Commit()
}

func (s *mysqlRelayStore) clearEvents(userID, sessionID string) {
	_, _ = s.db.Exec(`DELETE FROM events WHERE user_id = ? AND session_id = ?`, userID, sessionID)
}

func (s *mysqlRelayStore) sessionDevice(userID, sessionID string) string {
	var deviceID string
	_ = s.db.QueryRow(`SELECT device_id FROM sessions WHERE user_id = ? AND id = ?`, userID, sessionID).Scan(&deviceID)
	return deviceID
}

func (s *mysqlRelayStore) sessionsForUser(userID, deviceID string) []Session {
	query := `SELECT id, title, mode, status, created_at, updated_at, COALESCE(cwd, ''), COALESCE(note, '') FROM sessions WHERE user_id = ?`
	args := []interface{}{userID}
	if deviceID != "" {
		query += ` AND device_id = ?`
		args = append(args, deviceID)
	}
	query += ` ORDER BY updated_at DESC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return []Session{}
	}
	defer rows.Close()
	result := []Session{}
	for rows.Next() {
		var session Session
		if rows.Scan(&session.ID, &session.Title, &session.Mode, &session.Status, &session.CreatedAt, &session.UpdatedAt, &session.Cwd, &session.Note) == nil {
			result = append(result, session)
		}
	}
	return result
}

func (s *mysqlRelayStore) appendEvent(userID, deviceID string, event Event) Event {
	if event.TS == "" {
		event.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if event.Payload == nil {
		event.Payload = map[string]interface{}{}
	}
	payload, _ := json.Marshal(event.Payload)
	result, err := s.db.Exec(`INSERT INTO events (user_id, device_id, session_id, event_type, event_ts, payload_json) VALUES (?, ?, ?, ?, ?, ?)`, userID, deviceID, event.SessionID, event.Type, event.TS, payload)
	if err != nil {
		return event
	}
	event.ID, _ = result.LastInsertId()
	s.mu.Lock()
	channels := make([]chan Event, 0, len(s.subscribers[userID]))
	for channel := range s.subscribers[userID] {
		channels = append(channels, channel)
	}
	s.mu.Unlock()
	for _, channel := range channels {
		select {
		case channel <- event:
		default:
		}
	}
	return event
}

func (s *mysqlRelayStore) eventsForUser(userID, sessionID string, after int64, limit int) []Event {
	rows, err := s.db.Query(`SELECT id, event_type, event_ts, payload_json FROM events WHERE user_id = ? AND session_id = ? AND id > ? ORDER BY id DESC LIMIT ?`, userID, sessionID, after, limit)
	if err != nil {
		return []Event{}
	}
	defer rows.Close()
	result := []Event{}
	for rows.Next() {
		var event Event
		var payload string
		if rows.Scan(&event.ID, &event.Type, &event.TS, &payload) != nil {
			continue
		}
		if json.Unmarshal([]byte(payload), &event.Payload) != nil {
			event.Payload = map[string]interface{}{}
		}
		event.SessionID = sessionID
		result = append(result, event)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (s *mysqlRelayStore) subscribe(userID string) chan Event {
	channel := make(chan Event, 128)
	s.mu.Lock()
	if s.subscribers[userID] == nil {
		s.subscribers[userID] = map[chan Event]struct{}{}
	}
	s.subscribers[userID][channel] = struct{}{}
	s.mu.Unlock()
	return channel
}

func (s *mysqlRelayStore) unsubscribe(userID string, channel chan Event) {
	s.mu.Lock()
	delete(s.subscribers[userID], channel)
	s.mu.Unlock()
}

func (s *mysqlRelayStore) saveUpload(userID string, attachment Attachment, dataURL string) (Attachment, error) {
	if !strings.HasPrefix(strings.ToLower(attachment.MimeType), "image/") {
		return Attachment{}, errors.New("只支持图片附件")
	}
	comma := strings.Index(dataURL, ",")
	if comma < 0 || !strings.Contains(strings.ToLower(dataURL[:comma]), ";base64") {
		return Attachment{}, errors.New("图片数据无效")
	}
	raw, err := base64.StdEncoding.DecodeString(dataURL[comma+1:])
	if err != nil || len(raw) == 0 || len(raw) > maxImageBytes {
		return Attachment{}, errors.New("图片数据无效或超过 10MB")
	}
	attachment.ID = randomID()
	attachment.Path = filepath.Join(s.uploadDir, attachment.ID+".bin")
	attachment.DataURL = ""
	attachment.URL = "/api/uploads/" + attachment.ID
	if attachment.Name == "" {
		attachment.Name = "image.png"
	}
	if err := os.WriteFile(attachment.Path, raw, 0o600); err != nil {
		return Attachment{}, fmt.Errorf("保存图片失败: %w", err)
	}
	_, err = s.db.Exec(`INSERT INTO uploads (id, user_id, name, mime_type, file_path, file_size, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, attachment.ID, userID, attachment.Name, attachment.MimeType, attachment.Path, len(raw), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		_ = os.Remove(attachment.Path)
		return Attachment{}, err
	}
	attachment.Path = ""
	return attachment, nil
}

func (s *mysqlRelayStore) upload(userID, id string) (storedUpload, bool) {
	var upload storedUpload
	var path, name, mimeType string
	err := s.db.QueryRow(`SELECT name, mime_type, file_path FROM uploads WHERE id = ? AND user_id = ?`, id, userID).Scan(&name, &mimeType, &path)
	if err != nil {
		return storedUpload{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return storedUpload{}, false
	}
	upload.UserID = userID
	upload.Attachment = Attachment{ID: id, Name: name, MimeType: mimeType, URL: "/api/uploads/" + id}
	upload.DataURL = "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(raw)
	return upload, true
}

func (s *mysqlRelayStore) resolveAttachments(userID string, attachments []Attachment) ([]Attachment, error) {
	result := make([]Attachment, 0, len(attachments))
	for _, attachment := range attachments {
		if attachment.ID == "" {
			return nil, errors.New("图片附件缺少 ID")
		}
		upload, ok := s.upload(userID, attachment.ID)
		if !ok {
			return nil, errors.New("图片附件不存在或不属于当前用户")
		}
		item := upload.Attachment
		item.DataURL = upload.DataURL
		result = append(result, item)
	}
	return result, nil
}

func subtleCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var result byte
	for index := range a {
		result |= a[index] ^ b[index]
	}
	return result == 0
}

func tokenListJSON(records []accessTokenRecord) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(records))
	for _, record := range records {
		result = append(result, tokenJSON(record))
	}
	return result
}

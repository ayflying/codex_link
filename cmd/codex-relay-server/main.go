package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	authCookieName            = "codex_relay_session"
	passwordIterations        = 120000
	defaultEventBacklog       = 120
	maxImageBytes             = 10 * 1024 * 1024
	defaultRequestTimeoutSecs = 35
)

type Session struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Mode      string `json:"mode"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	Cwd       string `json:"cwd,omitempty"`
	Note      string `json:"note,omitempty"`
}

type Event struct {
	ID        int64                  `json:"id,omitempty"`
	SessionID string                 `json:"sessionId"`
	Type      string                 `json:"type"`
	TS        string                 `json:"ts"`
	Payload   map[string]interface{} `json:"payload"`
}

type Attachment struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Path     string `json:"path,omitempty"`
	URL      string `json:"url,omitempty"`
	DataURL  string `json:"dataUrl,omitempty"`
}

type AppSettings struct {
	ApprovalMode string `json:"approvalMode"`
	WorkMode     string `json:"workMode"`
}

type envelope struct {
	Type      string          `json:"type"`
	RequestID string          `json:"requestId,omitempty"`
	Action    string          `json:"action,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Error     string          `json:"error,omitempty"`
}

type User struct {
	ID                string `json:"id"`
	Username          string `json:"username"`
	PasswordHash      string `json:"passwordHash"`
	Salt              string `json:"salt"`
	Iterations        int    `json:"iterations"`
	CreatedAt         string `json:"createdAt"`
	UpdatedAt         string `json:"updatedAt"`
	APITokenHash      string `json:"apiTokenHash,omitempty"`
	APITokenPrefix    string `json:"apiTokenPrefix,omitempty"`
	APITokenCreatedAt string `json:"apiTokenCreatedAt,omitempty"`
}

type Device struct {
	ID          string `json:"id"`
	UserID      string `json:"userId"`
	Name        string `json:"name"`
	TokenID     string `json:"tokenId,omitempty"`
	TokenName   string `json:"tokenName,omitempty"`
	TokenHash   string `json:"tokenHash,omitempty"`
	TokenPrefix string `json:"tokenPrefix,omitempty"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
	LastSeenAt  string `json:"lastSeenAt,omitempty"`
}

type ownedSession struct {
	UserID   string  `json:"userId"`
	DeviceID string  `json:"deviceId"`
	Session  Session `json:"session"`
}

type ownedEvent struct {
	UserID   string `json:"userId"`
	DeviceID string `json:"deviceId"`
	Event    Event  `json:"event"`
}

type storedUpload struct {
	UserID     string     `json:"userId"`
	Attachment Attachment `json:"attachment"`
	DataURL    string     `json:"dataUrl"`
}

type relayFile struct {
	Users    []User         `json:"users"`
	Devices  []Device       `json:"devices"`
	Sessions []ownedSession `json:"sessions"`
	Events   []ownedEvent   `json:"events"`
	Uploads  []storedUpload `json:"uploads"`
	NextID   int64          `json:"nextId"`
}

type relayStore struct {
	mu          sync.Mutex
	path        string
	users       map[string]User
	userNames   map[string]string
	devices     map[string]Device
	sessions    map[string]ownedSession
	events      []ownedEvent
	uploads     map[string]storedUpload
	nextID      int64
	subscribers map[string]map[chan Event]struct{}
}

func newRelayStore(dataDir string) *relayStore {
	_ = os.MkdirAll(dataDir, 0o700)
	store := &relayStore{
		path:        filepath.Join(dataDir, "relay-store.json"),
		users:       map[string]User{},
		userNames:   map[string]string{},
		devices:     map[string]Device{},
		sessions:    map[string]ownedSession{},
		uploads:     map[string]storedUpload{},
		subscribers: map[string]map[chan Event]struct{}{},
	}
	store.load()
	return store
}

func (s *relayStore) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var file relayFile
	if json.Unmarshal(raw, &file) != nil {
		return
	}
	for _, user := range file.Users {
		s.users[user.ID] = user
		s.userNames[strings.ToLower(user.Username)] = user.ID
	}
	for _, device := range file.Devices {
		s.devices[device.ID] = device
	}
	for _, session := range file.Sessions {
		s.sessions[sessionKey(session.UserID, session.Session.ID)] = session
	}
	for _, upload := range file.Uploads {
		s.uploads[upload.Attachment.ID] = upload
	}
	s.events = file.Events
	s.nextID = file.NextID
	for _, event := range file.Events {
		if event.Event.ID > s.nextID {
			s.nextID = event.Event.ID
		}
	}
}

func (s *relayStore) persistLocked() {
	file := relayFile{Events: s.events, NextID: s.nextID}
	for _, user := range s.users {
		file.Users = append(file.Users, user)
	}
	for _, device := range s.devices {
		file.Devices = append(file.Devices, device)
	}
	for _, session := range s.sessions {
		file.Sessions = append(file.Sessions, session)
	}
	for _, upload := range s.uploads {
		file.Uploads = append(file.Uploads, upload)
	}
	sort.Slice(file.Users, func(i, j int) bool { return file.Users[i].Username < file.Users[j].Username })
	sort.Slice(file.Devices, func(i, j int) bool { return file.Devices[i].UpdatedAt > file.Devices[j].UpdatedAt })
	sort.Slice(file.Sessions, func(i, j int) bool { return file.Sessions[i].Session.UpdatedAt > file.Sessions[j].Session.UpdatedAt })
	raw, _ := json.MarshalIndent(file, "", "  ")
	temporary := s.path + ".tmp"
	if os.WriteFile(temporary, raw, 0o600) == nil {
		_ = os.Rename(temporary, s.path)
	}
}

func (s *relayStore) userCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.users)
}

func (s *relayStore) register(username, password string) (User, error) {
	username = strings.TrimSpace(username)
	if len([]rune(username)) < 3 || len([]rune(username)) > 48 {
		return User{}, errors.New("用户名长度应为 3 到 48 个字符")
	}
	if len([]rune(password)) < 8 {
		return User{}, errors.New("密码至少 8 个字符")
	}
	key := strings.ToLower(username)
	now := time.Now().Format(time.RFC3339)
	user := User{ID: randomID(), Username: username, Salt: randomID(), Iterations: passwordIterations, CreatedAt: now, UpdatedAt: now}
	user.PasswordHash = hashPassword(password, user.Salt, user.Iterations)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.userNames[key]; exists {
		return User{}, errors.New("用户名已存在")
	}
	s.users[user.ID] = user
	s.userNames[key] = user.ID
	s.persistLocked()
	return user, nil
}

func (s *relayStore) authenticate(username, password string) (User, bool) {
	s.mu.Lock()
	id := s.userNames[strings.ToLower(strings.TrimSpace(username))]
	user, ok := s.users[id]
	s.mu.Unlock()
	if !ok || user.PasswordHash == "" {
		return User{}, false
	}
	iterations := user.Iterations
	if iterations <= 0 {
		iterations = passwordIterations
	}
	candidate := hashPassword(password, user.Salt, iterations)
	return user, subtle.ConstantTimeCompare([]byte(candidate), []byte(user.PasswordHash)) == 1
}

func (s *relayStore) changePassword(userID, current, next string) error {
	if len([]rune(next)) < 8 {
		return errors.New("新密码至少 8 个字符")
	}
	s.mu.Lock()
	user, ok := s.users[userID]
	s.mu.Unlock()
	if !ok {
		return errors.New("用户不存在")
	}
	if hashPassword(current, user.Salt, user.Iterations) != user.PasswordHash {
		return errors.New("当前密码不正确")
	}
	user.Salt = randomID()
	user.Iterations = passwordIterations
	user.PasswordHash = hashPassword(next, user.Salt, user.Iterations)
	user.UpdatedAt = time.Now().Format(time.RFC3339)
	s.mu.Lock()
	s.users[user.ID] = user
	s.persistLocked()
	s.mu.Unlock()
	return nil
}

func (s *relayStore) rotateAPIToken(userID string) (string, map[string]interface{}, error) {
	token := "crs_" + randomToken(32)
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[userID]
	if !ok {
		return "", nil, errors.New("用户不存在")
	}
	user.APITokenHash = hashToken(token)
	user.APITokenPrefix = token[:min(12, len(token))]
	user.APITokenCreatedAt = time.Now().Format(time.RFC3339)
	s.users[userID] = user
	s.persistLocked()
	return token, apiTokenStatus(user), nil
}

func (s *relayStore) clearAPIToken(userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[userID]
	if !ok {
		return
	}
	user.APITokenHash = ""
	user.APITokenPrefix = ""
	user.APITokenCreatedAt = ""
	s.users[userID] = user
	s.persistLocked()
}

func (s *relayStore) apiTokenStatus(userID string) map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return apiTokenStatus(s.users[userID])
}

func apiTokenStatus(user User) map[string]interface{} {
	return map[string]interface{}{"enabled": user.APITokenHash != "", "prefix": user.APITokenPrefix, "createdAt": user.APITokenCreatedAt}
}

func (s *relayStore) userForAPIToken(token string) (User, bool) {
	hash := hashToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, user := range s.users {
		if user.APITokenHash != "" && subtle.ConstantTimeCompare([]byte(hash), []byte(user.APITokenHash)) == 1 {
			return user, true
		}
	}
	return User{}, false
}

func (s *relayStore) loginDevice(userID, deviceID, name string) (Device, string, error) {
	if deviceID == "" {
		deviceID = randomID()
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Codex 客户端"
	}
	token := "cra_" + randomToken(32)
	now := time.Now().Format(time.RFC3339)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.devices[deviceID]; ok && existing.UserID != userID {
		return Device{}, "", errors.New("设备 ID 已被其他用户使用")
	}
	device := s.devices[deviceID]
	if device.ID == "" {
		device = Device{ID: deviceID, UserID: userID, CreatedAt: now}
	}
	device.Name = name
	device.TokenHash = hashToken(token)
	device.TokenPrefix = token[:min(12, len(token))]
	device.UpdatedAt = now
	s.devices[deviceID] = device
	s.persistLocked()
	return device, token, nil
}

func (s *relayStore) verifyDevice(deviceID, token string) (Device, bool) {
	s.mu.Lock()
	device, ok := s.devices[deviceID]
	s.mu.Unlock()
	if !ok || device.TokenHash == "" {
		return Device{}, false
	}
	return device, subtle.ConstantTimeCompare([]byte(hashToken(token)), []byte(device.TokenHash)) == 1
}

func (s *relayStore) devicesForUser(userID string, online func(string) bool) []map[string]interface{} {
	s.mu.Lock()
	devices := []Device{}
	for _, device := range s.devices {
		if device.UserID == userID {
			devices = append(devices, device)
		}
	}
	s.mu.Unlock()
	sort.Slice(devices, func(i, j int) bool { return devices[i].UpdatedAt > devices[j].UpdatedAt })
	result := make([]map[string]interface{}, 0, len(devices))
	for _, device := range devices {
		result = append(result, map[string]interface{}{"id": device.ID, "name": device.Name, "online": online(device.ID), "updatedAt": device.UpdatedAt, "createdAt": device.CreatedAt})
	}
	return result
}

func (s *relayStore) upsertSessions(userID, deviceID string, sessions []Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, session := range sessions {
		if session.ID == "" {
			continue
		}
		s.sessions[sessionKey(userID, session.ID)] = ownedSession{UserID: userID, DeviceID: deviceID, Session: session}
	}
	s.persistLocked()
}

func (s *relayStore) removeSession(userID, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionKey(userID, sessionID))
	s.events = filterOwnedEvents(s.events, func(event ownedEvent) bool { return event.UserID != userID || event.Event.SessionID != sessionID })
	s.persistLocked()
}

func (s *relayStore) clearEvents(userID, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = filterOwnedEvents(s.events, func(event ownedEvent) bool { return event.UserID != userID || event.Event.SessionID != sessionID })
	s.persistLocked()
}

func (s *relayStore) sessionDevice(userID, sessionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[sessionKey(userID, sessionID)].DeviceID
}

func (s *relayStore) sessionsForUser(userID, deviceID string) []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []Session{}
	for _, session := range s.sessions {
		if session.UserID == userID && (deviceID == "" || session.DeviceID == deviceID) {
			result = append(result, session.Session)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt > result[j].UpdatedAt })
	return result
}

func (s *relayStore) appendEvent(userID, deviceID string, event Event) Event {
	if event.TS == "" {
		event.TS = time.Now().Format(time.RFC3339)
	}
	if event.Payload == nil {
		event.Payload = map[string]interface{}{}
	}
	s.mu.Lock()
	s.nextID++
	event.ID = s.nextID
	s.events = append(s.events, ownedEvent{UserID: userID, DeviceID: deviceID, Event: event})
	if len(s.events) > 5000 {
		s.events = s.events[len(s.events)-5000:]
	}
	s.persistLocked()
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

func (s *relayStore) eventsForUser(userID, sessionID string, after int64, limit int) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []Event{}
	for _, owned := range s.events {
		if owned.UserID == userID && owned.Event.SessionID == sessionID && owned.Event.ID > after {
			result = append(result, owned.Event)
		}
	}
	if len(result) > limit {
		return result[len(result)-limit:]
	}
	return result
}

func (s *relayStore) subscribe(userID string) chan Event {
	channel := make(chan Event, 128)
	s.mu.Lock()
	if s.subscribers[userID] == nil {
		s.subscribers[userID] = map[chan Event]struct{}{}
	}
	s.subscribers[userID][channel] = struct{}{}
	s.mu.Unlock()
	return channel
}

func (s *relayStore) unsubscribe(userID string, channel chan Event) {
	s.mu.Lock()
	delete(s.subscribers[userID], channel)
	close(channel)
	s.mu.Unlock()
}

func (s *relayStore) saveUpload(userID string, attachment Attachment, dataURL string) (Attachment, error) {
	if !strings.HasPrefix(strings.ToLower(attachment.MimeType), "image/") {
		return Attachment{}, errors.New("只支持图片附件")
	}
	if size := decodedDataURLSize(dataURL); size <= 0 || size > maxImageBytes {
		return Attachment{}, errors.New("图片数据无效或超过 10MB")
	}
	attachment.ID = randomID()
	attachment.Path = ""
	attachment.DataURL = ""
	attachment.URL = "/api/uploads/" + attachment.ID
	if attachment.Name == "" {
		attachment.Name = "image.png"
	}
	s.mu.Lock()
	s.uploads[attachment.ID] = storedUpload{UserID: userID, Attachment: attachment, DataURL: dataURL}
	s.persistLocked()
	s.mu.Unlock()
	return attachment, nil
}

func (s *relayStore) upload(userID, id string) (storedUpload, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	upload, ok := s.uploads[id]
	return upload, ok && upload.UserID == userID
}

func (s *relayStore) resolveAttachments(userID string, attachments []Attachment) ([]Attachment, error) {
	resolved := make([]Attachment, 0, len(attachments))
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
		resolved = append(resolved, item)
	}
	return resolved, nil
}

type sessionTokens struct {
	mu     sync.Mutex
	tokens map[string]tokenSession
}

type tokenSession struct {
	UserID    string
	ExpiresAt time.Time
}

func newSessionTokens() *sessionTokens {
	return &sessionTokens{tokens: map[string]tokenSession{}}
}

func (s *sessionTokens) create(userID string) string {
	token := randomToken(32)
	s.mu.Lock()
	s.tokens[token] = tokenSession{UserID: userID, ExpiresAt: time.Now().Add(30 * 24 * time.Hour)}
	s.mu.Unlock()
	return token
}

func (s *sessionTokens) userID(token string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.tokens[token]
	if !ok || time.Now().After(session.ExpiresAt) {
		delete(s.tokens, token)
		return ""
	}
	return session.UserID
}

func (s *sessionTokens) remove(token string) {
	s.mu.Lock()
	delete(s.tokens, token)
	s.mu.Unlock()
}

type relayServer struct {
	store    *mysqlRelayStore
	webDir   string
	mu       sync.Mutex
	agents   map[string]*agentPeer
	upgrader websocket.Upgrader
}

type agentPeer struct {
	server   *relayServer
	userID   string
	deviceID string
	tokenID  string
	conn     *websocket.Conn
	write    sync.Mutex
	pending  map[string]chan envelope
	mu       sync.Mutex
}

func newRelayServer(uploadDir, webDir string) (*relayServer, error) {
	store, err := newMySQLRelayStore(uploadDir)
	if err != nil {
		return nil, err
	}
	return &relayServer{
		store:  store,
		webDir: webDir,
		agents: map[string]*agentPeer{},
		upgrader: websocket.Upgrader{CheckOrigin: func(request *http.Request) bool {
			origin := request.Header.Get("Origin")
			return origin == "" || sameOrigin(request, origin)
		}},
	}, nil
}

func main() {
	dataDir := env("DATA_DIR", "/data")
	uploadDir := env("UPLOAD_DIR", filepath.Join(dataDir, "uploads"))
	webDir := env("WEB_DIR", "/app/web")
	host := env("HOST", "0.0.0.0")
	port := env("PORT", "8787")
	server, err := newRelayServer(uploadDir, webDir)
	if err != nil {
		log.Fatal(err)
	}
	defer server.store.close()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/status", server.authStatus)
	mux.HandleFunc("/api/auth/register", server.authRegister)
	mux.HandleFunc("/api/auth/login", server.authLogin)
	mux.HandleFunc("/api/auth/logout", server.authLogout)
	mux.HandleFunc("/api/auth/password", server.authPassword)
	mux.HandleFunc("/api/auth/token", server.authToken)
	mux.HandleFunc("/api/auth/tokens", server.authTokens)
	mux.HandleFunc("/api/auth/tokens/", server.authTokenItem)
	mux.HandleFunc("/api/agent/login", server.agentLogin)
	mux.HandleFunc("/api/agent/validate", server.agentValidate)
	mux.HandleFunc("/api/agent/ws", server.agentWebSocket)
	mux.HandleFunc("/api/openapi.json", server.openapi)
	mux.HandleFunc("/api/health", server.health)
	mux.HandleFunc("/api/devices", server.devices)
	mux.HandleFunc("/api/settings", server.settings)
	mux.HandleFunc("/api/uploads", server.uploads)
	mux.HandleFunc("/api/uploads/", server.uploadFile)
	mux.HandleFunc("/api/threads", server.threads)
	mux.HandleFunc("/api/threads/", server.threadAction)
	mux.HandleFunc("/api/sessions", server.sessions)
	mux.HandleFunc("/api/sessions/", server.sessionAction)
	mux.HandleFunc("/", server.static)
	address := host + ":" + port
	log.Printf("Codex Relay Server: http://%s", strings.Replace(address, "0.0.0.0", "127.0.0.1", 1))
	log.Fatal(http.ListenAndServe(address, withCORS(server.requireAuth(mux))))
}

func (s *relayServer) authStatus(w http.ResponseWriter, request *http.Request) {
	user, ok := s.userFromRequest(request)
	records := []accessTokenRecord{}
	if ok {
		records, _ = s.store.listTokens(user.ID)
	}
	writeJSON(w, map[string]interface{}{
		"authenticated":    ok,
		"username":         user.Username,
		"registrationOpen": strings.EqualFold(env("ALLOW_REGISTRATION", "true"), "true"),
		"tokens":           tokenListJSON(records),
		"apiToken":         map[string]interface{}{"enabled": len(records) > 0, "count": len(records)},
	})
}

func (s *relayServer) authRegister(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !strings.EqualFold(env("ALLOW_REGISTRATION", "true"), "true") {
		writeErrorStatus(w, http.StatusForbidden, "服务端已关闭注册")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeErrorStatus(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	user, err := s.store.register(body.Username, body.Password)
	if err != nil {
		writeErrorStatus(w, http.StatusConflict, err.Error())
		return
	}
	sessionToken, err := s.store.createWebSession(user.ID)
	if err != nil {
		writeErrorStatus(w, http.StatusInternalServerError, "创建登录会话失败")
		return
	}
	s.setCookie(w, sessionToken)
	writeJSONStatus(w, http.StatusCreated, map[string]interface{}{"ok": true, "authenticated": true, "username": user.Username})
}

func (s *relayServer) authLogin(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeErrorStatus(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	user, ok := s.store.authenticate(body.Username, body.Password)
	if !ok {
		writeErrorStatus(w, http.StatusUnauthorized, "用户名或密码不正确")
		return
	}
	sessionToken, err := s.store.createWebSession(user.ID)
	if err != nil {
		writeErrorStatus(w, http.StatusInternalServerError, "创建登录会话失败")
		return
	}
	s.setCookie(w, sessionToken)
	writeJSON(w, map[string]interface{}{"ok": true, "authenticated": true, "username": user.Username})
}

func (s *relayServer) authLogout(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if cookie, err := request.Cookie(authCookieName); err == nil {
		s.store.removeWebSession(cookie.Value)
	}
	s.clearCookie(w)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *relayServer) authPassword(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	user, ok := s.userFromRequest(request)
	if !ok {
		writeErrorStatus(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeErrorStatus(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	if err := s.store.changePassword(user.ID, body.CurrentPassword, body.NewPassword); err != nil {
		writeErrorStatus(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *relayServer) authToken(w http.ResponseWriter, request *http.Request) {
	user, ok := s.userFromRequest(request)
	if !ok {
		writeErrorStatus(w, http.StatusUnauthorized, "请先登录")
		return
	}
	switch request.Method {
	case http.MethodGet:
		records, err := s.store.listTokens(user.ID)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, map[string]interface{}{"tokens": tokenListJSON(records)})
	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
		}
		if request.Body != nil {
			_ = decodeJSON(request, &body)
		}
		record, err := s.store.createToken(user.ID, body.Name)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusCreated, map[string]interface{}{"token": tokenJSON(record), "note": "Token 将始终显示在当前账号的 Token 列表中，请妥善保管。"})
	case http.MethodDelete:
		tokenID := request.URL.Query().Get("tokenId")
		if tokenID == "" {
			writeErrorStatus(w, http.StatusBadRequest, "请通过 tokenId 指定要删除的 Token")
			return
		}
		if err := s.store.deleteToken(user.ID, tokenID); err != nil {
			writeErrorStatus(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func (s *relayServer) authTokens(w http.ResponseWriter, request *http.Request) {
	user, ok := s.userFromRequest(request)
	if !ok {
		writeErrorStatus(w, http.StatusUnauthorized, "请先登录")
		return
	}
	switch request.Method {
	case http.MethodGet:
		records, err := s.store.listTokens(user.ID)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, map[string]interface{}{"tokens": tokenListJSON(records)})
	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
		}
		if err := decodeJSON(request, &body); err != nil {
			writeErrorStatus(w, http.StatusBadRequest, "请求格式不正确")
			return
		}
		record, err := s.store.createToken(user.ID, body.Name)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusCreated, map[string]interface{}{"token": tokenJSON(record)})
	default:
		methodNotAllowed(w)
	}
}

func (s *relayServer) authTokenItem(w http.ResponseWriter, request *http.Request) {
	user, ok := s.userFromRequest(request)
	if !ok {
		writeErrorStatus(w, http.StatusUnauthorized, "请先登录")
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/auth/tokens/"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeErrorStatus(w, http.StatusNotFound, "Token 不存在")
		return
	}
	tokenID := parts[0]
	if len(parts) == 2 && parts[1] == "refresh" && request.Method == http.MethodPost {
		record, err := s.store.refreshToken(user.ID, tokenID)
		if err != nil {
			writeErrorStatus(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, map[string]interface{}{"token": tokenJSON(record)})
		return
	}
	if len(parts) == 1 && request.Method == http.MethodDelete {
		if err := s.store.deleteToken(user.ID, tokenID); err != nil {
			writeErrorStatus(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	writeErrorStatus(w, http.StatusNotFound, "接口不存在")
}

func (s *relayServer) agentLogin(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		Token      string `json:"token"`
		DeviceID   string `json:"deviceId"`
		DeviceName string `json:"deviceName"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeErrorStatus(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	tokenValue := strings.TrimSpace(body.Token)
	if tokenValue == "" {
		tokenValue = bearerToken(request)
	}
	user, tokenRecord, ok := s.store.userForAPIToken(tokenValue)
	if !ok {
		writeErrorStatus(w, http.StatusUnauthorized, "Token 无效或已删除")
		return
	}
	device, err := s.store.loginDevice(user.ID, tokenRecord.ID, tokenRecord.Prefix, body.DeviceID, body.DeviceName)
	if err != nil {
		writeErrorStatus(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"deviceId": device.ID, "deviceName": device.Name, "tokenId": tokenRecord.ID, "tokenName": tokenRecord.Name, "tokenPrefix": tokenRecord.Prefix})
}

func (s *relayServer) agentValidate(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	deviceID := request.URL.Query().Get("deviceId")
	device, token, ok := s.store.verifyAgent(deviceID, bearerToken(request))
	if !ok {
		writeErrorStatus(w, http.StatusUnauthorized, "Token 无效、已删除或未绑定此设备")
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "deviceId": device.ID, "deviceName": device.Name, "tokenId": token.ID, "tokenName": token.Name})
}

func (s *relayServer) agentWebSocket(w http.ResponseWriter, request *http.Request) {
	deviceID := request.URL.Query().Get("deviceId")
	token := bearerToken(request)
	device, tokenRecord, ok := s.store.verifyAgent(deviceID, token)
	if !ok {
		writeErrorStatus(w, http.StatusUnauthorized, "Token 无效、已删除或未绑定此设备")
		return
	}
	conn, err := s.upgrader.Upgrade(w, request, nil)
	if err != nil {
		return
	}
	peer := &agentPeer{server: s, userID: device.UserID, deviceID: device.ID, tokenID: tokenRecord.ID, conn: conn, pending: map[string]chan envelope{}}
	s.registerPeer(peer)
	defer func() {
		s.unregisterPeer(peer)
		_ = conn.Close()
	}()
	_ = peer.writeJSON(envelope{Type: "connected", Payload: mustJSON(map[string]interface{}{"deviceId": device.ID, "serverTime": time.Now().Format(time.RFC3339)})})
	for {
		var message envelope
		if err := conn.ReadJSON(&message); err != nil {
			return
		}
		s.handleAgentMessage(peer, message)
	}
}

func (s *relayServer) handleAgentMessage(peer *agentPeer, message envelope) {
	switch message.Type {
	case "event":
		var event Event
		if json.Unmarshal(message.Payload, &event) == nil && event.SessionID != "" {
			s.store.appendEvent(peer.userID, peer.deviceID, event)
		}
	case "session":
		var session Session
		if json.Unmarshal(message.Payload, &session) == nil && session.ID != "" {
			s.store.upsertSessions(peer.userID, peer.deviceID, []Session{session})
		}
	case "sync.sessions":
		var body struct {
			Sessions []Session `json:"sessions"`
		}
		if json.Unmarshal(message.Payload, &body) == nil {
			s.store.upsertSessions(peer.userID, peer.deviceID, body.Sessions)
		}
	case "response":
		peer.resolve(message)
	}
}

func (s *relayServer) health(w http.ResponseWriter, request *http.Request) {
	user, _ := s.userFromRequest(request)
	devices := s.store.devicesForUser(user.ID, s.deviceOnline)
	databaseStatus := "ok"
	if err := s.store.ping(); err != nil {
		databaseStatus = "error"
	}
	writeJSON(w, map[string]interface{}{"ok": databaseStatus == "ok", "mode": "relay-server", "database": databaseStatus, "devices": devices, "connectedDevices": s.agentCount(user.ID)})
}

func (s *relayServer) devices(w http.ResponseWriter, request *http.Request) {
	user, _ := s.userFromRequest(request)
	writeJSON(w, map[string]interface{}{"devices": s.store.devicesForUser(user.ID, s.deviceOnline)})
}

func (s *relayServer) settings(w http.ResponseWriter, request *http.Request) {
	user, _ := s.userFromRequest(request)
	deviceID, err := s.resolveDevice(user.ID, request, "")
	if err != nil {
		writeErrorStatus(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	switch request.Method {
	case http.MethodGet:
		result, err := s.command(user.ID, deviceID, "settings.get", nil)
		if err != nil {
			writeErrorStatus(w, http.StatusBadGateway, err.Error())
			return
		}
		writeRawJSON(w, result)
	case http.MethodPost:
		var settings AppSettings
		if err := decodeJSON(request, &settings); err != nil {
			writeErrorStatus(w, http.StatusBadRequest, "请求格式不正确")
			return
		}
		result, err := s.command(user.ID, deviceID, "settings.update", settings)
		if err != nil {
			writeErrorStatus(w, http.StatusBadGateway, err.Error())
			return
		}
		writeRawJSON(w, result)
	default:
		methodNotAllowed(w)
	}
}

func (s *relayServer) uploads(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	user, _ := s.userFromRequest(request)
	var body struct {
		Name     string `json:"name"`
		MimeType string `json:"mimeType"`
		DataURL  string `json:"dataUrl"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeErrorStatus(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	attachment, err := s.store.saveUpload(user.ID, Attachment{Name: body.Name, MimeType: body.MimeType}, body.DataURL)
	if err != nil {
		writeErrorStatus(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]interface{}{"attachment": attachment})
}

func (s *relayServer) uploadFile(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	user, _ := s.userFromRequest(request)
	id := filepath.Base(strings.TrimPrefix(request.URL.Path, "/api/uploads/"))
	upload, ok := s.store.upload(user.ID, id)
	if !ok {
		writeErrorStatus(w, http.StatusNotFound, "图片不存在")
		return
	}
	comma := strings.Index(upload.DataURL, ",")
	if comma < 0 {
		writeErrorStatus(w, http.StatusInternalServerError, "图片数据损坏")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(upload.DataURL[comma+1:])
	if err != nil {
		writeErrorStatus(w, http.StatusInternalServerError, "图片数据损坏")
		return
	}
	w.Header().Set("content-type", upload.Attachment.MimeType)
	_, _ = w.Write(raw)
}

func (s *relayServer) threads(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	user, _ := s.userFromRequest(request)
	deviceID, err := s.resolveDevice(user.ID, request, "")
	if err != nil {
		writeErrorStatus(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	result, err := s.command(user.ID, deviceID, "threads.list", nil)
	if err != nil {
		writeErrorStatus(w, http.StatusBadGateway, err.Error())
		return
	}
	var response struct {
		Sessions []Session `json:"sessions"`
	}
	if json.Unmarshal(result, &response) == nil {
		s.store.upsertSessions(user.ID, deviceID, response.Sessions)
	}
	writeRawJSON(w, result)
}

func (s *relayServer) threadAction(w http.ResponseWriter, request *http.Request) {
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/api/threads/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeErrorStatus(w, http.StatusNotFound, "对话不存在")
		return
	}
	user, _ := s.userFromRequest(request)
	threadID := parts[0]
	deviceID, err := s.resolveDevice(user.ID, request, threadID)
	if err != nil {
		writeErrorStatus(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if len(parts) == 2 && parts[1] == "resume" && request.Method == http.MethodPost {
		s.store.clearEvents(user.ID, threadID)
		result, err := s.command(user.ID, deviceID, "threads.resume", map[string]string{"id": threadID})
		if err != nil {
			writeErrorStatus(w, http.StatusBadGateway, err.Error())
			return
		}
		var response struct {
			Session Session `json:"session"`
		}
		if json.Unmarshal(result, &response) == nil {
			s.store.upsertSessions(user.ID, deviceID, []Session{response.Session})
		}
		writeRawJSON(w, result)
		return
	}
	if len(parts) == 1 && request.Method == http.MethodDelete {
		if _, err := s.command(user.ID, deviceID, "threads.archive", map[string]string{"id": threadID}); err != nil {
			writeErrorStatus(w, http.StatusBadGateway, err.Error())
			return
		}
		s.store.removeSession(user.ID, threadID)
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	writeErrorStatus(w, http.StatusNotFound, "接口不存在")
}

func (s *relayServer) sessions(w http.ResponseWriter, request *http.Request) {
	user, _ := s.userFromRequest(request)
	switch request.Method {
	case http.MethodGet:
		deviceID, err := s.resolveDevice(user.ID, request, "")
		if err != nil {
			writeErrorStatus(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		writeJSON(w, map[string]interface{}{"sessions": s.store.sessionsForUser(user.ID, deviceID)})
	case http.MethodPost:
		deviceID, err := s.resolveDevice(user.ID, request, "")
		if err != nil {
			writeErrorStatus(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		var body struct {
			Prompt string `json:"prompt"`
		}
		if err := decodeJSON(request, &body); err != nil {
			writeErrorStatus(w, http.StatusBadRequest, "请求格式不正确")
			return
		}
		result, err := s.command(user.ID, deviceID, "sessions.create", map[string]string{"prompt": body.Prompt})
		if err != nil {
			writeErrorStatus(w, http.StatusBadGateway, err.Error())
			return
		}
		var response struct {
			Session Session `json:"session"`
		}
		if json.Unmarshal(result, &response) == nil {
			s.store.upsertSessions(user.ID, deviceID, []Session{response.Session})
		}
		writeRawJSONStatus(w, http.StatusCreated, result)
	default:
		methodNotAllowed(w)
	}
}

func (s *relayServer) sessionAction(w http.ResponseWriter, request *http.Request) {
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/api/sessions/"), "/")
	if len(parts) < 2 || parts[0] == "" {
		writeErrorStatus(w, http.StatusNotFound, "会话不存在")
		return
	}
	user, _ := s.userFromRequest(request)
	sessionID, action := parts[0], parts[1]
	if action == "events" {
		s.sse(w, request, user.ID, sessionID)
		return
	}
	if request.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	deviceID, err := s.resolveDevice(user.ID, request, sessionID)
	if err != nil {
		writeErrorStatus(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	switch action {
	case "messages":
		var body struct {
			Text        string       `json:"text"`
			Attachments []Attachment `json:"attachments"`
		}
		if err := decodeJSON(request, &body); err != nil {
			writeErrorStatus(w, http.StatusBadRequest, "请求格式不正确")
			return
		}
		if strings.TrimSpace(body.Text) == "" && len(body.Attachments) == 0 {
			writeErrorStatus(w, http.StatusBadRequest, "消息不能为空")
			return
		}
		attachments, err := s.store.resolveAttachments(user.ID, body.Attachments)
		if err != nil {
			writeErrorStatus(w, http.StatusBadRequest, err.Error())
			return
		}
		_, err = s.command(user.ID, deviceID, "sessions.message", map[string]interface{}{"id": sessionID, "text": body.Text, "attachments": attachments})
		if err != nil {
			writeErrorStatus(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSONStatus(w, http.StatusAccepted, map[string]bool{"ok": true})
	case "approvals":
		var body struct {
			ApprovalID string `json:"approvalId"`
			Decision   string `json:"decision"`
		}
		if err := decodeJSON(request, &body); err != nil {
			writeErrorStatus(w, http.StatusBadRequest, "请求格式不正确")
			return
		}
		if body.Decision != "approved" {
			body.Decision = "rejected"
		}
		_, err := s.command(user.ID, deviceID, "sessions.approval", map[string]string{"id": sessionID, "approvalId": body.ApprovalID, "decision": body.Decision})
		if err != nil {
			writeErrorStatus(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	case "cancel":
		_, err := s.command(user.ID, deviceID, "sessions.cancel", map[string]string{"id": sessionID})
		if err != nil {
			writeErrorStatus(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		writeErrorStatus(w, http.StatusNotFound, "接口不存在")
	}
}

func (s *relayServer) sse(w http.ResponseWriter, request *http.Request, userID, sessionID string) {
	after, _ := strconv.ParseInt(firstNonEmpty(request.URL.Query().Get("after"), request.Header.Get("last-event-id"), "0"), 10, 64)
	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-cache, no-transform")
	w.Header().Set("connection", "keep-alive")
	w.Header().Set("x-accel-buffering", "no")
	for _, event := range s.store.eventsForUser(userID, sessionID, after, defaultEventBacklog) {
		writeSSE(w, event)
	}
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	channel := s.store.subscribe(userID)
	defer s.store.unsubscribe(userID, channel)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case <-ticker.C:
			_, _ = w.Write([]byte(": ping\n\n"))
		case event := <-channel:
			if event.SessionID != sessionID {
				continue
			}
			writeSSE(w, event)
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func (s *relayServer) resolveDevice(userID string, request *http.Request, sessionID string) (string, error) {
	if requested := request.URL.Query().Get("deviceId"); requested != "" && s.deviceOwnedBy(userID, requested) {
		return requested, nil
	}
	if sessionID != "" {
		if deviceID := s.store.sessionDevice(userID, sessionID); deviceID != "" {
			return deviceID, nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for deviceID, peer := range s.agents {
		if peer.userID == userID {
			return deviceID, nil
		}
	}
	return "", errors.New("没有在线的 Codex 客户端。请先在运行 Codex 的电脑上登录并启动客户端 agent")
}

func (s *relayServer) deviceOwnedBy(userID, deviceID string) bool {
	return s.store.deviceOwnedBy(userID, deviceID)
}

func (s *relayServer) command(userID, deviceID, action string, payload interface{}) (json.RawMessage, error) {
	s.mu.Lock()
	peer := s.agents[deviceID]
	s.mu.Unlock()
	if peer == nil || peer.userID != userID {
		return nil, errors.New("选中的 Codex 客户端不在线")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return peer.request(action, raw)
}

func (s *relayServer) registerPeer(peer *agentPeer) {
	s.mu.Lock()
	old := s.agents[peer.deviceID]
	s.agents[peer.deviceID] = peer
	s.mu.Unlock()
	if old != nil && old != peer {
		_ = old.conn.Close()
	}
}

func (s *relayServer) unregisterPeer(peer *agentPeer) {
	s.mu.Lock()
	if s.agents[peer.deviceID] == peer {
		delete(s.agents, peer.deviceID)
	}
	s.mu.Unlock()
	peer.failPending(errors.New("客户端连接已断开"))
}

func (s *relayServer) deviceOnline(deviceID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agents[deviceID] != nil
}

func (s *relayServer) agentCount(userID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, peer := range s.agents {
		if peer.userID == userID {
			count++
		}
	}
	return count
}

func (p *agentPeer) request(action string, payload json.RawMessage) (json.RawMessage, error) {
	requestID := randomID()
	channel := make(chan envelope, 1)
	p.mu.Lock()
	p.pending[requestID] = channel
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.pending, requestID)
		p.mu.Unlock()
	}()
	if err := p.writeJSON(envelope{Type: "command", RequestID: requestID, Action: action, Payload: payload}); err != nil {
		return nil, err
	}
	select {
	case response := <-channel:
		if response.Error != "" {
			return nil, errors.New(response.Error)
		}
		return response.Payload, nil
	case <-time.After(defaultRequestTimeoutSecs * time.Second):
		return nil, errors.New("等待本机 Codex 客户端响应超时")
	}
}

func (p *agentPeer) resolve(response envelope) {
	p.mu.Lock()
	channel := p.pending[response.RequestID]
	p.mu.Unlock()
	if channel == nil {
		return
	}
	select {
	case channel <- response:
	default:
	}
}

func (p *agentPeer) failPending(err error) {
	p.mu.Lock()
	pending := p.pending
	p.pending = map[string]chan envelope{}
	p.mu.Unlock()
	for _, channel := range pending {
		select {
		case channel <- envelope{Type: "response", Error: err.Error()}:
		default:
		}
	}
}

func (p *agentPeer) writeJSON(value interface{}) error {
	p.write.Lock()
	defer p.write.Unlock()
	return p.conn.WriteJSON(value)
}

func (s *relayServer) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodOptions || !strings.HasPrefix(request.URL.Path, "/api/") || s.publicPath(request.URL.Path) {
			next.ServeHTTP(w, request)
			return
		}
		if _, ok := s.userFromRequest(request); ok {
			next.ServeHTTP(w, request)
			return
		}
		writeErrorStatus(w, http.StatusUnauthorized, "请先登录")
	})
}

func (s *relayServer) publicPath(path string) bool {
	switch path {
	case "/api/auth/status", "/api/auth/register", "/api/auth/login", "/api/agent/login", "/api/agent/ws", "/api/openapi.json":
		return true
	default:
		return false
	}
}

func (s *relayServer) userFromRequest(request *http.Request) (User, bool) {
	if token := bearerToken(request); token != "" {
		if user, _, ok := s.store.userForAPIToken(token); ok {
			return user, true
		}
	}
	if cookie, err := request.Cookie(authCookieName); err == nil {
		if user, ok := s.store.userForWebSession(cookie.Value); ok {
			return user, true
		}
	}
	return User{}, false
}

func (s *relayServer) setCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: authCookieName, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 60 * 60})
}

func (s *relayServer) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: authCookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func (s *relayServer) openapi(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, map[string]interface{}{
		"openapi": "3.0.3",
		"info":    map[string]string{"title": "Codex Link API", "version": "1.1.0", "description": "中心服务端 API。运行 Codex 的本机客户端使用 Token 和 WebSocket 反向连接。"},
		"servers": []map[string]string{{"url": "/"}},
		"components": map[string]interface{}{
			"securitySchemes": map[string]interface{}{
				"bearerAuth": map[string]string{"type": "http", "scheme": "bearer", "bearerFormat": "Codex Link Token"},
				"webSession": map[string]string{"type": "apiKey", "in": "cookie", "name": authCookieName},
			},
		},
		"paths": map[string]interface{}{
			"/api/health":                   map[string]interface{}{"get": map[string]string{"summary": "服务端和 MySQL 健康状态"}},
			"/api/auth/status":              map[string]interface{}{"get": map[string]string{"summary": "查询网页登录状态"}},
			"/api/auth/register":            map[string]interface{}{"post": map[string]string{"summary": "注册服务端账号"}},
			"/api/auth/login":               map[string]interface{}{"post": map[string]string{"summary": "网页登录"}},
			"/api/auth/logout":              map[string]interface{}{"post": map[string]string{"summary": "退出网页登录"}},
			"/api/auth/password":            map[string]interface{}{"post": map[string]string{"summary": "修改账号密码"}},
			"/api/auth/token":               map[string]interface{}{"get": map[string]string{"summary": "兼容接口：查询 Token 列表"}, "post": map[string]string{"summary": "兼容接口：创建 Token"}, "delete": map[string]string{"summary": "兼容接口：删除指定 Token"}},
			"/api/auth/tokens":              map[string]interface{}{"get": map[string]string{"summary": "查询当前账号的全部 Token"}, "post": map[string]string{"summary": "创建 Token"}},
			"/api/auth/tokens/{id}/refresh": map[string]interface{}{"post": map[string]string{"summary": "刷新指定 Token"}},
			"/api/auth/tokens/{id}":         map[string]interface{}{"delete": map[string]string{"summary": "删除指定 Token"}},
			"/api/agent/login":              map[string]interface{}{"post": map[string]string{"summary": "本机客户端使用 Token 登录设备"}},
			"/api/agent/validate":           map[string]interface{}{"get": map[string]string{"summary": "校验客户端 Token 和设备绑定"}},
			"/api/agent/ws":                 map[string]interface{}{"get": map[string]string{"summary": "客户端 WebSocket 反向连接"}},
			"/api/devices":                  map[string]interface{}{"get": map[string]string{"summary": "设备列表及在线状态"}},
			"/api/threads":                  map[string]interface{}{"get": map[string]string{"summary": "从在线客户端读取历史对话"}},
			"/api/threads/{id}":             map[string]interface{}{"delete": map[string]string{"summary": "归档 Codex 对话"}},
			"/api/threads/{id}/resume":      map[string]interface{}{"post": map[string]string{"summary": "恢复 Codex 对话"}},
			"/api/sessions":                 map[string]interface{}{"get": map[string]string{"summary": "读取同步会话缓存"}, "post": map[string]string{"summary": "创建新会话"}},
			"/api/sessions/{id}/events":     map[string]interface{}{"get": map[string]string{"summary": "SSE 流式事件"}},
			"/api/sessions/{id}/messages":   map[string]interface{}{"post": map[string]string{"summary": "发送消息并经 WebSocket 转发"}},
			"/api/sessions/{id}/approvals":  map[string]interface{}{"post": map[string]string{"summary": "提交审批"}},
			"/api/sessions/{id}/cancel":     map[string]interface{}{"post": map[string]string{"summary": "取消当前 turn"}},
			"/api/uploads":                  map[string]interface{}{"post": map[string]string{"summary": "上传图片附件"}},
			"/api/uploads/{id}":             map[string]interface{}{"get": map[string]string{"summary": "读取当前用户的图片附件"}},
		},
	})
}

func (s *relayServer) static(w http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.URL.Path, "/api/") {
		writeErrorStatus(w, http.StatusNotFound, "接口不存在")
		return
	}
	requestPath := filepath.Clean(strings.TrimPrefix(request.URL.Path, "/"))
	if requestPath == "." || requestPath == "" {
		requestPath = "index.html"
	}
	fullPath := filepath.Join(s.webDir, requestPath)
	if info, err := os.Stat(fullPath); err == nil && !info.IsDir() {
		http.ServeFile(w, request, fullPath)
		return
	}
	http.ServeFile(w, request, filepath.Join(s.webDir, "index.html"))
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("access-control-allow-origin", "*")
		w.Header().Set("access-control-allow-methods", "GET,POST,DELETE,OPTIONS")
		w.Header().Set("access-control-allow-headers", "authorization,content-type,last-event-id")
		if request.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, request)
	})
}

func writeJSON(w http.ResponseWriter, value interface{}) { writeJSONStatus(w, http.StatusOK, value) }

func writeJSONStatus(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeRawJSON(w http.ResponseWriter, raw json.RawMessage) {
	writeRawJSONStatus(w, http.StatusOK, raw)
}

func writeRawJSONStatus(w http.ResponseWriter, status int, raw json.RawMessage) {
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func writeError(w http.ResponseWriter, err error) {
	writeErrorStatus(w, http.StatusInternalServerError, err.Error())
}

func writeErrorStatus(w http.ResponseWriter, status int, message string) {
	writeJSONStatus(w, status, map[string]string{"error": message})
}

func methodNotAllowed(w http.ResponseWriter) {
	writeErrorStatus(w, http.StatusMethodNotAllowed, "请求方法不支持")
}

func writeSSE(w http.ResponseWriter, event Event) {
	raw, _ := json.Marshal(event)
	_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.ID, event.Type, raw)
}

func decodeJSON(request *http.Request, target interface{}) error {
	request.Body = http.MaxBytesReader(nil, request.Body, 16*1024*1024)
	return json.NewDecoder(request.Body).Decode(target)
}

func bearerToken(request *http.Request) string {
	header := strings.TrimSpace(request.Header.Get("authorization"))
	if len(header) < 8 || !strings.EqualFold(header[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(header[7:])
}

func sameOrigin(request *http.Request, origin string) bool {
	parsed, err := http.NewRequest(http.MethodGet, origin, nil)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Host, request.Host)
}

func hashPassword(password, salt string, iterations int) string {
	sum := sha256.Sum256([]byte(salt + "\x00" + password))
	for index := 1; index < iterations; index++ {
		next := sha256.Sum256(append(sum[:], []byte(salt)...))
		sum = next
	}
	return hex.EncodeToString(sum[:])
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buffer)
}

func randomToken(size int) string {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return randomID() + randomID()
	}
	return base64.RawURLEncoding.EncodeToString(buffer)
}

func decodedDataURLSize(value string) int {
	comma := strings.Index(value, ",")
	if comma < 0 || !strings.Contains(value[:comma], ";base64") {
		return 0
	}
	raw, err := base64.StdEncoding.DecodeString(value[comma+1:])
	if err != nil {
		return 0
	}
	return len(raw)
}

func filterOwnedEvents(events []ownedEvent, keep func(ownedEvent) bool) []ownedEvent {
	result := events[:0]
	for _, event := range events {
		if keep(event) {
			result = append(result, event)
		}
	}
	return result
}

func sessionKey(userID, sessionID string) string { return userID + ":" + sessionID }

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func min(first, second int) int {
	if first < second {
		return first
	}
	return second
}

func mustJSON(value interface{}) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

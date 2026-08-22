package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const baseDeveloperInstructions = "请全程使用中文与用户交流。除非用户明确要求其他语言，否则回复、解释、状态说明和审批说明均使用简体中文。"
const authCookieName = "codex_remote_session"
const defaultPasswordIterations = 120000

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

type Store struct {
	mu          sync.Mutex
	path        string
	dataDir     string
	sessions    map[string]Session
	events      []Event
	nextID      int64
	clients     map[string]map[chan Event]struct{}
	auth        AuthConfig
	settings    AppSettings
	eventHook   func(Event)
	sessionHook func(Session)
}

type storeFile struct {
	Sessions []Session   `json:"sessions"`
	Events   []Event     `json:"events"`
	Auth     AuthConfig  `json:"auth,omitempty"`
	Settings AppSettings `json:"settings,omitempty"`
}

type AuthConfig struct {
	PasswordHash      string `json:"passwordHash,omitempty"`
	Salt              string `json:"salt,omitempty"`
	Iterations        int    `json:"iterations,omitempty"`
	UpdatedAt         string `json:"updatedAt,omitempty"`
	APITokenHash      string `json:"apiTokenHash,omitempty"`
	APITokenPrefix    string `json:"apiTokenPrefix,omitempty"`
	APITokenCreatedAt string `json:"apiTokenCreatedAt,omitempty"`
}

type AppSettings struct {
	ApprovalMode string `json:"approvalMode"`
	WorkMode     string `json:"workMode"`
}

type Attachment struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Path     string `json:"path,omitempty"`
	URL      string `json:"url,omitempty"`
	DataURL  string `json:"dataUrl,omitempty"`
}

func NewStore(dataDir string) *Store {
	_ = os.MkdirAll(dataDir, 0o755)
	store := &Store{
		dataDir:  dataDir,
		path:     filepath.Join(dataDir, "store-go.json"),
		sessions: map[string]Session{},
		clients:  map[string]map[chan Event]struct{}{},
		settings: defaultSettings(),
	}
	store.load()
	return store
}

func (s *Store) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var file storeFile
	if json.Unmarshal(raw, &file) != nil {
		return
	}
	for _, session := range file.Sessions {
		s.sessions[session.ID] = session
	}
	s.events = file.Events
	s.auth = file.Auth
	if file.Settings.ApprovalMode != "" || file.Settings.WorkMode != "" {
		s.settings = normalizeSettings(file.Settings)
	}
	for _, event := range s.events {
		if event.ID > s.nextID {
			s.nextID = event.ID
		}
	}
}

func (s *Store) persistLocked() {
	file := storeFile{Events: s.events, Auth: s.auth, Settings: s.settings}
	for _, session := range s.sessions {
		file.Sessions = append(file.Sessions, session)
	}
	sort.Slice(file.Sessions, func(i, j int) bool { return file.Sessions[i].UpdatedAt > file.Sessions[j].UpdatedAt })
	raw, _ := json.MarshalIndent(file, "", "  ")
	_ = os.WriteFile(s.path, raw, 0o644)
}

func (s *Store) UpsertSession(session Session) {
	s.mu.Lock()
	s.sessions[session.ID] = session
	s.persistLocked()
	hook := s.sessionHook
	s.mu.Unlock()
	if hook != nil {
		hook(session)
	}
}

func (s *Store) SetEventHook(hook func(Event)) {
	s.mu.Lock()
	s.eventHook = hook
	s.mu.Unlock()
}

func (s *Store) SetSessionHook(hook func(Session)) {
	s.mu.Lock()
	s.sessionHook = hook
	s.mu.Unlock()
}

func (s *Store) RemoveSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
	s.events = filterEvents(s.events, func(event Event) bool { return event.SessionID != id })
	s.persistLocked()
}

func (s *Store) ClearEvents(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = filterEvents(s.events, func(event Event) bool { return event.SessionID != id })
	s.persistLocked()
}

func (s *Store) Sessions() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessions := make([]Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].UpdatedAt > sessions[j].UpdatedAt })
	return sessions
}

func (s *Store) PasswordSet() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auth.PasswordHash != "" && s.auth.Salt != ""
}

func (s *Store) AuthConfig() AuthConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auth
}

func (s *Store) SetPassword(password string) error {
	salt := randomID()
	iterations := defaultPasswordIterations
	hash := hashPassword(password, salt, iterations)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auth = AuthConfig{
		PasswordHash: hash,
		Salt:         salt,
		Iterations:   iterations,
		UpdatedAt:    time.Now().Format(time.RFC3339),
	}
	s.persistLocked()
	return nil
}

func (s *Store) VerifyPassword(password string) bool {
	config := s.AuthConfig()
	if config.PasswordHash == "" || config.Salt == "" {
		return true
	}
	iterations := config.Iterations
	if iterations <= 0 {
		iterations = defaultPasswordIterations
	}
	candidate := hashPassword(password, config.Salt, iterations)
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(config.PasswordHash)) == 1
}

func (s *Store) APITokenStatus() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]interface{}{
		"enabled":   s.auth.APITokenHash != "",
		"prefix":    s.auth.APITokenPrefix,
		"createdAt": s.auth.APITokenCreatedAt,
	}
}

func (s *Store) RotateAPIToken() (string, map[string]interface{}) {
	token := "crm_" + randomToken(32)
	hash := hashAPIToken(token)
	prefix := token
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auth.APITokenHash = hash
	s.auth.APITokenPrefix = prefix
	s.auth.APITokenCreatedAt = time.Now().Format(time.RFC3339)
	s.persistLocked()
	return token, map[string]interface{}{
		"enabled":   true,
		"prefix":    s.auth.APITokenPrefix,
		"createdAt": s.auth.APITokenCreatedAt,
	}
}

func (s *Store) ClearAPIToken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auth.APITokenHash = ""
	s.auth.APITokenPrefix = ""
	s.auth.APITokenCreatedAt = ""
	s.persistLocked()
}

func (s *Store) VerifyAPIToken(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	s.mu.Lock()
	hash := s.auth.APITokenHash
	s.mu.Unlock()
	if hash == "" {
		return false
	}
	candidate := hashAPIToken(token)
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(hash)) == 1
}

func (s *Store) Settings() AppSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return normalizeSettings(s.settings)
}

func (s *Store) UpdateSettings(settings AppSettings) AppSettings {
	normalized := normalizeSettings(settings)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = normalized
	s.persistLocked()
	return normalized
}

func (s *Store) Events(sessionID string, after int64, limit int) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := []Event{}
	for _, event := range s.events {
		if event.SessionID == sessionID && event.ID > after {
			events = append(events, event)
		}
	}
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events
}

func (s *Store) Append(event Event) Event {
	s.mu.Lock()
	s.nextID++
	event.ID = s.nextID
	s.events = append(s.events, event)
	if len(s.events) > 3000 {
		s.events = s.events[len(s.events)-3000:]
	}
	s.persistLocked()
	clients := make([]chan Event, 0, len(s.clients[event.SessionID]))
	for client := range s.clients[event.SessionID] {
		clients = append(clients, client)
	}
	hook := s.eventHook
	s.mu.Unlock()

	for _, client := range clients {
		select {
		case client <- event:
		default:
		}
	}
	if hook != nil {
		hook(event)
	}
	return event
}

func (s *Store) Subscribe(sessionID string) chan Event {
	ch := make(chan Event, 64)
	s.mu.Lock()
	if s.clients[sessionID] == nil {
		s.clients[sessionID] = map[chan Event]struct{}{}
	}
	s.clients[sessionID][ch] = struct{}{}
	s.mu.Unlock()
	return ch
}

func (s *Store) Unsubscribe(sessionID string, ch chan Event) {
	s.mu.Lock()
	delete(s.clients[sessionID], ch)
	close(ch)
	s.mu.Unlock()
}

func filterEvents(events []Event, keep func(Event) bool) []Event {
	next := events[:0]
	for _, event := range events {
		if keep(event) {
			next = append(next, event)
		}
	}
	return next
}

type rpcMessage struct {
	JSONRPC string      `json:"jsonrpc,omitempty"`
	ID      interface{} `json:"id,omitempty"`
	Method  string      `json:"method,omitempty"`
	Params  interface{} `json:"params,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   interface{} `json:"error,omitempty"`
}

type pendingCall struct {
	result chan rpcMessage
}

type Bridge struct {
	mu              sync.Mutex
	cmd             *exec.Cmd
	stdin           io.WriteCloser
	nextRPCID       int64
	pending         map[int64]pendingCall
	session         *Session
	codexThreadID   string
	activeTurnID    string
	pendingApproval map[string]int64
	initialized     bool
	codexBin        string
	cwd             string
	store           *Store
}

func NewBridge(store *Store, cwd string) *Bridge {
	codexBin := os.Getenv("CODEX_BIN")
	if codexBin == "" {
		codexBin = discoverCodexBin()
	}
	if codexBin == "" {
		codexBin = "codex"
	}
	return &Bridge{
		pending:         map[int64]pendingCall{},
		pendingApproval: map[string]int64{},
		codexBin:        codexBin,
		cwd:             cwd,
		store:           store,
	}
}

func (b *Bridge) Health() map[string]interface{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	mode := "disconnected"
	if b.session != nil {
		mode = b.session.Mode
	}
	return map[string]interface{}{
		"available": b.cmd != nil,
		"mode":      mode,
		"codexBin":  b.codexBin,
		"cwd":       b.cwd,
		"mock":      false,
	}
}

func (b *Bridge) CurrentSession() *Session {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.session == nil {
		return nil
	}
	copy := *b.session
	return &copy
}

func (b *Bridge) ensureReady() error {
	if err := b.startProcess(); err != nil {
		return err
	}
	b.mu.Lock()
	initialized := b.initialized
	b.mu.Unlock()
	if initialized {
		return nil
	}
	_, err := b.request("initialize", map[string]interface{}{
		"clientInfo": map[string]interface{}{
			"name":    "codex-mobile-remote-go",
			"title":   "Codex Mobile Remote Go",
			"version": "0.1.0",
		},
		"capabilities": map[string]interface{}{
			"experimentalApi":    true,
			"requestAttestation": false,
		},
	})
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.initialized = true
	b.mu.Unlock()
	return nil
}

func (b *Bridge) startProcess() error {
	b.mu.Lock()
	if b.cmd != nil {
		b.mu.Unlock()
		return nil
	}
	cmd := exec.Command(b.codexBin, "app-server")
	cmd.Dir = b.cwd
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		b.mu.Unlock()
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		b.mu.Unlock()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		b.mu.Unlock()
		return err
	}
	if err := cmd.Start(); err != nil {
		b.mu.Unlock()
		return err
	}
	b.cmd = cmd
	b.stdin = stdin
	b.mu.Unlock()

	go b.readStdout(stdout)
	go b.readStderr(stderr)
	go func() {
		_ = cmd.Wait()
		b.mu.Lock()
		b.cmd = nil
		b.stdin = nil
		b.initialized = false
		b.mu.Unlock()
		b.emit("error", map[string]interface{}{"message": "Codex app-server exited"})
	}()
	return nil
}

func (b *Bridge) readStdout(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		b.handleLine(scanner.Text())
	}
}

func (b *Bridge) readStderr(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, `"level":"ERROR"`) {
			b.emit("error", map[string]interface{}{"message": line})
		}
	}
}

func (b *Bridge) request(method string, params interface{}) (interface{}, error) {
	b.mu.Lock()
	b.nextRPCID++
	id := b.nextRPCID
	call := pendingCall{result: make(chan rpcMessage, 1)}
	b.pending[id] = call
	stdin := b.stdin
	b.mu.Unlock()

	if stdin == nil {
		return nil, errors.New("codex app-server process is not running")
	}
	msg := rpcMessage{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	raw, _ := json.Marshal(msg)
	if _, err := stdin.Write(append(raw, '\n')); err != nil {
		return nil, err
	}

	select {
	case response := <-call.result:
		if response.Error != nil {
			return nil, fmt.Errorf("%s error: %v", method, response.Error)
		}
		return response.Result, nil
	case <-time.After(30 * time.Second):
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return nil, fmt.Errorf("timed out waiting for %s", method)
	}
}

func (b *Bridge) writeResult(id int64, result interface{}) error {
	b.mu.Lock()
	stdin := b.stdin
	b.mu.Unlock()
	if stdin == nil {
		return errors.New("codex app-server process is not running")
	}
	raw, _ := json.Marshal(rpcMessage{JSONRPC: "2.0", ID: id, Result: result})
	_, err := stdin.Write(append(raw, '\n'))
	return err
}

func (b *Bridge) handleLine(line string) {
	var msg rpcMessage
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		b.emit("tool.output", map[string]interface{}{"text": line + "\n"})
		return
	}
	id, hasNumericID := numericID(msg.ID)
	if hasNumericID && msg.Method == "" {
		b.mu.Lock()
		call, ok := b.pending[id]
		if ok {
			delete(b.pending, id)
		}
		b.mu.Unlock()
		if ok {
			call.result <- msg
		}
		return
	}
	if msg.Method != "" {
		b.mapCodexEvent(msg.Method, msg.Params, id, hasNumericID)
	}
}

func (b *Bridge) mapCodexEvent(method string, params interface{}, requestID int64, hasRequestID bool) {
	payload := asMap(params)
	if hasRequestID && strings.Contains(method, "requestApproval") {
		approvalID := stringValue(firstNonEmpty(payload["approvalId"], payload["itemId"], requestID))
		b.mu.Lock()
		b.pendingApproval[approvalID] = requestID
		b.mu.Unlock()
		b.updateStatus("waiting-approval")
		payload["approvalId"] = approvalID
		b.emit("approval.requested", payload)
		return
	}
	switch method {
	case "turn/completed":
		b.updateStatus("done")
		b.emit("turn.done", payload)
	case "item/agentMessage/delta":
		b.emit("assistant.delta", map[string]interface{}{"text": stringValue(payload["delta"])})
	case "command/exec/outputDelta", "item/commandExecution/outputDelta", "process/outputDelta":
		b.emit("tool.output", map[string]interface{}{"text": stringValue(firstNonEmpty(payload["delta"], payload["text"]))})
	case "item/started":
		item := asMap(payload["item"])
		itemType := strings.ToLower(stringValue(item["type"]))
		if strings.Contains(itemType, "command") {
			payload["command"] = firstNonEmpty(item["command"], item["parsedCommand"], "命令开始执行")
			b.emit("tool.started", payload)
		}
	case "error":
		b.updateStatus("error")
		b.emit("error", payload)
	}
}

func (b *Bridge) ListThreads() ([]Session, error) {
	if err := b.ensureReady(); err != nil {
		return nil, err
	}
	result, err := b.request("thread/list", map[string]interface{}{
		"limit":         100,
		"sortKey":       "updated_at",
		"sortDirection": "desc",
		"archived":      false,
	})
	if err != nil {
		return nil, err
	}
	data, _ := asMap(result)["data"].([]interface{})
	sessions := []Session{}
	for _, raw := range data {
		sessions = append(sessions, threadToSession(asMap(raw)))
	}
	return sessions, nil
}

func (b *Bridge) ResumeThread(threadID string) (Session, error) {
	if err := b.ensureReady(); err != nil {
		return Session{}, err
	}
	settings := b.store.Settings()
	result, err := b.request("thread/resume", withRuntimeOptions(map[string]interface{}{
		"threadId":              threadID,
		"developerInstructions": developerInstructions(settings),
	}, settings))
	if err != nil {
		return Session{}, err
	}
	thread := asMap(asMap(result)["thread"])
	session := threadToSession(thread)
	b.mu.Lock()
	b.codexThreadID = session.ID
	b.session = &session
	b.mu.Unlock()
	b.emit("session.status", map[string]interface{}{"status": session.Status, "mode": session.Mode})
	b.hydrateThread(thread)
	return session, nil
}

func (b *Bridge) ArchiveThread(threadID string) error {
	if err := b.ensureReady(); err != nil {
		return err
	}
	_, err := b.request("thread/archive", map[string]interface{}{"threadId": threadID})
	if err != nil {
		return err
	}
	b.mu.Lock()
	if b.codexThreadID == threadID || (b.session != nil && b.session.ID == threadID) {
		b.session = nil
		b.codexThreadID = ""
		b.activeTurnID = ""
	}
	b.mu.Unlock()
	return nil
}

func (b *Bridge) CreateSession(prompt string) (Session, error) {
	now := time.Now().Format(time.RFC3339)
	session := Session{
		ID:        randomID(),
		Title:     "Codex 会话",
		Mode:      "host-new-session",
		Status:    "idle",
		CreatedAt: now,
		UpdatedAt: now,
		Cwd:       b.cwd,
		Note:      "当前没有稳定公开的 Codex 桌面任务附着接口，已降级为宿主机新建会话。",
	}
	if strings.TrimSpace(prompt) != "" {
		session.Title = truncate(prompt, 60)
	}
	b.mu.Lock()
	b.session = &session
	b.mu.Unlock()
	if err := b.ensureReady(); err != nil {
		return Session{}, err
	}
	if err := b.startCodexThread(); err != nil {
		return Session{}, err
	}
	b.mu.Lock()
	session = *b.session
	b.mu.Unlock()
	b.store.UpsertSession(session)
	b.emit("session.status", map[string]interface{}{"mode": session.Mode, "status": session.Status, "note": session.Note})
	if strings.TrimSpace(prompt) != "" {
		if err := b.SendMessage(prompt, session.ID, nil); err != nil {
			return session, err
		}
	}
	return session, nil
}

func (b *Bridge) startCodexThread() error {
	settings := b.store.Settings()
	result, err := b.request("thread/start", withRuntimeOptions(map[string]interface{}{
		"cwd":                   b.cwd,
		"developerInstructions": developerInstructions(settings),
	}, settings))
	if err != nil {
		return err
	}
	thread := asMap(asMap(result)["thread"])
	threadID := stringValue(thread["id"])
	if threadID == "" {
		return errors.New("codex app-server did not return a thread id")
	}
	b.mu.Lock()
	b.codexThreadID = threadID
	if b.session != nil {
		b.session.ID = threadID
	}
	b.mu.Unlock()
	return nil
}

func (b *Bridge) SendMessage(text, sessionID string, attachments []Attachment) error {
	b.mu.Lock()
	missing := b.session == nil || (sessionID != "" && b.session.ID != sessionID)
	b.mu.Unlock()
	if missing {
		if sessionID != "" {
			if _, err := b.ResumeThread(sessionID); err != nil {
				if _, createErr := b.CreateSession(""); createErr != nil {
					return createErr
				}
			}
		} else if _, err := b.CreateSession(""); err != nil {
			return err
		}
	}
	text = messageWithAttachments(text, attachments)
	b.emit("user.message", map[string]interface{}{"text": text, "attachments": attachments})
	b.updateStatus("running")
	b.mu.Lock()
	threadID := b.codexThreadID
	b.mu.Unlock()
	settings := b.store.Settings()
	result, err := b.request("turn/start", withRuntimeOptions(map[string]interface{}{
		"threadId": threadID,
		"input": []map[string]interface{}{
			{"type": "text", "text": text, "text_elements": []interface{}{}},
		},
	}, settings))
	if err != nil {
		return err
	}
	turnID := stringValue(asMap(asMap(result)["turn"])["id"])
	b.mu.Lock()
	b.activeTurnID = turnID
	b.mu.Unlock()
	return nil
}

func (b *Bridge) ResolveApproval(approvalID, decision string) error {
	b.emit("approval.resolved", map[string]interface{}{"approvalId": approvalID, "decision": decision})
	b.mu.Lock()
	requestID, ok := b.pendingApproval[approvalID]
	if ok {
		delete(b.pendingApproval, approvalID)
	}
	b.mu.Unlock()
	if !ok {
		return errors.New("approval request is no longer pending")
	}
	result := map[string]interface{}{"decision": "decline"}
	if decision == "approved" {
		result["decision"] = "accept"
	}
	return b.writeResult(requestID, result)
}

func (b *Bridge) Cancel() error {
	b.mu.Lock()
	session := b.session
	threadID := b.codexThreadID
	turnID := b.activeTurnID
	b.mu.Unlock()
	if session == nil {
		return nil
	}
	if turnID != "" {
		_, _ = b.request("turn/interrupt", map[string]interface{}{"threadId": threadID, "turnId": turnID})
	}
	b.updateStatus("cancelled")
	b.emit("turn.done", map[string]interface{}{"status": "cancelled"})
	return nil
}

func (b *Bridge) hydrateThread(thread map[string]interface{}) {
	turns, _ := thread["turns"].([]interface{})
	limit := intEnv("CODEX_HISTORY_TURN_LIMIT", 10)
	if len(turns) > limit {
		turns = turns[len(turns)-limit:]
	}
	for _, rawTurn := range turns {
		turn := asMap(rawTurn)
		ts := timestampToISO(turn["startedAt"])
		items, _ := turn["items"].([]interface{})
		for _, rawItem := range items {
			item := asMap(rawItem)
			switch stringValue(item["type"]) {
			case "userMessage":
				content, _ := item["content"].([]interface{})
				parts := []string{}
				for _, rawContent := range content {
					contentItem := asMap(rawContent)
					if stringValue(contentItem["type"]) == "text" {
						parts = append(parts, stringValue(contentItem["text"]))
					}
				}
				if text := strings.Join(parts, ""); text != "" {
					b.emitAt("user.message", map[string]interface{}{"text": text}, ts)
				}
			case "agentMessage":
				if text := stringValue(item["text"]); text != "" {
					b.emitAt("assistant.delta", map[string]interface{}{"text": text}, ts)
				}
			case "commandExecution":
				b.emitAt("tool.started", map[string]interface{}{"command": stringValue(firstNonEmpty(item["command"], "命令执行"))}, ts)
				if output := stringValue(item["aggregatedOutput"]); output != "" {
					b.emitAt("tool.output", map[string]interface{}{"text": output}, ts)
				}
			}
		}
	}
}

func (b *Bridge) updateStatus(status string) {
	b.mu.Lock()
	if b.session == nil {
		b.mu.Unlock()
		return
	}
	b.session.Status = status
	b.session.UpdatedAt = time.Now().Format(time.RFC3339)
	mode := b.session.Mode
	session := *b.session
	b.mu.Unlock()
	b.store.UpsertSession(session)
	b.emit("session.status", map[string]interface{}{"status": status, "mode": mode})
}

func (b *Bridge) emit(kind string, payload map[string]interface{}) {
	b.emitAt(kind, payload, time.Now().Format(time.RFC3339))
}

func (b *Bridge) emitAt(kind string, payload map[string]interface{}, ts string) {
	b.mu.Lock()
	if b.session == nil {
		b.mu.Unlock()
		return
	}
	sessionID := b.session.ID
	b.mu.Unlock()
	b.store.Append(Event{SessionID: sessionID, Type: kind, TS: ts, Payload: payload})
}

type App struct {
	store  *Store
	bridge *Bridge
	webDir string
	tokens *TokenStore
}

type TokenStore struct {
	mu     sync.Mutex
	tokens map[string]time.Time
}

func NewTokenStore() *TokenStore {
	return &TokenStore{tokens: map[string]time.Time{}}
}

func (t *TokenStore) Create() string {
	token := randomID() + randomID()
	t.mu.Lock()
	t.tokens[token] = time.Now().Add(30 * 24 * time.Hour)
	t.mu.Unlock()
	return token
}

func (t *TokenStore) Valid(token string) bool {
	if token == "" {
		return false
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	expiresAt, ok := t.tokens[token]
	if !ok {
		return false
	}
	if now.After(expiresAt) {
		delete(t.tokens, token)
		return false
	}
	return true
}

func (t *TokenStore) Delete(token string) {
	t.mu.Lock()
	delete(t.tokens, token)
	t.mu.Unlock()
}

func main() {
	root := workingRoot()
	cwd := getenv("CODEX_CWD", root)
	dataDir := getenv("DATA_DIR", filepath.Join(root, "data-go"))
	if len(os.Args) > 1 && strings.EqualFold(os.Args[1], "login") {
		loginRemoteAgent(dataDir)
		return
	}
	if isRemoteAgentMode() {
		runRemoteAgent(root, cwd, dataDir)
		return
	}
	webDir := getenv("WEB_DIR", defaultWebDir(root))
	host := getenv("HOST", "0.0.0.0")
	port := getenv("PORT", "8787")
	store := NewStore(dataDir)
	app := &App{store: store, bridge: NewBridge(store, cwd), webDir: webDir, tokens: NewTokenStore()}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/status", app.authStatus)
	mux.HandleFunc("/api/auth/login", app.authLogin)
	mux.HandleFunc("/api/auth/logout", app.authLogout)
	mux.HandleFunc("/api/auth/password", app.authPassword)
	mux.HandleFunc("/api/auth/token", app.authToken)
	mux.HandleFunc("/api/openapi.json", app.openapi)
	mux.HandleFunc("/api/settings", app.settings)
	mux.HandleFunc("/api/uploads", app.uploads)
	mux.HandleFunc("/api/health", app.health)
	mux.HandleFunc("/api/sessions", app.sessions)
	mux.HandleFunc("/api/sessions/", app.sessionAction)
	mux.HandleFunc("/api/threads", app.threads)
	mux.HandleFunc("/api/threads/", app.threadAction)
	mux.HandleFunc("/uploads/", app.uploadStatic)
	mux.HandleFunc("/", app.static)

	addr := host + ":" + port
	log.Printf("Codex Go local server: http://%s", strings.Replace(addr, "0.0.0.0", "127.0.0.1", 1))
	log.Printf("Phone/Tailscale URL: http://<tailscale-ip>:%s", port)
	log.Printf("Workspace: %s", cwd)
	log.Fatal(http.ListenAndServe(addr, withCORS(app.requireAuth(mux))))
}

func (a *App) authStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]interface{}{
		"authenticated": a.isAuthenticated(r),
		"passwordSet":   a.store.PasswordSet(),
		"apiToken":      a.store.APITokenStatus(),
	})
}

func (a *App) authLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !a.store.VerifyPassword(body.Password) {
		writeErrorStatus(w, http.StatusUnauthorized, "密码不正确")
		return
	}
	a.setAuthCookie(w, a.tokens.Create())
	writeJSON(w, map[string]interface{}{
		"ok":            true,
		"authenticated": true,
		"passwordSet":   a.store.PasswordSet(),
	})
}

func (a *App) authLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if cookie, err := r.Cookie(authCookieName); err == nil {
		a.tokens.Delete(cookie.Value)
	}
	a.clearAuthCookie(w)
	writeJSON(w, map[string]interface{}{"ok": true})
}

func (a *App) authPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !a.isAuthenticated(r) {
		writeErrorStatus(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	newPassword := strings.TrimSpace(body.NewPassword)
	if len([]rune(newPassword)) < 4 {
		writeErrorStatus(w, http.StatusBadRequest, "新密码至少 4 个字符")
		return
	}
	if a.store.PasswordSet() && !a.store.VerifyPassword(body.CurrentPassword) {
		writeErrorStatus(w, http.StatusUnauthorized, "当前密码不正确")
		return
	}
	if err := a.store.SetPassword(newPassword); err != nil {
		writeError(w, err)
		return
	}
	a.setAuthCookie(w, a.tokens.Create())
	writeJSON(w, map[string]interface{}{"ok": true, "passwordSet": true})
}

func (a *App) authToken(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, a.store.APITokenStatus())
	case http.MethodPost:
		token, status := a.store.RotateAPIToken()
		writeJSONStatus(w, http.StatusCreated, map[string]interface{}{
			"token":  token,
			"status": status,
			"note":   "请立即保存 token，服务端不会再次显示完整值。",
		})
	case http.MethodDelete:
		a.store.ClearAPIToken()
		writeJSON(w, map[string]interface{}{"ok": true, "enabled": false})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *App) openapi(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, openAPISpec())
}

func (a *App) settings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, a.store.Settings())
	case http.MethodPost:
		var body AppSettings
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, a.store.UpdateSettings(body))
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *App) uploads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Name     string `json:"name"`
		MimeType string `json:"mimeType"`
		DataURL  string `json:"dataUrl"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	attachment, err := saveUpload(a.store.dataDir, body.Name, body.MimeType, body.DataURL)
	if err != nil {
		writeErrorStatus(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]interface{}{"attachment": attachment})
}

func (a *App) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]interface{}{
		"ok": true,
		"backend": map[string]interface{}{
			"dataDir":  a.store.path,
			"mode":     "go-single-process",
			"settings": a.store.Settings(),
		},
		"hostAgent": map[string]interface{}{
			"ok": true,
			"desktopAttach": map[string]interface{}{
				"available": false,
				"reason":    "当前没有稳定公开的 Codex 桌面任务附着接口。",
			},
			"codex": a.bridge.Health(),
		},
	})
}

func (a *App) sessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		sessions := a.store.Sessions()
		if current := a.bridge.CurrentSession(); current != nil {
			filtered := []Session{*current}
			for _, session := range sessions {
				if session.ID != current.ID {
					filtered = append(filtered, session)
				}
			}
			sessions = filtered
		}
		writeJSON(w, map[string]interface{}{"sessions": sessions})
	case http.MethodPost:
		var body struct {
			Prompt string `json:"prompt"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		session, err := a.bridge.CreateSession(strings.TrimSpace(body.Prompt))
		if err != nil {
			writeError(w, err)
			return
		}
		a.store.UpsertSession(session)
		writeJSONStatus(w, http.StatusCreated, map[string]interface{}{"session": session})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *App) threads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	sessions, err := a.bridge.ListThreads()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{"sessions": sessions})
}

func (a *App) threadAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/threads/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	threadID := parts[0]
	if len(parts) == 2 && parts[1] == "resume" && r.Method == http.MethodPost {
		a.store.ClearEvents(threadID)
		session, err := a.bridge.ResumeThread(threadID)
		if err != nil {
			writeError(w, err)
			return
		}
		a.store.UpsertSession(session)
		writeJSON(w, map[string]interface{}{"session": session})
		return
	}
	if len(parts) == 1 && r.Method == http.MethodDelete {
		if err := a.bridge.ArchiveThread(threadID); err != nil {
			writeError(w, err)
			return
		}
		a.store.RemoveSession(threadID)
		writeJSON(w, map[string]interface{}{"ok": true})
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (a *App) sessionAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/")
	if len(parts) < 2 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	sessionID, action := parts[0], parts[1]
	switch action {
	case "events":
		a.sse(w, r, sessionID)
	case "messages":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Text        string       `json:"text"`
			Attachments []Attachment `json:"attachments"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.TrimSpace(body.Text) == "" && len(body.Attachments) == 0 {
			writeErrorStatus(w, http.StatusBadRequest, "Message text is required")
			return
		}
		if err := a.bridge.SendMessage(strings.TrimSpace(body.Text), sessionID, body.Attachments); err != nil {
			writeError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusAccepted, map[string]interface{}{"ok": true})
	case "approvals":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			ApprovalID string `json:"approvalId"`
			Decision   string `json:"decision"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Decision != "approved" {
			body.Decision = "rejected"
		}
		if err := a.bridge.ResolveApproval(body.ApprovalID, body.Decision); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true})
	case "cancel":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := a.bridge.Cancel(); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (a *App) sse(w http.ResponseWriter, r *http.Request, sessionID string) {
	after, _ := strconv.ParseInt(firstNonEmptyString(r.URL.Query().Get("after"), r.Header.Get("last-event-id"), "0"), 10, 64)
	limit := intEnv("EVENT_BACKLOG_LIMIT", 120)
	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-cache, no-transform")
	w.Header().Set("connection", "keep-alive")
	w.Header().Set("x-accel-buffering", "no")

	for _, event := range a.store.Events(sessionID, after, limit) {
		writeSSE(w, event)
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	client := a.store.Subscribe(sessionID)
	defer a.store.Unsubscribe(sessionID, client)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			_, _ = w.Write([]byte(": ping\n\n"))
		case event := <-client:
			writeSSE(w, event)
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func (a *App) static(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	requestPath := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if requestPath == "." || requestPath == "" {
		requestPath = "index.html"
	}
	fullPath := filepath.Join(a.webDir, requestPath)
	if info, err := os.Stat(fullPath); err == nil && !info.IsDir() {
		http.ServeFile(w, r, fullPath)
		return
	}
	http.ServeFile(w, r, filepath.Join(a.webDir, "index.html"))
}

func (a *App) uploadStatic(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(strings.TrimPrefix(r.URL.Path, "/uploads/"))
	if name == "." || name == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	http.ServeFile(w, r, filepath.Join(a.store.dataDir, "uploads", name))
}

func (a *App) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions || !strings.HasPrefix(r.URL.Path, "/api/") || a.isPublicAuthPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if !a.store.PasswordSet() || a.isAuthenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		writeErrorStatus(w, http.StatusUnauthorized, "请先登录")
	})
}

func (a *App) isPublicAuthPath(path string) bool {
	switch path {
	case "/api/auth/status", "/api/auth/login", "/api/openapi.json":
		return true
	default:
		return false
	}
}

func (a *App) isAuthenticated(r *http.Request) bool {
	if !a.store.PasswordSet() {
		return true
	}
	if token := bearerToken(r); token != "" && a.store.VerifyAPIToken(token) {
		return true
	}
	cookie, err := r.Cookie(authCookieName)
	return err == nil && a.tokens.Valid(cookie.Value)
}

func (a *App) setAuthCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   30 * 24 * 60 * 60,
	})
}

func (a *App) clearAuthCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("access-control-allow-origin", "*")
		w.Header().Set("access-control-allow-methods", "GET,POST,DELETE,OPTIONS")
		w.Header().Set("access-control-allow-headers", "authorization,content-type,last-event-id")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("authorization"))
	if len(header) < 8 || !strings.EqualFold(header[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(header[7:])
}

func threadToSession(thread map[string]interface{}) Session {
	status := stringValue(thread["status"])
	if statusMap := asMap(thread["status"]); len(statusMap) > 0 {
		status = stringValue(statusMap["type"])
	}
	mappedStatus := "done"
	if status == "active" {
		mappedStatus = "running"
	} else if status == "error" {
		mappedStatus = "error"
	}
	return Session{
		ID:        stringValue(thread["id"]),
		Title:     truncate(redactSensitiveText(firstNonEmptyString(stringValue(thread["name"]), stringValue(thread["preview"]), "Codex 会话")), 80),
		Mode:      "host-new-session",
		Status:    mappedStatus,
		CreatedAt: timestampToISO(thread["createdAt"]),
		UpdatedAt: timestampToISO(thread["updatedAt"]),
		Cwd:       stringValue(thread["cwd"]),
	}
}

func discoverCodexBin() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		return ""
	}
	binRoot := filepath.Join(root, "OpenAI", "Codex", "bin")
	entries, err := os.ReadDir(binRoot)
	if err != nil {
		return ""
	}
	type candidate struct {
		path  string
		mtime time.Time
	}
	candidates := []candidate{}
	for _, entry := range entries {
		path := filepath.Join(binRoot, entry.Name(), "codex.exe")
		info, err := os.Stat(path)
		if err == nil {
			candidates = append(candidates, candidate{path: path, mtime: info.ModTime()})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].mtime.After(candidates[j].mtime) })
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0].path
}

func defaultWebDir(root string) string {
	if exists(filepath.Join(root, "apps", "web", "dist", "index.html")) {
		return filepath.Join(root, "apps", "web", "dist")
	}
	exe, _ := os.Executable()
	return filepath.Join(filepath.Dir(exe), "web")
}

func workingRoot() string {
	cwd, _ := os.Getwd()
	return cwd
}

func writeJSON(w http.ResponseWriter, value interface{}) {
	writeJSONStatus(w, http.StatusOK, value)
}

func writeJSONStatus(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	writeErrorStatus(w, http.StatusInternalServerError, err.Error())
}

func writeErrorStatus(w http.ResponseWriter, status int, message string) {
	writeJSONStatus(w, status, map[string]interface{}{"error": message})
}

func openAPISpec() map[string]interface{} {
	sessionSchema := map[string]interface{}{
		"type":     "object",
		"required": []string{"id", "title", "mode", "status", "createdAt", "updatedAt"},
		"properties": map[string]interface{}{
			"id":        map[string]interface{}{"type": "string"},
			"title":     map[string]interface{}{"type": "string"},
			"mode":      map[string]interface{}{"type": "string", "enum": []string{"desktop-attached", "host-new-session", "disconnected", "error"}},
			"status":    map[string]interface{}{"type": "string"},
			"createdAt": map[string]interface{}{"type": "string", "format": "date-time"},
			"updatedAt": map[string]interface{}{"type": "string", "format": "date-time"},
			"cwd":       map[string]interface{}{"type": "string"},
			"note":      map[string]interface{}{"type": "string"},
		},
	}
	eventSchema := map[string]interface{}{
		"type":     "object",
		"required": []string{"id", "sessionId", "type", "ts", "payload"},
		"properties": map[string]interface{}{
			"id":        map[string]interface{}{"type": "integer"},
			"sessionId": map[string]interface{}{"type": "string"},
			"type":      map[string]interface{}{"type": "string", "enum": []string{"session.status", "user.message", "assistant.delta", "tool.started", "tool.output", "approval.requested", "approval.resolved", "turn.done", "error"}},
			"ts":        map[string]interface{}{"type": "string", "format": "date-time"},
			"payload":   map[string]interface{}{"type": "object", "additionalProperties": true},
		},
	}
	attachmentSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id":       map[string]interface{}{"type": "string"},
			"name":     map[string]interface{}{"type": "string"},
			"mimeType": map[string]interface{}{"type": "string"},
			"path":     map[string]interface{}{"type": "string"},
			"url":      map[string]interface{}{"type": "string"},
		},
	}
	jsonContent := func(schema map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{"application/json": map[string]interface{}{"schema": schema}}
	}
	ok := map[string]interface{}{"description": "OK"}
	return map[string]interface{}{
		"openapi": "3.0.3",
		"info": map[string]interface{}{
			"title":       "Codex Remote Local API",
			"version":     "1.0.0",
			"description": "通过 HTTP/SSE 调用本机 Codex Remote 服务。不要公网裸露此服务。",
		},
		"servers": []map[string]interface{}{{"url": "http://127.0.0.1:8787"}},
		"components": map[string]interface{}{
			"securitySchemes": map[string]interface{}{
				"bearerAuth": map[string]interface{}{"type": "http", "scheme": "bearer"},
				"cookieAuth": map[string]interface{}{"type": "apiKey", "in": "cookie", "name": authCookieName},
			},
			"schemas": map[string]interface{}{
				"Session":     sessionSchema,
				"RemoteEvent": eventSchema,
				"Attachment":  attachmentSchema,
			},
		},
		"paths": map[string]interface{}{
			"/api/auth/status": map[string]interface{}{
				"get": map[string]interface{}{"summary": "查看登录和 API Token 状态", "responses": map[string]interface{}{"200": ok}},
			},
			"/api/auth/login": map[string]interface{}{
				"post": map[string]interface{}{"summary": "网页 Cookie 登录", "requestBody": map[string]interface{}{"content": jsonContent(map[string]interface{}{"type": "object", "properties": map[string]interface{}{"password": map[string]interface{}{"type": "string"}}})}, "responses": map[string]interface{}{"200": ok, "401": ok}},
			},
			"/api/auth/password": map[string]interface{}{
				"post": map[string]interface{}{"summary": "设置或修改访问密码", "security": []map[string]interface{}{{"bearerAuth": []string{}}, {"cookieAuth": []string{}}}, "requestBody": map[string]interface{}{"content": jsonContent(map[string]interface{}{"type": "object", "properties": map[string]interface{}{"currentPassword": map[string]interface{}{"type": "string"}, "newPassword": map[string]interface{}{"type": "string", "minLength": 4}}})}, "responses": map[string]interface{}{"200": ok}},
			},
			"/api/auth/token": map[string]interface{}{
				"get":    map[string]interface{}{"summary": "查看 API Token 状态", "security": []map[string]interface{}{{"bearerAuth": []string{}}, {"cookieAuth": []string{}}}, "responses": map[string]interface{}{"200": ok}},
				"post":   map[string]interface{}{"summary": "创建或轮换 API Token，完整 token 只返回一次", "security": []map[string]interface{}{{"bearerAuth": []string{}}, {"cookieAuth": []string{}}}, "responses": map[string]interface{}{"201": ok}},
				"delete": map[string]interface{}{"summary": "删除 API Token", "security": []map[string]interface{}{{"bearerAuth": []string{}}, {"cookieAuth": []string{}}}, "responses": map[string]interface{}{"200": ok}},
			},
			"/api/health": map[string]interface{}{
				"get": map[string]interface{}{"summary": "健康检查", "security": []map[string]interface{}{{"bearerAuth": []string{}}, {"cookieAuth": []string{}}}, "responses": map[string]interface{}{"200": ok}},
			},
			"/api/settings": map[string]interface{}{
				"get":  map[string]interface{}{"summary": "读取运行设置", "security": []map[string]interface{}{{"bearerAuth": []string{}}, {"cookieAuth": []string{}}}, "responses": map[string]interface{}{"200": ok}},
				"post": map[string]interface{}{"summary": "更新运行设置", "security": []map[string]interface{}{{"bearerAuth": []string{}}, {"cookieAuth": []string{}}}, "requestBody": map[string]interface{}{"content": jsonContent(map[string]interface{}{"type": "object", "properties": map[string]interface{}{"approvalMode": map[string]interface{}{"type": "string", "enum": []string{"on-request", "on-failure", "never"}}, "workMode": map[string]interface{}{"type": "string", "enum": []string{"edit", "plan"}}}})}, "responses": map[string]interface{}{"200": ok}},
			},
			"/api/threads": map[string]interface{}{
				"get": map[string]interface{}{"summary": "列出 Codex 历史对话", "security": []map[string]interface{}{{"bearerAuth": []string{}}, {"cookieAuth": []string{}}}, "responses": map[string]interface{}{"200": map[string]interface{}{"description": "Sessions", "content": jsonContent(map[string]interface{}{"type": "object", "properties": map[string]interface{}{"sessions": map[string]interface{}{"type": "array", "items": sessionSchema}}})}}},
			},
			"/api/threads/{id}/resume": map[string]interface{}{
				"post": map[string]interface{}{"summary": "恢复已有 Codex 对话", "security": []map[string]interface{}{{"bearerAuth": []string{}}, {"cookieAuth": []string{}}}, "parameters": []map[string]interface{}{{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}}}, "responses": map[string]interface{}{"200": ok}},
			},
			"/api/threads/{id}": map[string]interface{}{
				"delete": map[string]interface{}{"summary": "归档/删除 Codex 对话", "security": []map[string]interface{}{{"bearerAuth": []string{}}, {"cookieAuth": []string{}}}, "parameters": []map[string]interface{}{{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}}}, "responses": map[string]interface{}{"200": ok}},
			},
			"/api/sessions": map[string]interface{}{
				"get":  map[string]interface{}{"summary": "列出本地会话缓存", "security": []map[string]interface{}{{"bearerAuth": []string{}}, {"cookieAuth": []string{}}}, "responses": map[string]interface{}{"200": ok}},
				"post": map[string]interface{}{"summary": "新建 Codex 会话", "security": []map[string]interface{}{{"bearerAuth": []string{}}, {"cookieAuth": []string{}}}, "requestBody": map[string]interface{}{"content": jsonContent(map[string]interface{}{"type": "object", "properties": map[string]interface{}{"prompt": map[string]interface{}{"type": "string"}}})}, "responses": map[string]interface{}{"201": ok}},
			},
			"/api/sessions/{id}/events": map[string]interface{}{
				"get": map[string]interface{}{"summary": "SSE 事件流", "security": []map[string]interface{}{{"bearerAuth": []string{}}, {"cookieAuth": []string{}}}, "parameters": []map[string]interface{}{{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}}, {"name": "after", "in": "query", "schema": map[string]interface{}{"type": "integer"}}}, "responses": map[string]interface{}{"200": map[string]interface{}{"description": "text/event-stream", "content": map[string]interface{}{"text/event-stream": map[string]interface{}{"schema": eventSchema}}}}},
			},
			"/api/sessions/{id}/messages": map[string]interface{}{
				"post": map[string]interface{}{"summary": "发送用户消息", "security": []map[string]interface{}{{"bearerAuth": []string{}}, {"cookieAuth": []string{}}}, "parameters": []map[string]interface{}{{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}}}, "requestBody": map[string]interface{}{"content": jsonContent(map[string]interface{}{"type": "object", "properties": map[string]interface{}{"text": map[string]interface{}{"type": "string"}, "attachments": map[string]interface{}{"type": "array", "items": attachmentSchema}}})}, "responses": map[string]interface{}{"202": ok}},
			},
			"/api/sessions/{id}/approvals": map[string]interface{}{
				"post": map[string]interface{}{"summary": "提交审批决定", "security": []map[string]interface{}{{"bearerAuth": []string{}}, {"cookieAuth": []string{}}}, "parameters": []map[string]interface{}{{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}}}, "requestBody": map[string]interface{}{"content": jsonContent(map[string]interface{}{"type": "object", "properties": map[string]interface{}{"approvalId": map[string]interface{}{"type": "string"}, "decision": map[string]interface{}{"type": "string", "enum": []string{"approved", "rejected"}}}})}, "responses": map[string]interface{}{"200": ok}},
			},
			"/api/sessions/{id}/cancel": map[string]interface{}{
				"post": map[string]interface{}{"summary": "取消当前 turn", "security": []map[string]interface{}{{"bearerAuth": []string{}}, {"cookieAuth": []string{}}}, "parameters": []map[string]interface{}{{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}}}, "responses": map[string]interface{}{"200": ok}},
			},
			"/api/uploads": map[string]interface{}{
				"post": map[string]interface{}{"summary": "上传图片附件", "security": []map[string]interface{}{{"bearerAuth": []string{}}, {"cookieAuth": []string{}}}, "requestBody": map[string]interface{}{"content": jsonContent(map[string]interface{}{"type": "object", "properties": map[string]interface{}{"name": map[string]interface{}{"type": "string"}, "mimeType": map[string]interface{}{"type": "string"}, "dataUrl": map[string]interface{}{"type": "string"}}})}, "responses": map[string]interface{}{"201": ok}},
			},
		},
	}
}

func writeSSE(w http.ResponseWriter, event Event) {
	raw, _ := json.Marshal(event)
	_, _ = fmt.Fprintf(w, "id: %d\n", event.ID)
	_, _ = fmt.Fprintf(w, "event: %s\n", event.Type)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
}

func asMap(value interface{}) map[string]interface{} {
	if value == nil {
		return map[string]interface{}{}
	}
	if typed, ok := value.(map[string]interface{}); ok {
		return typed
	}
	return map[string]interface{}{}
}

func numericID(value interface{}) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	default:
		return 0, false
	}
}

func stringValue(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		if math.Trunc(typed) == typed {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(typed, 10)
	case int:
		return strconv.Itoa(typed)
	default:
		if value == nil {
			return ""
		}
		raw, _ := json.Marshal(value)
		return string(raw)
	}
}

func firstNonEmpty(values ...interface{}) interface{} {
	for _, value := range values {
		if str := stringValue(value); str != "" {
			return value
		}
	}
	return ""
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func timestampToISO(value interface{}) string {
	numeric, err := strconv.ParseFloat(stringValue(value), 64)
	if err != nil || numeric <= 0 {
		return time.Now().Format(time.RFC3339)
	}
	if numeric < 10_000_000_000 {
		numeric *= 1000
	}
	return time.UnixMilli(int64(numeric)).Format(time.RFC3339)
}

func defaultSettings() AppSettings {
	return AppSettings{ApprovalMode: "on-request", WorkMode: "edit"}
}

func normalizeSettings(settings AppSettings) AppSettings {
	if settings.ApprovalMode != "on-request" && settings.ApprovalMode != "on-failure" && settings.ApprovalMode != "never" {
		settings.ApprovalMode = "on-request"
	}
	if settings.WorkMode != "edit" && settings.WorkMode != "plan" {
		settings.WorkMode = "edit"
	}
	return settings
}

func developerInstructions(settings AppSettings) string {
	settings = normalizeSettings(settings)
	parts := []string{baseDeveloperInstructions}
	switch settings.WorkMode {
	case "plan":
		parts = append(parts, "当前工作模式为计划模式。先给出清晰计划，除非用户明确要求执行，否则不要直接修改文件或运行高影响命令。")
	case "edit":
		parts = append(parts, "当前工作模式为编辑模式。用户提出修改需求时，优先直接落实代码或文件改动，并在完成后简要说明。")
	default:
		parts = append(parts, "当前工作模式为编辑模式。用户提出修改需求时，优先直接落实代码或文件改动，并在完成后简要说明。")
	}
	switch settings.ApprovalMode {
	case "never":
		parts = append(parts, "审批策略为完全访问权限。除非系统或宿主环境阻止，否则不主动请求用户批准。")
	case "on-failure":
		parts = append(parts, "审批策略为按需升级。常规操作直接执行，仅在遇到权限、沙箱或高风险阻塞时再请求批准。")
	default:
		parts = append(parts, "审批策略为请求批准。涉及外部文件编辑、互联网访问或敏感操作时需要先请求批准。")
	}
	return strings.Join(parts, "\n")
}

func withRuntimeOptions(params map[string]interface{}, settings AppSettings) map[string]interface{} {
	settings = normalizeSettings(settings)
	params["approvalPolicy"] = settings.ApprovalMode
	params["approval_policy"] = settings.ApprovalMode
	if settings.ApprovalMode == "never" {
		params["sandboxMode"] = "danger-full-access"
		params["sandbox_mode"] = "danger-full-access"
	}
	params["workMode"] = settings.WorkMode
	return params
}

func messageWithAttachments(text string, attachments []Attachment) string {
	if len(attachments) == 0 {
		return text
	}
	lines := []string{}
	if strings.TrimSpace(text) != "" {
		lines = append(lines, strings.TrimSpace(text), "")
	}
	lines = append(lines, "附件图片：")
	for _, attachment := range attachments {
		if attachment.Path == "" {
			continue
		}
		name := attachment.Name
		if name == "" {
			name = filepath.Base(attachment.Path)
		}
		lines = append(lines, fmt.Sprintf("- %s：%s", name, attachment.Path))
	}
	lines = append(lines, "", "请读取以上本机图片路径并结合图片内容回答。")
	return strings.Join(lines, "\n")
}

func saveUpload(dataDir, name, mimeType, dataURL string) (Attachment, error) {
	if !strings.HasPrefix(mimeType, "image/") {
		return Attachment{}, errors.New("只支持图片附件")
	}
	comma := strings.Index(dataURL, ",")
	if comma < 0 {
		return Attachment{}, errors.New("图片数据格式不正确")
	}
	raw, err := base64.StdEncoding.DecodeString(dataURL[comma+1:])
	if err != nil {
		return Attachment{}, errors.New("图片数据无法解码")
	}
	if len(raw) > 10*1024*1024 {
		return Attachment{}, errors.New("单张图片不能超过 10MB")
	}
	ext := extensionForMime(mimeType)
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(name))
	}
	if ext == "" {
		ext = ".png"
	}
	id := randomID()
	uploadDir := filepath.Join(dataDir, "uploads")
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		return Attachment{}, err
	}
	filename := id + ext
	path := filepath.Join(uploadDir, filename)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return Attachment{}, err
	}
	return Attachment{ID: id, Name: firstNonEmptyString(name, filename), MimeType: mimeType, Path: path, URL: "/uploads/" + filename}, nil
}

func extensionForMime(mimeType string) string {
	switch strings.ToLower(mimeType) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
}

func hashPassword(password, salt string, iterations int) string {
	sum := sha256.Sum256([]byte(salt + "\x00" + password))
	for i := 1; i < iterations; i++ {
		next := sha256.Sum256(append(sum[:], []byte(salt)...))
		sum = next
	}
	return hex.EncodeToString(sum[:])
}

func hashAPIToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func redactSensitiveText(text string) string {
	words := strings.Fields(text)
	for index, word := range words {
		lower := strings.ToLower(word)
		if strings.Contains(lower, "appsecret") || strings.Contains(lower, "api_key") || strings.Contains(lower, "token") || strings.HasPrefix(lower, "sk-") {
			words[index] = "[已隐藏]"
		}
	}
	return strings.Join(words, " ")
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func randomID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buf)
}

func randomToken(size int) string {
	if size <= 0 {
		size = 32
	}
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return randomID() + randomID()
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func intEnv(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func getenv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func jsonBody(r *http.Request) map[string]interface{} {
	var body map[string]interface{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	return body
}

func jsonBytes(value interface{}) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

func compactJSON(value interface{}) string {
	var buf bytes.Buffer
	_ = json.Compact(&buf, jsonBytes(value))
	return buf.String()
}

func withTimeout(ctx context.Context, duration time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, duration)
}

package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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

const readOnlyThreadNote = "该对话正在由本机 Codex 使用，当前仅可查看历史。"

var errThreadReadOnly = errors.New("该对话正在由本机 Codex 使用，当前仅可查看历史")

type Session struct {
	ID         string      `json:"id"`
	Title      string      `json:"title"`
	Mode       string      `json:"mode"`
	Status     string      `json:"status"`
	CreatedAt  string      `json:"createdAt"`
	UpdatedAt  string      `json:"updatedAt"`
	Cwd        string      `json:"cwd,omitempty"`
	Note       string      `json:"note,omitempty"`
	Model      string      `json:"model,omitempty"`
	TokenUsage *TokenUsage `json:"tokenUsage,omitempty"`
}

type TokenUsageBreakdown struct {
	CachedInputTokens     int64 `json:"cachedInputTokens"`
	InputTokens           int64 `json:"inputTokens"`
	OutputTokens          int64 `json:"outputTokens"`
	ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
	TotalTokens           int64 `json:"totalTokens"`
}

type TokenUsage struct {
	Last               TokenUsageBreakdown `json:"last"`
	Total              TokenUsageBreakdown `json:"total"`
	ModelContextWindow *int64              `json:"modelContextWindow,omitempty"`
}

type ModelOption struct {
	ID                        string                   `json:"id"`
	Model                     string                   `json:"model"`
	DisplayName               string                   `json:"displayName"`
	Description               string                   `json:"description"`
	IsDefault                 bool                     `json:"isDefault"`
	Hidden                    bool                     `json:"hidden"`
	SupportedReasoningEfforts []map[string]interface{} `json:"supportedReasoningEfforts,omitempty"`
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
	settings    AppSettings
	eventHook   func(Event)
	sessionHook func(Session)
}

type storeFile struct {
	Sessions []Session   `json:"sessions"`
	Events   []Event     `json:"events"`
	Settings AppSettings `json:"settings,omitempty"`
}

type AppSettings struct {
	ApprovalMode string `json:"approvalMode"`
	WorkMode     string `json:"workMode"`
	Model        string `json:"model"`
}

type Attachment struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Path     string `json:"path,omitempty"`
	URL      string `json:"url,omitempty"`
	DataURL  string `json:"dataUrl,omitempty"`
}

type QueuedSubmission struct {
	ID                  string                   `json:"id"`
	ClientUserMessageID string                   `json:"clientUserMessageId"`
	Input               []map[string]interface{} `json:"input"`
}

type ThreadGoal struct {
	ThreadID    string `json:"threadId"`
	Objective   string `json:"objective"`
	Status      string `json:"status"`
	TokenBudget *int64 `json:"tokenBudget,omitempty"`
}

func NewStore(dataDir string) *Store {
	_ = os.MkdirAll(dataDir, 0o755)
	store := &Store{
		dataDir:  dataDir,
		path:     filepath.Join(dataDir, "remote-agent-cache.json"),
		sessions: map[string]Session{},
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
	if file.Settings.ApprovalMode != "" || file.Settings.WorkMode != "" || file.Settings.Model != "" {
		s.settings = normalizeSettings(file.Settings)
	}
	for _, event := range s.events {
		if event.ID > s.nextID {
			s.nextID = event.ID
		}
	}
}

func (s *Store) persistLocked() {
	file := storeFile{Events: s.events, Settings: s.settings}
	for _, session := range s.sessions {
		file.Sessions = append(file.Sessions, session)
	}
	sort.Slice(file.Sessions, func(i, j int) bool { return file.Sessions[i].UpdatedAt > file.Sessions[j].UpdatedAt })
	raw, _ := json.MarshalIndent(file, "", "  ")
	_ = os.WriteFile(s.path, raw, 0o644)
}

func (s *Store) UpsertSession(session Session) {
	s.mu.Lock()
	session = mergeSessionMetadata(s.sessions[session.ID], session)
	s.sessions[session.ID] = session
	s.persistLocked()
	hook := s.sessionHook
	s.mu.Unlock()
	if hook != nil {
		hook(session)
	}
}

func (s *Store) EnrichSession(session Session) Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return mergeSessionMetadata(s.sessions[session.ID], session)
}

func mergeSessionMetadata(existing, incoming Session) Session {
	if incoming.Model == "" {
		incoming.Model = existing.Model
	}
	if incoming.TokenUsage == nil {
		incoming.TokenUsage = existing.TokenUsage
	}
	return incoming
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

func (s *Store) EventsBefore(sessionID string, before int64, limit int) ([]Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 6
	}
	events := []Event{}
	for _, event := range s.events {
		if event.SessionID != sessionID || (before > 0 && event.ID >= before) {
			continue
		}
		events = append(events, event)
	}
	if len(events) <= limit {
		return events, false
	}
	return events[len(events)-limit:], true
}

func (s *Store) Append(event Event) Event {
	s.mu.Lock()
	s.nextID++
	event.ID = s.nextID
	s.events = append(s.events, event)
	if len(s.events) > 3000 {
		s.events = s.events[len(s.events)-3000:]
	}
	hook := s.eventHook
	s.persistLocked()
	s.mu.Unlock()
	if hook != nil {
		hook(event)
	}
	return event
}

func (s *Store) AppendLocal(event Event) Event {
	s.mu.Lock()
	s.nextID++
	event.ID = s.nextID
	s.events = append(s.events, event)
	if len(s.events) > 3000 {
		s.events = s.events[len(s.events)-3000:]
	}
	s.persistLocked()
	s.mu.Unlock()
	return event
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

const maxCodexRPCLineBytes = 64 * 1024 * 1024

type Bridge struct {
	mu              sync.Mutex
	rpcMu           sync.Mutex
	resumeMu        sync.Mutex
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
	readOnlyThreads map[string]Session
	requestHook     func(string, interface{}) (interface{}, error)
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
		readOnlyThreads: map[string]Session{},
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
	b.rpcMu.Lock()
	defer b.rpcMu.Unlock()
	b.mu.Lock()
	initialized := b.initialized
	b.mu.Unlock()
	if initialized {
		return nil
	}
	_, err := b.requestLocked("initialize", map[string]interface{}{
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
	if err := b.sendNotificationLocked("initialized", map[string]interface{}{}); err != nil {
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
	hideConsoleWindow(cmd)
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
	scanner.Buffer(make([]byte, 0, 64*1024), maxCodexRPCLineBytes)
	for scanner.Scan() {
		b.handleLine(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		b.failPending(fmt.Errorf("读取 Codex app-server 响应失败: %w", err))
	}
}

func (b *Bridge) failPending(err error) {
	b.mu.Lock()
	pending := b.pending
	b.pending = map[int64]pendingCall{}
	b.mu.Unlock()
	for _, call := range pending {
		select {
		case call.result <- rpcMessage{Error: err.Error()}:
		default:
		}
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
	if b.requestHook != nil {
		return b.requestHook(method, params)
	}
	b.rpcMu.Lock()
	defer b.rpcMu.Unlock()
	return b.requestLocked(method, params)
}

func (b *Bridge) requestLocked(method string, params interface{}) (interface{}, error) {
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

func (b *Bridge) sendNotificationLocked(method string, params interface{}) error {
	b.mu.Lock()
	stdin := b.stdin
	b.mu.Unlock()
	if stdin == nil {
		return errors.New("codex app-server process is not running")
	}
	raw, _ := json.Marshal(rpcMessage{JSONRPC: "2.0", Method: method, Params: params})
	_, err := stdin.Write(append(raw, '\n'))
	return err
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
	case "turn/started":
		turn := asMap(payload["turn"])
		turnID := stringValue(firstNonEmpty(turn["id"], payload["turnId"]))
		threadID := stringValue(firstNonEmpty(turn["threadId"], payload["threadId"]))
		b.mu.Lock()
		if threadID == "" || threadID == b.codexThreadID {
			b.activeTurnID = turnID
		}
		b.mu.Unlock()
		b.updateStatus("running")
	case "turn/completed":
		turn := asMap(payload["turn"])
		threadID := stringValue(firstNonEmpty(turn["threadId"], payload["threadId"]))
		b.mu.Lock()
		if threadID == "" || threadID == b.codexThreadID {
			b.activeTurnID = ""
		}
		b.mu.Unlock()
		b.updateStatus("done")
		b.emit("turn.done", payload)
		if threadID != "" {
			go b.startNextQueuedSubmission(threadID)
		}
	case "thread/queue/changed":
		threadID := stringValue(payload["threadId"])
		b.emitForSession(threadID, "queue.changed", map[string]interface{}{})
	case "thread/goal/updated":
		threadID := stringValue(payload["threadId"])
		b.emitForSession(threadID, "goal.updated", map[string]interface{}{"goal": payload["goal"]})
	case "thread/goal/cleared":
		threadID := stringValue(payload["threadId"])
		b.emitForSession(threadID, "goal.cleared", map[string]interface{}{})
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
	case "thread/tokenUsage/updated":
		threadID := stringValue(payload["threadId"])
		usage := parseTokenUsage(payload["tokenUsage"])
		if threadID != "" && usage != nil {
			b.updateTokenUsage(threadID, usage)
			b.emitForSession(threadID, "context.usage", map[string]interface{}{"usage": usage})
		}
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
		sessions = append(sessions, b.store.EnrichSession(threadToSession(asMap(raw))))
	}
	return sessions, nil
}

func (b *Bridge) ListModels() ([]ModelOption, error) {
	if err := b.ensureReady(); err != nil {
		return nil, err
	}
	result, err := b.request("model/list", map[string]interface{}{"limit": 100, "includeHidden": false})
	if err != nil {
		return nil, err
	}
	data, _ := asMap(result)["data"].([]interface{})
	models := make([]ModelOption, 0, len(data))
	for _, raw := range data {
		item := asMap(raw)
		model := ModelOption{
			ID:          stringValue(item["id"]),
			Model:       stringValue(item["model"]),
			DisplayName: stringValue(item["displayName"]),
			Description: stringValue(item["description"]),
			IsDefault:   boolValue(item["isDefault"]),
			Hidden:      boolValue(item["hidden"]),
		}
		if model.Model == "" {
			model.Model = model.ID
		}
		if model.ID == "" {
			model.ID = model.Model
		}
		if model.Model != "" {
			models = append(models, model)
		}
	}
	return models, nil
}

func (b *Bridge) ResumeThread(threadID string) (Session, error) {
	b.resumeMu.Lock()
	defer b.resumeMu.Unlock()
	if err := b.ensureReady(); err != nil {
		return Session{}, err
	}
	b.mu.Lock()
	if b.session != nil && b.codexThreadID == threadID {
		session := *b.session
		b.mu.Unlock()
		return session, nil
	}
	b.mu.Unlock()
	settings := b.store.Settings()
	result, err := b.request("thread/resume", withRuntimeOptions(map[string]interface{}{
		"threadId":              threadID,
		"developerInstructions": developerInstructions(settings),
	}, settings))
	if err != nil {
		if !isActiveWriterError(err) {
			return Session{}, err
		}
		return b.readThreadWhileWriterIsActive(threadID)
	}
	thread := asMap(asMap(result)["thread"])
	session := threadToSession(thread)
	session.Model = stringValue(asMap(result)["model"])
	session = b.store.EnrichSession(session)
	b.mu.Lock()
	b.codexThreadID = session.ID
	b.session = &session
	delete(b.readOnlyThreads, session.ID)
	b.mu.Unlock()
	b.store.ClearEvents(session.ID)
	b.emit("session.status", map[string]interface{}{"status": session.Status, "mode": session.Mode})
	b.hydrateThread(session.ID, thread)
	return session, nil
}

func (b *Bridge) readThreadWhileWriterIsActive(threadID string) (Session, error) {
	result, err := b.request("thread/read", map[string]interface{}{
		"threadId":     threadID,
		"includeTurns": true,
	})
	if err != nil {
		return Session{}, fmt.Errorf("读取被本机 Codex 占用的对话失败: %w", err)
	}
	thread := asMap(asMap(result)["thread"])
	session := threadToSession(thread)
	if session.ID == "" {
		return Session{}, errors.New("Codex app-server 未返回对话 ID")
	}
	session.Mode = "host-readonly"
	session.Note = readOnlyThreadNote
	session = b.store.EnrichSession(session)
	b.mu.Lock()
	if b.readOnlyThreads == nil {
		b.readOnlyThreads = map[string]Session{}
	}
	b.readOnlyThreads[session.ID] = session
	b.mu.Unlock()
	b.store.ClearEvents(session.ID)
	b.store.UpsertSession(session)
	b.hydrateThread(session.ID, thread)
	return session, nil
}

func isActiveWriterError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "already has an active writer")
}

func (b *Bridge) ArchiveThread(threadID string) error {
	b.mu.Lock()
	_, readOnly := b.readOnlyThreads[threadID]
	b.mu.Unlock()
	if readOnly {
		session, err := b.ResumeThread(threadID)
		if err != nil {
			return err
		}
		if session.Mode == "host-readonly" {
			return errThreadReadOnly
		}
	}
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
	params := withRuntimeOptions(map[string]interface{}{
		"cwd":                   b.cwd,
		"developerInstructions": developerInstructions(settings),
	}, settings)
	result, err := b.request("thread/start", withModelOption(params, settings))
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
		b.session.Model = stringValue(asMap(result)["model"])
	}
	b.mu.Unlock()
	return nil
}

func (b *Bridge) SendMessage(text, sessionID string, attachments []Attachment) error {
	if err := b.ensureWritableSession(sessionID); err != nil {
		return err
	}
	input := messageInput(text, attachments)
	if len(input) == 0 {
		return errors.New("消息不能为空")
	}
	b.emitUserInput(input)
	b.updateStatus("running")
	b.mu.Lock()
	threadID := b.codexThreadID
	b.mu.Unlock()
	settings := b.store.Settings()
	params := withRuntimeOptions(map[string]interface{}{
		"threadId": threadID,
		"input":    input,
	}, settings)
	result, err := b.request("turn/start", withModelOption(params, settings))
	if err != nil {
		return err
	}
	turnID := stringValue(asMap(asMap(result)["turn"])["id"])
	b.mu.Lock()
	b.activeTurnID = turnID
	b.mu.Unlock()
	return nil
}

func (b *Bridge) ensureSession(sessionID string) error {
	b.mu.Lock()
	missing := b.session == nil || (sessionID != "" && b.session.ID != sessionID)
	b.mu.Unlock()
	if !missing {
		return nil
	}
	if sessionID != "" {
		session, err := b.ResumeThread(sessionID)
		if err != nil {
			return err
		}
		if session.Mode == "host-readonly" {
			return errThreadReadOnly
		}
		return nil
	}
	_, err := b.CreateSession("")
	return err
}

func (b *Bridge) ensureWritableSession(sessionID string) error {
	return b.ensureSession(sessionID)
}

func (b *Bridge) QueueList(sessionID string) ([]QueuedSubmission, error) {
	if err := b.ensureSession(sessionID); err != nil {
		return nil, err
	}
	b.mu.Lock()
	threadID := b.codexThreadID
	b.mu.Unlock()
	result, err := b.request("thread/queue/list", map[string]interface{}{"threadId": threadID, "limit": 100})
	if err != nil {
		return nil, err
	}
	data, _ := asMap(result)["data"].([]interface{})
	queue := make([]QueuedSubmission, 0, len(data))
	for _, raw := range data {
		item := asMap(raw)
		input, _ := item["input"].([]interface{})
		converted := make([]map[string]interface{}, 0, len(input))
		for _, inputItem := range input {
			converted = append(converted, asMap(inputItem))
		}
		queue = append(queue, QueuedSubmission{
			ID:                  stringValue(item["id"]),
			ClientUserMessageID: stringValue(item["clientUserMessageId"]),
			Input:               converted,
		})
	}
	return queue, nil
}

func (b *Bridge) QueueAdd(sessionID, text string, attachments []Attachment) (QueuedSubmission, error) {
	if err := b.ensureWritableSession(sessionID); err != nil {
		return QueuedSubmission{}, err
	}
	input := messageInput(text, attachments)
	if len(input) == 0 {
		return QueuedSubmission{}, errors.New("消息不能为空")
	}
	b.mu.Lock()
	threadID := b.codexThreadID
	b.mu.Unlock()
	result, err := b.request("thread/queue/add", map[string]interface{}{
		"threadId":            threadID,
		"clientUserMessageId": randomID(),
		"input":               input,
	})
	if err != nil {
		return QueuedSubmission{}, err
	}
	item := asMap(asMap(result)["queuedSubmission"])
	queued := QueuedSubmission{ID: stringValue(item["id"]), ClientUserMessageID: stringValue(item["clientUserMessageId"])}
	for _, raw := range asInterfaceSlice(item["input"]) {
		queued.Input = append(queued.Input, asMap(raw))
	}
	return queued, nil
}

func (b *Bridge) QueueUpdate(sessionID, submissionID string, input []map[string]interface{}) (QueuedSubmission, error) {
	if err := b.ensureWritableSession(sessionID); err != nil {
		return QueuedSubmission{}, err
	}
	if submissionID == "" || len(input) == 0 {
		return QueuedSubmission{}, errors.New("排队消息不完整")
	}
	b.mu.Lock()
	threadID := b.codexThreadID
	b.mu.Unlock()
	result, err := b.request("thread/queue/update", map[string]interface{}{
		"threadId":           threadID,
		"queuedSubmissionId": submissionID,
		"input":              input,
	})
	if err != nil {
		return QueuedSubmission{}, err
	}
	item := asMap(asMap(result)["queuedSubmission"])
	queued := QueuedSubmission{ID: stringValue(item["id"]), ClientUserMessageID: stringValue(item["clientUserMessageId"])}
	for _, raw := range asInterfaceSlice(item["input"]) {
		queued.Input = append(queued.Input, asMap(raw))
	}
	return queued, nil
}

func (b *Bridge) QueueDelete(sessionID, submissionID string) error {
	if err := b.ensureWritableSession(sessionID); err != nil {
		return err
	}
	b.mu.Lock()
	threadID := b.codexThreadID
	b.mu.Unlock()
	_, err := b.request("thread/queue/delete", map[string]interface{}{"threadId": threadID, "queuedSubmissionId": submissionID})
	return err
}

func (b *Bridge) QueueReorder(sessionID string, submissionIDs []string) error {
	if err := b.ensureWritableSession(sessionID); err != nil {
		return err
	}
	b.mu.Lock()
	threadID := b.codexThreadID
	b.mu.Unlock()
	_, err := b.request("thread/queue/reorder", map[string]interface{}{"threadId": threadID, "queuedSubmissionIds": submissionIDs})
	return err
}

func (b *Bridge) PromoteQueue(sessionID, submissionID string) error {
	if err := b.ensureWritableSession(sessionID); err != nil {
		return err
	}
	queue, err := b.QueueList(sessionID)
	if err != nil {
		return err
	}
	var selected *QueuedSubmission
	for index := range queue {
		if queue[index].ID == submissionID {
			selected = &queue[index]
			break
		}
	}
	if selected == nil {
		return errors.New("排队消息不存在")
	}
	b.mu.Lock()
	threadID, turnID := b.codexThreadID, b.activeTurnID
	b.mu.Unlock()
	if turnID != "" {
		params := map[string]interface{}{"threadId": threadID, "expectedTurnId": turnID, "input": selected.Input}
		if selected.ClientUserMessageID != "" {
			params["clientUserMessageId"] = selected.ClientUserMessageID
		}
		if _, err := b.request("turn/steer", params); err != nil {
			return err
		}
		if err := b.QueueDelete(sessionID, selected.ID); err != nil {
			return err
		}
		b.emitUserInput(selected.Input)
		return nil
	}
	return b.startQueuedSubmission(threadID, selected)
}

func (b *Bridge) startQueuedSubmission(threadID string, queued *QueuedSubmission) error {
	if queued == nil || queued.ID == "" {
		return errors.New("排队消息不存在")
	}
	result, err := b.request("thread/queue/start", map[string]interface{}{"threadId": threadID, "queuedSubmissionId": queued.ID})
	if err != nil {
		return err
	}
	turnID := stringValue(asMap(asMap(result)["turn"])["id"])
	b.mu.Lock()
	b.activeTurnID = turnID
	b.mu.Unlock()
	b.emitUserInput(queued.Input)
	b.updateStatus("running")
	return nil
}

func (b *Bridge) startNextQueuedSubmission(threadID string) {
	b.mu.Lock()
	activeThreadID, activeTurnID := b.codexThreadID, b.activeTurnID
	b.mu.Unlock()
	if threadID == "" || threadID != activeThreadID || activeTurnID != "" {
		return
	}
	queue, err := b.QueueList(threadID)
	if err != nil || len(queue) == 0 {
		return
	}
	_ = b.startQueuedSubmission(threadID, &queue[0])
}

func (b *Bridge) GetGoal(sessionID string) (*ThreadGoal, error) {
	if err := b.ensureSession(sessionID); err != nil {
		return nil, err
	}
	b.mu.Lock()
	threadID := b.codexThreadID
	b.mu.Unlock()
	result, err := b.request("thread/goal/get", map[string]interface{}{"threadId": threadID})
	if err != nil {
		return nil, err
	}
	goal := asMap(result)["goal"]
	if goal == nil {
		return nil, nil
	}
	raw, _ := json.Marshal(goal)
	var parsed ThreadGoal
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	return &parsed, nil
}

func (b *Bridge) SetGoal(sessionID, objective string) (*ThreadGoal, error) {
	if err := b.ensureWritableSession(sessionID); err != nil {
		return nil, err
	}
	b.mu.Lock()
	threadID := b.codexThreadID
	b.mu.Unlock()
	result, err := b.request("thread/goal/set", map[string]interface{}{"threadId": threadID, "objective": strings.TrimSpace(objective), "status": "active"})
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(asMap(result)["goal"])
	var goal ThreadGoal
	if err := json.Unmarshal(raw, &goal); err != nil {
		return nil, err
	}
	return &goal, nil
}

func (b *Bridge) ClearGoal(sessionID string) error {
	if err := b.ensureWritableSession(sessionID); err != nil {
		return err
	}
	b.mu.Lock()
	threadID := b.codexThreadID
	b.mu.Unlock()
	_, err := b.request("thread/goal/clear", map[string]interface{}{"threadId": threadID})
	return err
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

func (b *Bridge) hydrateThread(sessionID string, thread map[string]interface{}) {
	if sessionID == "" {
		return
	}
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
					b.store.AppendLocal(Event{SessionID: sessionID, Type: "user.message", TS: ts, Payload: map[string]interface{}{"text": text}})
				}
			case "agentMessage":
				if text := stringValue(item["text"]); text != "" {
					b.store.AppendLocal(Event{SessionID: sessionID, Type: "assistant.delta", TS: ts, Payload: map[string]interface{}{"text": text}})
				}
			case "commandExecution":
				b.store.AppendLocal(Event{SessionID: sessionID, Type: "tool.started", TS: ts, Payload: map[string]interface{}{"command": stringValue(firstNonEmpty(item["command"], "命令执行"))}})
				if output := stringValue(item["aggregatedOutput"]); output != "" {
					b.store.AppendLocal(Event{SessionID: sessionID, Type: "tool.output", TS: ts, Payload: map[string]interface{}{"text": output}})
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

func (b *Bridge) updateTokenUsage(threadID string, usage *TokenUsage) {
	b.mu.Lock()
	if b.session == nil || b.session.ID != threadID {
		b.mu.Unlock()
		return
	}
	b.session.TokenUsage = usage
	session := *b.session
	b.mu.Unlock()
	b.store.UpsertSession(session)
}

func (b *Bridge) emitForSession(sessionID, kind string, payload map[string]interface{}) {
	b.store.Append(Event{SessionID: sessionID, Type: kind, TS: time.Now().Format(time.RFC3339), Payload: payload})
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

func parseTokenUsage(value interface{}) *TokenUsage {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var usage TokenUsage
	if json.Unmarshal(raw, &usage) != nil {
		return nil
	}
	return &usage
}

func main() {
	root := workingRoot()
	cwd := getenv("CODEX_CWD", root)
	dataDir := getenv("DATA_DIR", defaultRemoteAgentDataDir(root))
	if os.Getenv("DATA_DIR") == "" {
		migrateRemoteAgentData(root, dataDir)
	}
	if len(os.Args) > 1 && strings.EqualFold(os.Args[1], "login") {
		loginRemoteAgent(dataDir)
		return
	}
	if isRemoteAgentMode() {
		runRemoteAgent(root, cwd, dataDir)
		return
	}
	if len(os.Args) == 1 {
		if err := runClientGUI(root, cwd, dataDir); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		return
	}
	fmt.Fprintln(os.Stderr, "用法: codex-remote-agent（图形界面）或 login --server <服务端地址> --token <Token> [--device <设备名称>]；登录后运行 agent")
	os.Exit(2)
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

func workingRoot() string {
	cwd, _ := os.Getwd()
	return cwd
}

func defaultRemoteAgentDataDir(root string) string {
	if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
		return filepath.Join(localAppData, "Codex Link", "remote-agent")
	}
	if configDir, err := os.UserConfigDir(); err == nil && strings.TrimSpace(configDir) != "" {
		return filepath.Join(configDir, "codex-link", "remote-agent")
	}
	return filepath.Join(root, "data-remote-agent")
}

func migrateRemoteAgentData(root, dataDir string) {
	legacyDir := filepath.Join(root, "data-remote-agent")
	if filepath.Clean(legacyDir) == filepath.Clean(dataDir) {
		return
	}
	entries, err := os.ReadDir(legacyDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		source := filepath.Join(legacyDir, entry.Name())
		target := filepath.Join(dataDir, entry.Name())
		if _, err := os.Stat(target); err == nil {
			continue
		}
		raw, err := os.ReadFile(source)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			return
		}
		_ = os.WriteFile(target, raw, 0o600)
	}
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

func boolValue(value interface{}) bool {
	typed, ok := value.(bool)
	return ok && typed
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
	return AppSettings{ApprovalMode: "on-request", WorkMode: "edit", Model: ""}
}

func normalizeSettings(settings AppSettings) AppSettings {
	if settings.ApprovalMode != "on-request" && settings.ApprovalMode != "on-failure" && settings.ApprovalMode != "never" {
		settings.ApprovalMode = "on-request"
	}
	if settings.WorkMode != "edit" && settings.WorkMode != "plan" {
		settings.WorkMode = "edit"
	}
	settings.Model = strings.TrimSpace(settings.Model)
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

func withModelOption(params map[string]interface{}, settings AppSettings) map[string]interface{} {
	if model := strings.TrimSpace(settings.Model); model != "" {
		params["model"] = model
	}
	return params
}

func messageInput(text string, attachments []Attachment) []map[string]interface{} {
	input := []map[string]interface{}{}
	if text = strings.TrimSpace(text); text != "" {
		input = append(input, map[string]interface{}{"type": "text", "text": text, "text_elements": []interface{}{}})
	}
	for _, attachment := range attachments {
		if attachment.Path == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(attachment.MimeType), "image/") {
			input = append(input, map[string]interface{}{"type": "localImage", "path": attachment.Path})
			continue
		}
		name := firstNonEmptyString(attachment.Name, filepath.Base(attachment.Path))
		input = append(input, map[string]interface{}{"type": "mention", "name": name, "path": attachment.Path})
	}
	return input
}

func asInterfaceSlice(value interface{}) []interface{} {
	items, _ := value.([]interface{})
	return items
}

func (b *Bridge) emitUserInput(input []map[string]interface{}) {
	text := []string{}
	attachments := []Attachment{}
	for _, item := range input {
		switch stringValue(item["type"]) {
		case "text":
			if value := strings.TrimSpace(stringValue(item["text"])); value != "" {
				text = append(text, value)
			}
		case "localImage", "mention":
			path := stringValue(item["path"])
			if path != "" {
				attachments = append(attachments, Attachment{Name: firstNonEmptyString(stringValue(item["name"]), filepath.Base(path)), Path: path})
			}
		}
	}
	b.emit("user.message", map[string]interface{}{"text": strings.Join(text, "\n"), "attachments": attachments})
}

func saveUpload(dataDir, name, mimeType, dataURL string) (Attachment, error) {
	comma := strings.Index(dataURL, ",")
	if comma < 0 {
		return Attachment{}, errors.New("文件数据格式不正确")
	}
	raw, err := base64.StdEncoding.DecodeString(dataURL[comma+1:])
	if err != nil {
		return Attachment{}, errors.New("文件数据无法解码")
	}
	if len(raw) == 0 || len(raw) > 16*1024*1024 {
		return Attachment{}, errors.New("单个文件必须小于 16MB")
	}
	ext := extensionForMime(mimeType)
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(name))
	}
	if ext == "" {
		ext = ".bin"
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

func getenv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func intEnv(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

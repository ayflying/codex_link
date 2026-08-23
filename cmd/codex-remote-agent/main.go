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
	hook := s.eventHook
	s.persistLocked()
	s.mu.Unlock()
	if hook != nil {
		hook(event)
	}
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

func main() {
	root := workingRoot()
	cwd := getenv("CODEX_CWD", root)
	dataDir := getenv("DATA_DIR", filepath.Join(root, "data-remote-agent"))
	if len(os.Args) > 1 && strings.EqualFold(os.Args[1], "login") {
		loginRemoteAgent(dataDir)
		return
	}
	if isRemoteAgentMode() {
		runRemoteAgent(root, cwd, dataDir)
		return
	}
	fmt.Fprintln(os.Stderr, "用法: codex-remote-agent login --server <服务端地址> --token <Token> [--device <设备名称>]；登录后运行 agent")
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

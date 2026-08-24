package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type recordingWriteCloser struct {
	bytes.Buffer
}

func (recordingWriteCloser) Close() error { return nil }

func TestSendNotificationUsesJSONRPCNotificationShape(t *testing.T) {
	writer := &recordingWriteCloser{}
	bridge := &Bridge{stdin: writer}
	if err := bridge.sendNotificationLocked("initialized", map[string]interface{}{}); err != nil {
		t.Fatalf("send initialized notification: %v", err)
	}

	var message rpcMessage
	if err := json.Unmarshal(bytes.TrimSpace(writer.Bytes()), &message); err != nil {
		t.Fatalf("decode notification: %v", err)
	}
	if message.JSONRPC != "2.0" || message.Method != "initialized" || message.ID != nil {
		t.Fatalf("unexpected notification envelope: %#v", message)
	}
}

func TestDefaultRemoteAgentDataDirUsesLocalAppData(t *testing.T) {
	localAppData := t.TempDir()
	t.Setenv("LOCALAPPDATA", localAppData)

	got := defaultRemoteAgentDataDir(t.TempDir())
	want := filepath.Join(localAppData, "Codex Link", "remote-agent")
	if got != want {
		t.Fatalf("expected data directory %q, got %q", want, got)
	}
}

func TestMigrateRemoteAgentDataCopiesOnlyMissingFiles(t *testing.T) {
	root := t.TempDir()
	legacyDir := filepath.Join(root, "data-remote-agent")
	dataDir := filepath.Join(t.TempDir(), "remote-agent")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatalf("create legacy directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "remote-agent.json"), []byte("legacy"), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("create target directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "remote-agent-cache.json"), []byte("current"), 0o600); err != nil {
		t.Fatalf("write current cache: %v", err)
	}

	migrateRemoteAgentData(root, dataDir)
	legacy, err := os.ReadFile(filepath.Join(dataDir, "remote-agent.json"))
	if err != nil || string(legacy) != "legacy" {
		t.Fatalf("legacy config was not migrated: %q, %v", legacy, err)
	}
	current, err := os.ReadFile(filepath.Join(dataDir, "remote-agent-cache.json"))
	if err != nil || string(current) != "current" {
		t.Fatalf("current cache was overwritten: %q, %v", current, err)
	}
}

func TestLoginRemoteAgentConfigSavesConfigAfterServerLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/agent/login" || request.Method != http.MethodPost {
			t.Errorf("unexpected login request: %s %s", request.Method, request.URL.Path)
		}
		var payload map[string]string
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode login request: %v", err)
		}
		if payload["token"] != "crs_test-token" || payload["deviceName"] != "测试电脑" {
			t.Errorf("unexpected login payload: %#v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"deviceId":"device-server","deviceName":"测试电脑"}`))
	}))
	defer server.Close()

	dataDir := filepath.Join(t.TempDir(), "remote-agent")
	config, err := loginRemoteAgentConfig(dataDir, server.URL, " crs_test-token ", " 测试电脑 ")
	if err != nil {
		t.Fatalf("login should succeed in writable data directory: %v", err)
	}
	if config.DeviceID != "device-server" {
		t.Fatalf("expected server device ID, got %q", config.DeviceID)
	}
	loaded, err := loadRemoteAgentConfig(dataDir)
	if err != nil || loaded.DeviceID != config.DeviceID || loaded.Token != config.Token {
		t.Fatalf("saved config could not be loaded: %#v, %v", loaded, err)
	}
}

func TestLoginRemoteAgentConfigReportsRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	originalClient := remoteHTTPClient
	remoteHTTPClient = &http.Client{Timeout: 20 * time.Millisecond}
	t.Cleanup(func() { remoteHTTPClient = originalClient })

	_, err := loginRemoteAgentConfig(t.TempDir(), server.URL, "crs_test-token", "测试电脑")
	if err == nil || !strings.Contains(err.Error(), "连接服务端超时") {
		t.Fatalf("expected a clear timeout error, got %v", err)
	}
}

func TestRemoteHTTPURLRequiresHost(t *testing.T) {
	_, err := remoteHTTPURL(remoteAgentConfig{ServerURL: "http://"})
	if err == nil || !strings.Contains(err.Error(), "必须包含主机名") {
		t.Fatalf("expected a missing-host error, got %v", err)
	}
}

func TestLoginDeviceIDReusesConfigForSameServer(t *testing.T) {
	dataDir := t.TempDir()
	config := remoteAgentConfig{
		ServerURL:  "https://relay.example",
		Token:      "crs_test-token",
		DeviceID:   "device-existing",
		DeviceName: "测试电脑",
	}
	if err := saveRemoteAgentConfig(dataDir, config); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if got := loginDeviceID(dataDir, "https://relay.example/"); got != config.DeviceID {
		t.Fatalf("expected existing device ID %q, got %q", config.DeviceID, got)
	}
}

func TestLoginDeviceIDCreatesNewIdentityForDifferentServer(t *testing.T) {
	dataDir := t.TempDir()
	if err := saveRemoteAgentConfig(dataDir, remoteAgentConfig{
		ServerURL: "https://relay.example",
		Token:     "crs_test-token",
		DeviceID:  "device-existing",
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if got := loginDeviceID(dataDir, "https://other-relay.example"); got != "" {
		t.Fatalf("expected no reused device ID, got %q", got)
	}
}

func TestResumeThreadReusesAttachedThreadAndKeepsEvents(t *testing.T) {
	store := NewStore(t.TempDir())
	threadID := "thread-existing"
	store.Append(Event{SessionID: threadID, Type: "assistant.delta", Payload: map[string]interface{}{"text": "保留"}})

	bridge := &Bridge{
		cmd:           &exec.Cmd{},
		initialized:   true,
		codexThreadID: threadID,
		session:       &Session{ID: threadID, Title: "已有会话", Status: "idle"},
		store:         store,
	}
	got, err := bridge.ResumeThread(threadID)
	if err != nil {
		t.Fatalf("resume existing thread: %v", err)
	}
	if got.ID != threadID {
		t.Fatalf("expected thread %q, got %q", threadID, got.ID)
	}
	if events := store.Events(threadID, 0, 10); len(events) != 1 || events[0].Payload["text"] != "保留" {
		t.Fatalf("existing events were not preserved: %#v", events)
	}
}

func TestResumeThreadReadsHistoryWhenAnotherWriterIsActive(t *testing.T) {
	store := NewStore(t.TempDir())
	attachedID := "thread-attached"
	busyID := "thread-busy"
	requests := []string{}
	bridge := &Bridge{
		cmd:             &exec.Cmd{},
		initialized:     true,
		codexThreadID:   attachedID,
		session:         &Session{ID: attachedID, Title: "当前可写会话", Mode: "host-new-session", Status: "idle"},
		store:           store,
		readOnlyThreads: map[string]Session{},
		requestHook: func(method string, params interface{}) (interface{}, error) {
			requests = append(requests, method)
			switch method {
			case "thread/resume":
				return nil, errors.New("thread/resume error: map[code:-32600 message:thread thread-busy already has an active writer]")
			case "thread/read":
				body := params.(map[string]interface{})
				if body["threadId"] != busyID || body["includeTurns"] != true {
					t.Fatalf("unexpected thread/read params: %#v", body)
				}
				return map[string]interface{}{"thread": map[string]interface{}{
					"id": busyID, "name": "桌面正在使用的会话", "status": "active",
					"turns": []interface{}{map[string]interface{}{
						"startedAt": 0,
						"items": []interface{}{
							map[string]interface{}{"type": "userMessage", "content": []interface{}{map[string]interface{}{"type": "text", "text": "历史问题"}}},
							map[string]interface{}{"type": "agentMessage", "text": "历史回答"},
						},
					}},
				}}, nil
			default:
				t.Fatalf("unexpected app-server request: %s", method)
				return nil, nil
			}
		},
	}

	session, err := bridge.ResumeThread(busyID)
	if err != nil {
		t.Fatalf("active-writer fallback should succeed: %v", err)
	}
	if session.Mode != "host-readonly" || session.Note != readOnlyThreadNote {
		t.Fatalf("expected read-only session, got %#v", session)
	}
	if got := bridge.CurrentSession(); got == nil || got.ID != attachedID {
		t.Fatalf("active writable session was replaced: %#v", got)
	}
	if len(requests) != 2 || requests[0] != "thread/resume" || requests[1] != "thread/read" {
		t.Fatalf("expected resume then read fallback, got %#v", requests)
	}
	events := store.Events(busyID, 0, 10)
	if len(events) != 2 || events[0].Payload["text"] != "历史问题" || events[1].Payload["text"] != "历史回答" {
		t.Fatalf("history was not hydrated: %#v", events)
	}
	if err := bridge.SendMessage("不应发送", busyID, nil); !errors.Is(err, errThreadReadOnly) {
		t.Fatalf("expected read-only write rejection, got %v", err)
	}
	if _, err := bridge.QueueList(busyID); !errors.Is(err, errThreadReadOnly) {
		t.Fatalf("expected read-only queue rejection, got %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("read-only commands should not retry app-server resume: %#v", requests)
	}
}

func TestStoreEventsBeforePagesHistoryWithoutBroadcastingLocalHydration(t *testing.T) {
	store := NewStore(t.TempDir())
	hooked := 0
	store.SetEventHook(func(Event) { hooked++ })
	for index := 0; index < 8; index++ {
		store.Append(Event{SessionID: "thread-page", Type: "assistant.delta"})
	}
	store.AppendLocal(Event{SessionID: "thread-page", Type: "user.message"})
	store.AppendLocal(Event{SessionID: "thread-page", Type: "assistant.delta"})

	latest, hasMore := store.EventsBefore("thread-page", 0, 6)
	if len(latest) != 6 || !hasMore || latest[0].ID != 5 || latest[len(latest)-1].ID != 10 {
		t.Fatalf("unexpected latest history page: len=%d hasMore=%v events=%#v", len(latest), hasMore, latest)
	}
	older, hasMore := store.EventsBefore("thread-page", latest[0].ID, 6)
	if len(older) != 4 || hasMore || older[0].ID != 1 || older[len(older)-1].ID != 4 {
		t.Fatalf("unexpected older history page: len=%d hasMore=%v events=%#v", len(older), hasMore, older)
	}
	if hooked != 8 {
		t.Fatalf("local hydration events should not be broadcast: got %d hooks", hooked)
	}
}

func TestStoreTruncatesOversizedToolOutputForHistory(t *testing.T) {
	store := NewStore(t.TempDir())
	stored := store.Append(Event{
		SessionID: "thread-large-output",
		Type:      "tool.output",
		Payload:   map[string]interface{}{"text": strings.Repeat("x", maxStoredToolOutputBytes*2)},
	})
	text := stored.Payload["text"].(string)
	if len(text) > maxStoredToolOutputBytes || !strings.HasSuffix(text, toolOutputTruncatedNote) {
		t.Fatalf("oversized output was not compacted: bytes=%d", len(text))
	}
	if truncated, _ := stored.Payload["truncated"].(bool); !truncated {
		t.Fatalf("compacted output should be marked as truncated: %#v", stored.Payload)
	}

	history, hasMore := store.EventsBefore("thread-large-output", 0, 6)
	if hasMore || len(history) != 1 || len(history[0].Payload["text"].(string)) > maxStoredToolOutputBytes {
		t.Fatalf("history returned an oversized event: hasMore=%v history=%#v", hasMore, history)
	}
}

func TestStoreCompactsOversizedOutputFromExistingCache(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "remote-agent-cache.json")
	raw, err := json.Marshal(storeFile{Events: []Event{{
		ID:        7,
		SessionID: "thread-existing-cache",
		Type:      "tool.output",
		Payload:   map[string]interface{}{"text": strings.Repeat("y", maxStoredToolOutputBytes*2)},
	}}})
	if err != nil {
		t.Fatalf("marshal cache fixture: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write cache fixture: %v", err)
	}

	store := NewStore(dataDir)
	events := store.Events("thread-existing-cache", 0, 6)
	if len(events) != 1 || len(events[0].Payload["text"].(string)) > maxStoredToolOutputBytes {
		t.Fatalf("existing cache was not compacted: %#v", events)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read compacted cache: %v", err)
	}
	if len(persisted) >= len(raw) {
		t.Fatalf("compacted cache was not persisted: before=%d after=%d", len(raw), len(persisted))
	}
}

func TestStoreAppendLocalBatchPersistsWithoutBroadcasting(t *testing.T) {
	store := NewStore(t.TempDir())
	broadcasts := 0
	store.SetEventHook(func(Event) { broadcasts++ })

	events := store.AppendLocalBatch([]Event{
		{SessionID: "thread-batch", Type: "user.message", Payload: map[string]interface{}{"text": "第一条"}},
		{SessionID: "thread-batch", Type: "assistant.delta", Payload: map[string]interface{}{"text": "第二条"}},
	})
	if len(events) != 2 || events[0].ID != 1 || events[1].ID != 2 {
		t.Fatalf("unexpected batched events: %#v", events)
	}
	if broadcasts != 0 {
		t.Fatalf("local batch hydration should not be broadcast: %d", broadcasts)
	}
	persisted := store.Events("thread-batch", 0, 10)
	if len(persisted) != 2 || persisted[0].Payload["text"] != "第一条" || persisted[1].Payload["text"] != "第二条" {
		t.Fatalf("batch was not persisted: %#v", persisted)
	}
}

func TestStoreUpsertSessionLocalDoesNotBroadcast(t *testing.T) {
	store := NewStore(t.TempDir())
	broadcasts := 0
	store.SetSessionHook(func(Session) { broadcasts++ })
	store.UpsertSessionLocal(Session{ID: "thread-local", Title: "只在本地更新"})
	if broadcasts != 0 {
		t.Fatalf("local session update should not be broadcast: %d", broadcasts)
	}
	sessions := store.Sessions()
	if len(sessions) != 1 || sessions[0].ID != "thread-local" {
		t.Fatalf("local session update was not persisted: %#v", sessions)
	}
}

func TestStoreUpsertSessionsLocalPersistsBatchWithoutBroadcasting(t *testing.T) {
	store := NewStore(t.TempDir())
	broadcasts := 0
	store.SetSessionHook(func(Session) { broadcasts++ })
	store.UpsertSessionsLocal([]Session{
		{ID: "thread-one", Title: "第一条"},
		{ID: "thread-two", Title: "第二条"},
	})
	if broadcasts != 0 {
		t.Fatalf("local session batch should not be broadcast: %d", broadcasts)
	}
	sessions := store.Sessions()
	if len(sessions) != 2 || sessions[0].ID == "" || sessions[1].ID == "" {
		t.Fatalf("local session batch was not persisted: %#v", sessions)
	}
}

func TestParseTokenUsage(t *testing.T) {
	window := int64(114688)
	got := parseTokenUsage(map[string]interface{}{
		"last": map[string]interface{}{
			"cachedInputTokens":     float64(1200),
			"inputTokens":           float64(54000),
			"outputTokens":          float64(800),
			"reasoningOutputTokens": float64(300),
			"totalTokens":           float64(56000),
		},
		"total": map[string]interface{}{
			"inputTokens":  float64(54000),
			"outputTokens": float64(800),
			"totalTokens":  float64(54800),
		},
		"modelContextWindow": float64(window),
	})
	if got == nil {
		t.Fatal("expected token usage to be parsed")
	}
	if got.Last.InputTokens != 54000 || got.Last.CachedInputTokens != 1200 || got.ModelContextWindow == nil || *got.ModelContextWindow != window {
		t.Fatalf("unexpected token usage: %#v", got)
	}
}

func TestWithModelOption(t *testing.T) {
	params := withModelOption(map[string]interface{}{}, AppSettings{Model: " gpt-5-codex "})
	if params["model"] != "gpt-5-codex" {
		t.Fatalf("expected trimmed model parameter, got %#v", params["model"])
	}

	params = withModelOption(map[string]interface{}{}, AppSettings{Model: "  "})
	if _, ok := params["model"]; ok {
		t.Fatalf("empty model should not be sent: %#v", params)
	}
}

func TestWithTurnOptionsIncludesValidatedReasoningEffort(t *testing.T) {
	params := withTurnOptions(map[string]interface{}{}, AppSettings{Model: " gpt-5-codex ", ReasoningEffort: " high "})
	if params["model"] != "gpt-5-codex" || params["effort"] != "high" {
		t.Fatalf("expected model and effort options, got %#v", params)
	}

	params = withTurnOptions(map[string]interface{}{}, AppSettings{ReasoningEffort: "unsupported"})
	if _, ok := params["effort"]; ok {
		t.Fatalf("unsupported effort should not be sent: %#v", params)
	}
}

func TestNormalizeSettingsReasoningEffort(t *testing.T) {
	settings := normalizeSettings(AppSettings{ApprovalMode: "on-request", WorkMode: "edit", ReasoningEffort: "xhigh"})
	if settings.ReasoningEffort != "xhigh" {
		t.Fatalf("expected xhigh effort, got %#v", settings)
	}

	settings = normalizeSettings(AppSettings{ApprovalMode: "on-request", WorkMode: "edit", ReasoningEffort: "invalid"})
	if settings.ReasoningEffort != "" {
		t.Fatalf("invalid effort should fall back to default, got %#v", settings)
	}
}

func TestStorePersistsReasoningEffort(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	store.UpdateSettings(AppSettings{ApprovalMode: "on-request", WorkMode: "edit", Model: "gpt-5-codex", ReasoningEffort: "high"})

	reloaded := NewStore(dir).Settings()
	if reloaded.Model != "gpt-5-codex" || reloaded.ReasoningEffort != "high" {
		t.Fatalf("reasoning effort was not persisted: %#v", reloaded)
	}
}

func TestMessageInputKeepsNativeTextImageAndFileTypes(t *testing.T) {
	input := messageInput("  请分析附件  ", []Attachment{
		{Name: "截图.png", MimeType: "image/png", Path: `C:\tmp\截图.png`},
		{Name: "说明.txt", MimeType: "text/plain", Path: `C:\tmp\说明.txt`},
	})
	if len(input) != 3 {
		t.Fatalf("expected three native input items, got %#v", input)
	}
	if input[0]["type"] != "text" || input[0]["text"] != "请分析附件" {
		t.Fatalf("unexpected text input: %#v", input[0])
	}
	if input[1]["type"] != "localImage" || input[1]["path"] != `C:\tmp\截图.png` {
		t.Fatalf("image was not represented as localImage: %#v", input[1])
	}
	if input[2]["type"] != "mention" || input[2]["name"] != "说明.txt" || input[2]["path"] != `C:\tmp\说明.txt` {
		t.Fatalf("file was not represented as mention: %#v", input[2])
	}
}

func TestTurnCompletedOnlyClearsMatchingActiveTurn(t *testing.T) {
	store := NewStore(t.TempDir())
	bridge := &Bridge{
		store:         store,
		pending:       map[int64]pendingCall{},
		session:       &Session{ID: "thread-a", Mode: "host-new-session", Status: "running"},
		codexThreadID: "thread-a",
		activeTurnID:  "turn-a",
	}
	bridge.mapCodexEvent("turn/completed", map[string]interface{}{"threadId": "thread-b"}, 0, false)
	bridge.mu.Lock()
	got := bridge.activeTurnID
	bridge.mu.Unlock()
	if got != "turn-a" {
		t.Fatalf("an unrelated completion cleared the active turn: %q", got)
	}

	bridge.mapCodexEvent("turn/completed", map[string]interface{}{"threadId": "thread-a"}, 0, false)
	bridge.mu.Lock()
	got = bridge.activeTurnID
	bridge.mu.Unlock()
	if got != "" {
		t.Fatalf("matching completion should clear the active turn, got %q", got)
	}
}

func TestStoreKeepsSessionModelAndTokenUsageWhenThreadListOmitsMetadata(t *testing.T) {
	store := NewStore(t.TempDir())
	window := int64(114688)
	usage := &TokenUsage{ModelContextWindow: &window}
	store.UpsertSession(Session{ID: "thread-1", Model: "gpt-5-codex", TokenUsage: usage, UpdatedAt: "2026-08-23T12:00:00Z"})
	store.UpsertSession(Session{ID: "thread-1", Title: "最新标题", UpdatedAt: "2026-08-23T12:01:00Z"})

	sessions := store.Sessions()
	if len(sessions) != 1 {
		t.Fatalf("expected one session, got %d", len(sessions))
	}
	if sessions[0].Model != "gpt-5-codex" || !reflect.DeepEqual(sessions[0].TokenUsage, usage) {
		t.Fatalf("session metadata was lost: %#v", sessions[0])
	}
}

func TestRemoteAgentCreateSessionUsesSelectedProjectCwd(t *testing.T) {
	store := NewStore(t.TempDir())
	const projectCwd = `D:\git\project-a`
	var threadStartCwd string
	bridge := &Bridge{
		cmd:         &exec.Cmd{},
		initialized: true,
		cwd:         `D:\git\default-workspace`,
		store:       store,
		requestHook: func(method string, params interface{}) (interface{}, error) {
			if method != "thread/start" {
				t.Fatalf("unexpected app-server request: %s", method)
			}
			threadStartCwd = stringValue(params.(map[string]interface{})["cwd"])
			return map[string]interface{}{
				"thread": map[string]interface{}{"id": "thread-project"},
				"model":  "gpt-5-codex",
			}, nil
		},
	}
	agent := &remoteAgent{store: store, bridge: bridge}
	payload, _ := json.Marshal(map[string]string{"cwd": projectCwd})
	result, err := agent.executeCommand("sessions.create", payload)
	if err != nil {
		t.Fatalf("create project session: %v", err)
	}
	session := result.(map[string]interface{})["session"].(Session)
	if threadStartCwd != projectCwd || session.Cwd != projectCwd {
		t.Fatalf("project cwd was not propagated: start=%q session=%q", threadStartCwd, session.Cwd)
	}
}

func TestBridgeCreateSessionWithoutProjectStaysRecentAfterThreadSync(t *testing.T) {
	store := NewStore(t.TempDir())
	const defaultCwd = `D:\git\default-workspace`
	var threadStartCwd string
	bridge := &Bridge{
		cmd:         &exec.Cmd{},
		initialized: true,
		cwd:         defaultCwd,
		store:       store,
		requestHook: func(method string, params interface{}) (interface{}, error) {
			if method != "thread/start" {
				t.Fatalf("unexpected app-server request: %s", method)
			}
			threadStartCwd = stringValue(params.(map[string]interface{})["cwd"])
			return map[string]interface{}{"thread": map[string]interface{}{"id": "thread-recent"}}, nil
		},
	}
	session, err := bridge.CreateSession("", "")
	if err != nil {
		t.Fatalf("create recent session: %v", err)
	}
	if threadStartCwd != defaultCwd || session.Cwd != "new-chat" {
		t.Fatalf("unexpected recent session cwd: start=%q session=%q", threadStartCwd, session.Cwd)
	}
	synced := store.EnrichSession(Session{ID: session.ID, Cwd: defaultCwd})
	if synced.Cwd != "new-chat" {
		t.Fatalf("thread sync should preserve recent classification, got %q", synced.Cwd)
	}
}

package main

import (
	"bytes"
	"encoding/json"
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

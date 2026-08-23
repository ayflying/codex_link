package main

import (
	"os/exec"
	"reflect"
	"testing"
)

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

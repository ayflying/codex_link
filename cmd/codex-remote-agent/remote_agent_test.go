package main

import (
	"os/exec"
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

package main

import "testing"

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

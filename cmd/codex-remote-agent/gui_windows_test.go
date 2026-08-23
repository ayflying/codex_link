//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentExitDetailIncludesDiagnosticLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "agent-gui.log")
	if err := os.WriteFile(logPath, []byte("服务端连接断开\nCodex app-server 不可用"), 0o600); err != nil {
		t.Fatalf("write diagnostic log: %v", err)
	}

	detail := agentExitDetail(errors.New("exit status 1"), logPath)
	if !strings.Contains(detail, "后台客户端异常退出") || !strings.Contains(detail, "Codex app-server 不可用") {
		t.Fatalf("diagnostic log was not included: %q", detail)
	}
}

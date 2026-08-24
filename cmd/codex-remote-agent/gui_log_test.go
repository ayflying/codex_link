package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadAgentGUILogTailKeepsRecentUTF8Lines(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "agent-gui.log")
	content := "较早日志\r\n连接服务端\r\n后台客户端已连接\r\n"
	if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	tail, err := readAgentGUILogTail(logPath, len("后台客户端已连接\r\n")+2)
	if err != nil {
		t.Fatalf("read log tail: %v", err)
	}
	if !strings.Contains(tail, "后台客户端已连接") || !strings.HasPrefix(tail, "... 已省略更早日志 ...") {
		t.Fatalf("expected recent log tail, got %q", tail)
	}
}

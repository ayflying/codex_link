package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
)

func TestP2PUploadWritesOnlyToAgentDataDir(t *testing.T) {
	agent := &remoteAgent{store: &Store{dataDir: t.TempDir()}}
	peer := &p2pPeer{uploads: map[string]*p2pUpload{}}
	uploadID := "browser-upload-1234"
	payload := []byte("p2p-image-payload")

	start, _ := json.Marshal(map[string]interface{}{
		"uploadId": uploadID,
		"name":     "screenshot.png",
		"mimeType": "image/png",
		"size":     len(payload),
	})
	if _, err := agent.handleP2PUpload(peer, "uploads.start", start); err != nil {
		t.Fatalf("start upload: %v", err)
	}
	chunk, _ := json.Marshal(map[string]string{
		"uploadId": uploadID,
		"data":     base64.RawStdEncoding.EncodeToString(payload),
	})
	if _, err := agent.handleP2PUpload(peer, "uploads.chunk", chunk); err != nil {
		t.Fatalf("write upload chunk: %v", err)
	}
	finish, _ := json.Marshal(map[string]string{"uploadId": uploadID})
	result, err := agent.handleP2PUpload(peer, "uploads.finish", finish)
	if err != nil {
		t.Fatalf("finish upload: %v", err)
	}
	attachment, ok := result.(Attachment)
	if !ok {
		t.Fatalf("unexpected result type %T", result)
	}
	if attachment.Path == "" || attachment.ID != uploadID {
		t.Fatalf("unexpected attachment: %#v", attachment)
	}
	written, err := os.ReadFile(attachment.Path)
	if err != nil {
		t.Fatalf("read local attachment: %v", err)
	}
	if string(written) != string(payload) {
		t.Fatalf("unexpected payload %q", written)
	}
}

func TestP2PSignalIdentifiers(t *testing.T) {
	if !isSafeP2PID("browser-upload-1234") {
		t.Fatal("expected safe identifier")
	}
	if isSafeP2PID("../../uploads") {
		t.Fatal("path traversal identifier must be rejected")
	}
	servers := splitP2PList(" stun:one.example:3478, ,stun:two.example:3478 ")
	if len(servers) != 2 || servers[0] != "stun:one.example:3478" || servers[1] != "stun:two.example:3478" {
		t.Fatalf("unexpected STUN servers: %#v", servers)
	}
}

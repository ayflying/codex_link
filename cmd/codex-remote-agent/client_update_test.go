package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseClientSemanticVersion(t *testing.T) {
	version, err := parseClientSemanticVersion("v12.3.4")
	if err != nil {
		t.Fatalf("parse version: %v", err)
	}
	if version.String() != "12.3.4" {
		t.Fatalf("unexpected version: %s", version.String())
	}
	newer, _ := parseClientSemanticVersion("12.4.0")
	if newer.Compare(version) <= 0 || version.Compare(newer) >= 0 {
		t.Fatalf("version comparison is incorrect: newer=%s current=%s", newer, version)
	}
	for _, invalid := range []string{"", "1.2", "1.2.3.4", "1.2.beta", "1.-2.3", "v1.2.3-beta"} {
		if _, err := parseClientSemanticVersion(invalid); err == nil {
			t.Fatalf("invalid version was accepted: %q", invalid)
		}
	}
}

func TestStageClientUpdateDownloadsVerifiedAsset(t *testing.T) {
	executable := []byte("verified Windows executable")
	payload := zipClientExecutable(t, executable)
	digest := sha256.Sum256(payload)
	server := newClientUpdateServer(t, payload, hex.EncodeToString(digest[:]), "v1.2.4")
	configureClientUpdateServer(t, server)

	target := filepath.Join(t.TempDir(), "codex-remote-agent.exe")
	current, _ := parseClientSemanticVersion("1.2.3")
	pending, available, err := stageClientUpdate(target, current)
	if err != nil {
		t.Fatalf("stage update: %v", err)
	}
	if !available {
		t.Fatal("expected a newer release")
	}
	if want := filepath.Join(filepath.Dir(target), "codex-remote-agent.next.exe"); pending != want {
		t.Fatalf("pending path = %q, want %q", pending, want)
	}
	content, err := os.ReadFile(pending)
	if err != nil {
		t.Fatalf("read pending update: %v", err)
	}
	if string(content) != string(executable) {
		t.Fatalf("pending content = %q, want %q", content, executable)
	}
}

func TestStageClientUpdateRejectsChecksumMismatch(t *testing.T) {
	payload := zipClientExecutable(t, []byte("tampered executable"))
	server := newClientUpdateServer(t, payload, strings.Repeat("0", sha256.Size*2), "v1.2.4")
	configureClientUpdateServer(t, server)

	executable := filepath.Join(t.TempDir(), "codex-remote-agent.exe")
	current, _ := parseClientSemanticVersion("1.2.3")
	if _, available, err := stageClientUpdate(executable, current); err == nil || available {
		t.Fatalf("checksum mismatch must fail without staging: available=%v err=%v", available, err)
	}
	if _, err := os.Stat(clientUpdateArtifactPath(executable, ".next")); !os.IsNotExist(err) {
		t.Fatalf("checksum mismatch left a pending update: %v", err)
	}
}

func TestStageClientUpdateSkipsCurrentRelease(t *testing.T) {
	server := newClientUpdateServer(t, []byte("unused"), strings.Repeat("0", sha256.Size*2), "v1.2.3")
	configureClientUpdateServer(t, server)

	current, _ := parseClientSemanticVersion("1.2.3")
	if pending, available, err := stageClientUpdate(filepath.Join(t.TempDir(), "client.exe"), current); err != nil || available || pending != "" {
		t.Fatalf("current release must be skipped: pending=%q available=%v err=%v", pending, available, err)
	}
}

func TestStageClientUpdateReportsReleaseRequestFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary failure", http.StatusServiceUnavailable)
	}))
	configureClientUpdateServer(t, server)

	current, _ := parseClientSemanticVersion("1.2.3")
	if _, available, err := stageClientUpdate(filepath.Join(t.TempDir(), "client.exe"), current); err == nil || available {
		t.Fatalf("release request failure must not stage an update: available=%v err=%v", available, err)
	}
}

func TestClientReleaseAssetsRequiresBothExpectedFiles(t *testing.T) {
	_, _, err := clientReleaseAssets([]githubClientReleaseAsset{{
		Name: clientReleaseArchiveName, BrowserDownloadURL: "https://example.invalid/client.zip", Size: 1,
	}})
	if err == nil {
		t.Fatal("release without checksum was accepted")
	}
}

func TestExtractClientExecutableRejectsNestedEntry(t *testing.T) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create("nested/" + clientReleaseExecutableName)
	if err != nil {
		t.Fatalf("create nested zip entry: %v", err)
	}
	if _, err := entry.Write([]byte("not accepted")); err != nil {
		t.Fatalf("write nested zip entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "client.zip")
	if err := os.WriteFile(archivePath, buffer.Bytes(), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	if err := extractClientExecutable(archivePath, filepath.Join(t.TempDir(), "client.next.exe")); err == nil {
		t.Fatal("nested executable entry was accepted")
	}
}

func TestParseClientChecksumRequiresExpectedFile(t *testing.T) {
	digest := strings.Repeat("a", sha256.Size*2)
	if _, err := parseClientChecksum([]byte(digest+"  other.zip\n"), clientReleaseArchiveName); err == nil {
		t.Fatal("checksum for another file was accepted")
	}
	parsed, err := parseClientChecksum([]byte(digest+" *"+clientReleaseArchiveName+"\n"), clientReleaseArchiveName)
	if err != nil || parsed != digest {
		t.Fatalf("valid checksum was rejected: checksum=%q err=%v", parsed, err)
	}
}

func newClientUpdateServer(t *testing.T, payload []byte, checksum, tag string) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/latest":
			release := githubClientRelease{
				TagName: tag,
				Assets: []githubClientReleaseAsset{
					{Name: clientReleaseArchiveName, BrowserDownloadURL: server.URL + "/client.zip", Size: int64(len(payload))},
					{Name: clientReleaseChecksumName, BrowserDownloadURL: server.URL + "/client.zip.sha256", Size: int64(len(checksum) + len(clientReleaseArchiveName) + 3)},
				},
			}
			_ = json.NewEncoder(w).Encode(release)
		case "/client.zip":
			_, _ = w.Write(payload)
		case "/client.zip.sha256":
			_, _ = w.Write([]byte(checksum + "  " + clientReleaseArchiveName + "\n"))
		default:
			http.NotFound(w, request)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func zipClientExecutable(t *testing.T, executable []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create(clientReleaseExecutableName)
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := entry.Write(executable); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buffer.Bytes()
}

func configureClientUpdateServer(t *testing.T, server *httptest.Server) {
	t.Helper()
	previousURL := clientReleaseLatestURL
	previousClient := clientUpdateHTTPClient
	clientReleaseLatestURL = server.URL + "/latest"
	clientUpdateHTTPClient = server.Client()
	t.Cleanup(func() {
		clientReleaseLatestURL = previousURL
		clientUpdateHTTPClient = previousClient
	})
}

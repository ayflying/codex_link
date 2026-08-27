package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultClientReleaseLatestURL = "https://api.github.com/repos/ayflying/codex_link/releases/latest"
	clientReleaseArchiveName      = "codex-remote-agent-windows-amd64.zip"
	clientReleaseExecutableName   = "codex-remote-agent.exe"
	clientUpdateMaxBytes          = 200 * 1024 * 1024
	clientReleaseMaxBytes         = 512 * 1024
	clientUpdateHelperFlag        = "--apply-client-update"
)

var (
	// These values are set by the release build. Development builds deliberately
	// keep the invalid default version so they never self-update.
	clientVersion  = "0.0.0-dev"
	clientRevision = "dev"

	clientReleaseLatestURL = defaultClientReleaseLatestURL
	clientUpdateHTTPClient = &http.Client{Timeout: 8 * time.Second}
)

type clientSemanticVersion struct {
	major int
	minor int
	patch int
}

func (v clientSemanticVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

func (v clientSemanticVersion) Compare(other clientSemanticVersion) int {
	if v.major != other.major {
		return compareClientVersionPart(v.major, other.major)
	}
	if v.minor != other.minor {
		return compareClientVersionPart(v.minor, other.minor)
	}
	return compareClientVersionPart(v.patch, other.patch)
}

func compareClientVersionPart(left, right int) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func parseClientSemanticVersion(value string) (clientSemanticVersion, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "v")
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return clientSemanticVersion{}, fmt.Errorf("版本号必须是三段式: %q", value)
	}
	parsed := [3]int{}
	for index, part := range parts {
		if part == "" || strings.HasPrefix(part, "+") || strings.HasPrefix(part, "-") {
			return clientSemanticVersion{}, fmt.Errorf("版本号包含无效字段: %q", value)
		}
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return clientSemanticVersion{}, fmt.Errorf("版本号包含无效字段: %q", value)
		}
		parsed[index] = number
	}
	return clientSemanticVersion{major: parsed[0], minor: parsed[1], patch: parsed[2]}, nil
}

type githubClientRelease struct {
	TagName    string                     `json:"tag_name"`
	Draft      bool                       `json:"draft"`
	Prerelease bool                       `json:"prerelease"`
	Assets     []githubClientReleaseAsset `json:"assets"`
}

type githubClientReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

func isClientUpdateHelperMode() bool {
	return len(os.Args) > 1 && os.Args[1] == clientUpdateHelperFlag
}

// maybeUpdateClient stages a verified update and starts a short-lived helper.
// A failed check must never block the normal client startup path.
func maybeUpdateClient() (bool, error) {
	if runtime.GOOS != "windows" {
		return false, nil
	}
	currentVersion, err := parseClientSemanticVersion(clientVersion)
	if err != nil {
		return false, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("获取客户端路径失败: %w", err)
	}
	pending, available, err := stageClientUpdate(executable, currentVersion)
	if err != nil || !available {
		return false, err
	}
	if err := startClientUpdateHelper(executable, pending, os.Args[1:]); err != nil {
		return false, err
	}
	return true, nil
}

func stageClientUpdate(executable string, currentVersion clientSemanticVersion) (string, bool, error) {
	release, err := fetchLatestClientRelease()
	if err != nil {
		return "", false, err
	}
	if release.Draft || release.Prerelease {
		return "", false, errors.New("最新客户端发布不是正式版本")
	}
	latestVersion, err := parseClientSemanticVersion(release.TagName)
	if err != nil {
		return "", false, fmt.Errorf("最新客户端发布标签无效: %w", err)
	}
	if latestVersion.Compare(currentVersion) <= 0 {
		return "", false, nil
	}
	executableAsset, err := clientReleaseAsset(release.Assets)
	if err != nil {
		return "", false, err
	}
	pending := clientUpdateArtifactPath(executable, ".next")
	if err := downloadClientAsset(executableAsset, pending); err != nil {
		return "", false, err
	}
	return pending, true, nil
}

func fetchLatestClientRelease() (githubClientRelease, error) {
	request, err := http.NewRequest(http.MethodGet, clientReleaseLatestURL, nil)
	if err != nil {
		return githubClientRelease{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", clientUpdateUserAgent())
	response, err := clientUpdateHTTPClient.Do(request)
	if err != nil {
		return githubClientRelease{}, fmt.Errorf("获取客户端发布信息失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return githubClientRelease{}, fmt.Errorf("获取客户端发布信息失败: %s", response.Status)
	}
	var release githubClientRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, clientReleaseMaxBytes)).Decode(&release); err != nil {
		return githubClientRelease{}, fmt.Errorf("解析客户端发布信息失败: %w", err)
	}
	return release, nil
}

func clientReleaseAsset(assets []githubClientReleaseAsset) (githubClientReleaseAsset, error) {
	var executable githubClientReleaseAsset
	for _, asset := range assets {
		if asset.Name == clientReleaseArchiveName {
			executable = asset
		}
	}
	if executable.BrowserDownloadURL == "" || executable.Size <= 0 || executable.Size > clientUpdateMaxBytes {
		return githubClientReleaseAsset{}, errors.New("最新客户端发布缺少有效的 Windows x64 压缩包")
	}
	return executable, nil
}

func downloadClientAsset(asset githubClientReleaseAsset, pendingPath string) error {
	response, err := fetchClientAsset(asset)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.ContentLength > clientUpdateMaxBytes {
		return errors.New("客户端更新文件过大")
	}
	directory := filepath.Dir(pendingPath)
	temporary, err := os.CreateTemp(directory, ".codex-link-client-update-*.zip")
	if err != nil {
		return fmt.Errorf("创建客户端更新临时文件失败: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	written, err := io.Copy(temporary, io.LimitReader(response.Body, clientUpdateMaxBytes+1))
	if err != nil {
		return fmt.Errorf("下载客户端更新失败: %w", err)
	}
	if written <= 0 || written > clientUpdateMaxBytes {
		return errors.New("客户端更新文件大小无效")
	}
	if asset.Size > 0 && written != asset.Size {
		return errors.New("客户端更新文件大小与发布信息不一致")
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("保存客户端更新失败: %w", err)
	}
	return extractClientExecutable(temporaryPath, pendingPath)
}

func extractClientExecutable(archivePath, pendingPath string) error {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("打开客户端更新压缩包失败: %w", err)
	}
	defer archive.Close()
	var entry *zip.File
	for _, candidate := range archive.File {
		if candidate.Name == clientReleaseExecutableName {
			entry = candidate
			break
		}
	}
	if entry == nil || entry.FileInfo().IsDir() || entry.UncompressedSize64 == 0 || entry.UncompressedSize64 > clientUpdateMaxBytes {
		return errors.New("客户端更新压缩包缺少有效的可执行文件")
	}
	reader, err := entry.Open()
	if err != nil {
		return fmt.Errorf("读取客户端更新压缩包失败: %w", err)
	}
	directory := filepath.Dir(pendingPath)
	temporary, err := os.CreateTemp(directory, ".codex-link-client-executable-*")
	if err != nil {
		_ = reader.Close()
		return fmt.Errorf("创建客户端更新文件失败: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = reader.Close()
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	written, err := io.Copy(temporary, io.LimitReader(reader, clientUpdateMaxBytes+1))
	if err != nil {
		cleanup()
		return fmt.Errorf("提取客户端更新失败: %w", err)
	}
	if err := reader.Close(); err != nil {
		cleanup()
		return fmt.Errorf("读取客户端更新失败: %w", err)
	}
	if written != int64(entry.UncompressedSize64) || written > clientUpdateMaxBytes {
		cleanup()
		return errors.New("客户端更新可执行文件大小无效")
	}
	if err := temporary.Close(); err != nil {
		cleanup()
		return fmt.Errorf("保存客户端更新失败: %w", err)
	}
	if err := os.Remove(pendingPath); err != nil && !os.IsNotExist(err) {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("替换旧客户端更新文件失败: %w", err)
	}
	if err := os.Rename(temporaryPath, pendingPath); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("保存客户端更新文件失败: %w", err)
	}
	return nil
}

func fetchClientAsset(asset githubClientReleaseAsset) (*http.Response, error) {
	request, err := http.NewRequest(http.MethodGet, asset.BrowserDownloadURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", clientUpdateUserAgent())
	response, err := clientUpdateHTTPClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("下载客户端更新文件失败: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("下载客户端更新文件失败: %s", response.Status)
	}
	return response, nil
}

func clientUpdateArtifactPath(executable, suffix string) string {
	extension := filepath.Ext(executable)
	base := strings.TrimSuffix(executable, extension)
	return base + suffix + extension
}

func clientUpdateUserAgent() string {
	return "Codex-Link-Remote-Agent/" + clientVersion + " (" + clientRevision + ")"
}

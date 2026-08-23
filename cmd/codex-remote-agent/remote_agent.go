package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type remoteAgentConfig struct {
	ServerURL  string `json:"serverUrl"`
	Token      string `json:"token"`
	DeviceID   string `json:"deviceId"`
	DeviceName string `json:"deviceName"`
}

type remoteEnvelope struct {
	Type      string          `json:"type"`
	RequestID string          `json:"requestId,omitempty"`
	Action    string          `json:"action,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Error     string          `json:"error,omitempty"`
}

type remoteAgent struct {
	store    *Store
	bridge   *Bridge
	config   remoteAgentConfig
	write    sync.Mutex
	conn     *websocket.Conn
	p2pMu    sync.Mutex
	p2pPeers map[string]*p2pPeer
	p2pOnly  atomic.Bool
}

func isRemoteAgentMode() bool {
	return len(os.Args) > 1 && strings.EqualFold(os.Args[1], "agent")
}

func loginRemoteAgent(dataDir string) {
	serverURL := firstNonEmptyString(argValue("--server"), os.Getenv("REMOTE_SERVER"))
	token := firstNonEmptyString(argValue("--token"), os.Getenv("REMOTE_TOKEN"))
	deviceName := firstNonEmptyString(argValue("--device"), os.Getenv("REMOTE_DEVICE_NAME"), localDeviceName())
	if serverURL == "" {
		log.Fatal("用法: codex-remote-agent login --server https://server.example --token crs_xxx [--device 设备名称]")
	}
	if token == "" {
		fmt.Fprint(os.Stderr, "服务端 Token: ")
		input, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && strings.TrimSpace(input) == "" {
			log.Fatal("未读取到密码")
		}
		token = strings.TrimSpace(input)
		if token == "" {
			log.Fatal("Token 不能为空")
		}
	}
	serverURL = strings.TrimRight(serverURL, "/")
	deviceID := loginDeviceID(dataDir, serverURL)
	if deviceID == "" {
		deviceID = randomID()
	}
	requestBody, _ := json.Marshal(map[string]string{
		"token":      token,
		"deviceId":   deviceID,
		"deviceName": deviceName,
	})
	response, err := http.Post(serverURL+"/api/agent/login", "application/json", bytes.NewReader(requestBody))
	if err != nil {
		log.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var result map[string]interface{}
		_ = json.NewDecoder(response.Body).Decode(&result)
		log.Fatalf("登录失败: %v", firstNonEmptyString(stringValue(result["error"]), response.Status))
	}
	var result struct {
		DeviceID   string `json:"deviceId"`
		DeviceName string `json:"deviceName"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		log.Fatal(err)
	}
	if result.DeviceID == "" {
		log.Fatal("服务端未返回设备 ID")
	}
	config := remoteAgentConfig{ServerURL: serverURL, Token: token, DeviceID: result.DeviceID, DeviceName: firstNonEmptyString(result.DeviceName, deviceName)}
	if err := saveRemoteAgentConfig(dataDir, config); err != nil {
		log.Fatal(err)
	}
	log.Printf("客户端已登录: %s", config.DeviceName)
	log.Printf("登录配置已保存；此命令会退出，请另行运行: codex-remote-agent agent")
}

func loginDeviceID(dataDir, serverURL string) string {
	config, err := loadRemoteAgentConfig(dataDir)
	if err != nil {
		return ""
	}
	if !strings.EqualFold(strings.TrimRight(config.ServerURL, "/"), strings.TrimRight(serverURL, "/")) {
		return ""
	}
	return config.DeviceID
}

func runRemoteAgent(root, cwd, dataDir string) {
	config, err := loadRemoteAgentConfig(dataDir)
	if err != nil {
		log.Fatal("客户端尚未使用 Token 登录。请先运行: codex-remote-agent login --server <服务端地址> --token <Token>")
	}
	if config.DeviceName == "" {
		config.DeviceName = localDeviceName()
	}
	store := NewStore(dataDir)
	agent := &remoteAgent{store: store, bridge: NewBridge(store, cwd), config: config, p2pPeers: map[string]*p2pPeer{}}
	agent.initP2P()
	store.SetEventHook(func(event Event) { agent.send("event", "", "", event) })
	store.SetSessionHook(func(session Session) { agent.send("session", "", "", session) })
	log.Printf("Codex Remote 客户端: %s", config.DeviceName)
	log.Printf("服务端: %s", config.ServerURL)
	if err := agent.validate(); err != nil {
		if errors.Is(err, errAgentAuth) {
			log.Fatal("客户端 Token 无效或已删除，请重新执行 login")
		}
		log.Printf("服务端暂时不可用: %v；5 秒后重试", err)
	}
	for {
		if err := agent.connectAndServe(); err != nil {
			if errors.Is(err, errAgentAuth) {
				log.Fatal("客户端 Token 无效或已删除，请重新执行 login")
			}
			log.Printf("服务端连接断开: %v；5 秒后重试", err)
		}
		time.Sleep(5 * time.Second)
	}
}

var errAgentAuth = errors.New("agent authentication failed")

func (a *remoteAgent) validate() error {
	endpoint, err := remoteHTTPURL(a.config)
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodGet, endpoint+"/api/agent/validate?deviceId="+url.QueryEscape(a.config.DeviceID), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+a.config.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return errAgentAuth
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("服务端校验失败: %s", response.Status)
	}
	return nil
}

func (a *remoteAgent) connectAndServe() error {
	endpoint, err := remoteWebSocketURL(a.config)
	if err != nil {
		return err
	}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+a.config.Token)
	conn, response, err := websocket.DefaultDialer.Dial(endpoint, headers)
	if err != nil {
		if response != nil {
			if response.StatusCode == http.StatusUnauthorized {
				return errAgentAuth
			}
			return fmt.Errorf("%s: %s", response.Status, endpoint)
		}
		return err
	}
	a.write.Lock()
	a.conn = conn
	a.write.Unlock()
	defer func() {
		a.write.Lock()
		if a.conn == conn {
			a.conn = nil
		}
		a.write.Unlock()
		_ = conn.Close()
	}()
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return err
	}
	var connected remoteEnvelope
	if json.Unmarshal(raw, &connected) == nil && connected.Type == "connected" {
		a.updateServerMode(connected)
	}

	a.send("hello", "", "", map[string]interface{}{
		"deviceId":   a.config.DeviceID,
		"deviceName": a.config.DeviceName,
		"platform":   runtime.GOOS,
		"version":    "1.0.0",
	})
	if err := a.syncThreads(); err != nil {
		a.send("error", "", "", map[string]string{"message": "同步本机 Codex 对话失败: " + err.Error()})
	}
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var message remoteEnvelope
		if err := json.Unmarshal(raw, &message); err != nil {
			continue
		}
		if message.Type == "command" {
			go a.handleCommand(message)
		} else if message.Type == "connected" {
			a.updateServerMode(message)
		} else if message.Type == "p2p.signal" || message.Type == "p2p.close" {
			a.handleP2PMessage(message)
		}
	}
}

func (a *remoteAgent) updateServerMode(message remoteEnvelope) {
	var payload struct {
		P2POnly bool `json:"p2pOnly"`
	}
	if json.Unmarshal(message.Payload, &payload) == nil {
		a.p2pOnly.Store(payload.P2POnly)
	}
}

func (a *remoteAgent) syncThreads() error {
	sessions, err := a.bridge.ListThreads()
	if err != nil {
		return err
	}
	for _, session := range sessions {
		a.store.UpsertSession(session)
	}
	a.send("sync.sessions", "", "", map[string]interface{}{"sessions": sessions})
	return nil
}

func (a *remoteAgent) handleCommand(message remoteEnvelope) {
	result, err := a.executeCommand(message.Action, message.Payload)
	if err != nil {
		a.send("response", message.RequestID, message.Action, nil, err.Error())
		return
	}
	a.send("response", message.RequestID, message.Action, result)
}

func (a *remoteAgent) executeCommand(action string, payload json.RawMessage) (interface{}, error) {
	switch action {
	case "settings.get":
		return a.store.Settings(), nil
	case "threads.list":
		sessions, err := a.bridge.ListThreads()
		if err != nil {
			return nil, err
		}
		for _, session := range sessions {
			a.store.UpsertSession(session)
		}
		return map[string]interface{}{"sessions": sessions}, nil
	case "sessions.create":
		var body struct {
			Prompt string `json:"prompt"`
		}
		_ = json.Unmarshal(payload, &body)
		session, err := a.bridge.CreateSession(strings.TrimSpace(body.Prompt))
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"session": session}, nil
	case "threads.resume":
		var body struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(payload, &body)
		if body.ID == "" {
			return nil, errors.New("缺少对话 ID")
		}
		session, err := a.bridge.ResumeThread(body.ID)
		if err != nil {
			return nil, err
		}
		a.store.UpsertSession(session)
		return map[string]interface{}{"session": session}, nil
	case "threads.archive":
		var body struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(payload, &body)
		if body.ID == "" {
			return nil, errors.New("缺少对话 ID")
		}
		if err := a.bridge.ArchiveThread(body.ID); err != nil {
			return nil, err
		}
		a.store.RemoveSession(body.ID)
		return map[string]bool{"ok": true}, nil
	case "sessions.message":
		var body struct {
			ID          string       `json:"id"`
			Text        string       `json:"text"`
			Attachments []Attachment `json:"attachments"`
		}
		_ = json.Unmarshal(payload, &body)
		attachments, err := a.materializeAttachments(body.Attachments)
		if err != nil {
			return nil, err
		}
		if err := a.bridge.SendMessage(strings.TrimSpace(body.Text), body.ID, attachments); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	case "sessions.approval":
		var body struct {
			ID         string `json:"id"`
			ApprovalID string `json:"approvalId"`
			Decision   string `json:"decision"`
		}
		_ = json.Unmarshal(payload, &body)
		if err := a.bridge.ResolveApproval(body.ApprovalID, body.Decision); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	case "sessions.cancel":
		if err := a.bridge.Cancel(); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	case "settings.update":
		var settings AppSettings
		_ = json.Unmarshal(payload, &settings)
		return a.store.UpdateSettings(settings), nil
	case "health":
		return map[string]interface{}{"ok": true, "codex": a.bridge.Health()}, nil
	default:
		return nil, fmt.Errorf("未知客户端命令: %s", action)
	}
}

func (a *remoteAgent) materializeAttachments(attachments []Attachment) ([]Attachment, error) {
	result := make([]Attachment, 0, len(attachments))
	for _, attachment := range attachments {
		if attachment.DataURL == "" {
			result = append(result, attachment)
			continue
		}
		local, err := saveUpload(a.store.dataDir, attachment.Name, attachment.MimeType, attachment.DataURL)
		if err != nil {
			return nil, err
		}
		result = append(result, local)
	}
	return result, nil
}

func (a *remoteAgent) send(kind, requestID, action string, payload interface{}, messageError ...string) {
	message := remoteEnvelope{Type: kind, RequestID: requestID, Action: action}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return
		}
		message.Payload = raw
	}
	if len(messageError) > 0 {
		message.Error = messageError[0]
	}
	if message.Type == "event" || message.Type == "session" {
		if a.sendP2P(message) {
			return
		}
		if a.p2pOnly.Load() {
			return
		}
	}
	a.write.Lock()
	defer a.write.Unlock()
	if a.conn == nil {
		return
	}
	_ = a.conn.WriteJSON(message)
}

func remoteWebSocketURL(config remoteAgentConfig) (string, error) {
	parsed, err := url.Parse(config.ServerURL)
	if err != nil {
		return "", err
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", errors.New("服务端地址必须使用 http 或 https")
	}
	parsed.Path = "/api/agent/ws"
	query := parsed.Query()
	query.Set("deviceId", config.DeviceID)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func remoteHTTPURL(config remoteAgentConfig) (string, error) {
	parsed, err := url.Parse(config.ServerURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("服务端地址必须使用 http 或 https")
	}
	parsed.Path = ""
	parsed.RawQuery = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func loadRemoteAgentConfig(dataDir string) (remoteAgentConfig, error) {
	var config remoteAgentConfig
	raw, err := os.ReadFile(filepath.Join(dataDir, "remote-agent.json"))
	if err != nil {
		return config, err
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return config, err
	}
	if config.ServerURL == "" || config.Token == "" || config.DeviceID == "" {
		return config, errors.New("客户端登录信息不完整")
	}
	return config, nil
}

func saveRemoteAgentConfig(dataDir string, config remoteAgentConfig) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, "remote-agent.json"), raw, 0o600)
}

func argValue(name string) string {
	for index, value := range os.Args {
		if value == name && index+1 < len(os.Args) {
			return strings.TrimSpace(os.Args[index+1])
		}
	}
	return ""
}

func localDeviceName() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "Codex 客户端"
	}
	return name
}

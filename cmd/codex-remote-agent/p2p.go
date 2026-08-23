package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pion/webrtc/v4"
)

const (
	p2pMaxUploadBytes = 10 * 1024 * 1024
	p2pChunkBytes     = 48 * 1024
)

type p2pSignal struct {
	ClientID         string   `json:"clientId"`
	Kind             string   `json:"kind"`
	SDP              string   `json:"sdp,omitempty"`
	Candidate        string   `json:"candidate,omitempty"`
	SDPMid           *string  `json:"sdpMid,omitempty"`
	SDPMLineIndex    *uint16  `json:"sdpMLineIndex,omitempty"`
	UsernameFragment *string  `json:"usernameFragment,omitempty"`
	ICEServers       []string `json:"iceServers,omitempty"`
}

type p2pUpload struct {
	ID       string
	Name     string
	MimeType string
	Path     string
	Size     int
	Received int
	File     *os.File
}

type p2pPeer struct {
	clientID             string
	pc                   *webrtc.PeerConnection
	dc                   *webrtc.DataChannel
	write                sync.Mutex
	mu                   sync.Mutex
	remoteDescriptionSet bool
	pendingCandidates    []webrtc.ICECandidateInit
	uploads              map[string]*p2pUpload
	closeOnce            sync.Once
}

func (a *remoteAgent) initP2P() {
	a.p2pMu.Lock()
	if a.p2pPeers == nil {
		a.p2pPeers = map[string]*p2pPeer{}
	}
	a.p2pMu.Unlock()
}

func (a *remoteAgent) handleP2PMessage(message remoteEnvelope) {
	var signal p2pSignal
	if err := json.Unmarshal(message.Payload, &signal); err != nil || signal.ClientID == "" {
		return
	}
	if message.Type == "p2p.close" {
		a.closeP2PPeer(signal.ClientID)
		return
	}
	switch signal.Kind {
	case "offer":
		a.acceptP2POffer(signal)
	case "candidate":
		a.addP2PRemoteCandidate(signal)
	}
}

func (a *remoteAgent) acceptP2POffer(signal p2pSignal) {
	a.closeP2PPeer(signal.ClientID)
	iceServers := splitP2PList(getenv("WEBRTC_STUN_SERVERS", "stun:stun.l.google.com:19302"))
	if len(signal.ICEServers) > 0 {
		iceServers = signal.ICEServers
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: iceServers}},
	})
	if err != nil {
		a.sendP2PSignal(p2pSignal{ClientID: signal.ClientID, Kind: "error", SDP: err.Error()})
		return
	}
	peer := &p2pPeer{clientID: signal.ClientID, pc: pc, uploads: map[string]*p2pUpload{}}
	a.p2pMu.Lock()
	if a.p2pPeers == nil {
		a.p2pPeers = map[string]*p2pPeer{}
	}
	a.p2pPeers[signal.ClientID] = peer
	a.p2pMu.Unlock()

	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		init := candidate.ToJSON()
		a.sendP2PSignal(p2pSignal{
			ClientID:         signal.ClientID,
			Kind:             "candidate",
			Candidate:        init.Candidate,
			SDPMid:           init.SDPMid,
			SDPMLineIndex:    init.SDPMLineIndex,
			UsernameFragment: init.UsernameFragment,
		})
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			a.closeP2PPeer(signal.ClientID)
		}
	})
	pc.OnDataChannel(func(dataChannel *webrtc.DataChannel) {
		if dataChannel.Label() != "codex-control" {
			return
		}
		peer.mu.Lock()
		peer.dc = dataChannel
		peer.mu.Unlock()
		dataChannel.OnMessage(func(message webrtc.DataChannelMessage) {
			a.handleP2PCommand(peer, message.Data)
		})
		dataChannel.OnClose(func() { a.closeP2PPeer(signal.ClientID) })
		dataChannel.OnError(func(err error) { a.closeP2PPeer(signal.ClientID) })
	})

	offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: signal.SDP}
	if err := pc.SetRemoteDescription(offer); err != nil {
		a.closeP2PPeer(signal.ClientID)
		return
	}
	peer.mu.Lock()
	peer.remoteDescriptionSet = true
	candidates := append([]webrtc.ICECandidateInit(nil), peer.pendingCandidates...)
	peer.pendingCandidates = nil
	peer.mu.Unlock()
	for _, candidate := range candidates {
		_ = pc.AddICECandidate(candidate)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		a.closeP2PPeer(signal.ClientID)
		return
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		a.closeP2PPeer(signal.ClientID)
		return
	}
	a.sendP2PSignal(p2pSignal{ClientID: signal.ClientID, Kind: "answer", SDP: answer.SDP})
}

func (a *remoteAgent) addP2PRemoteCandidate(signal p2pSignal) {
	a.p2pMu.Lock()
	peer := a.p2pPeers[signal.ClientID]
	a.p2pMu.Unlock()
	if peer == nil {
		return
	}
	candidate := webrtc.ICECandidateInit{
		Candidate:        signal.Candidate,
		SDPMid:           signal.SDPMid,
		SDPMLineIndex:    signal.SDPMLineIndex,
		UsernameFragment: signal.UsernameFragment,
	}
	peer.mu.Lock()
	ready := peer.remoteDescriptionSet
	if !ready {
		peer.pendingCandidates = append(peer.pendingCandidates, candidate)
	}
	peer.mu.Unlock()
	if ready {
		_ = peer.pc.AddICECandidate(candidate)
	}
}

func (a *remoteAgent) sendP2PSignal(signal p2pSignal) {
	a.send("p2p.signal", "", "", signal)
}

func (a *remoteAgent) handleP2PCommand(peer *p2pPeer, raw []byte) {
	var message remoteEnvelope
	if err := json.Unmarshal(raw, &message); err != nil || message.Type != "command" {
		return
	}
	go func() {
		var result interface{}
		var err error
		if strings.HasPrefix(message.Action, "uploads.") {
			result, err = a.handleP2PUpload(peer, message.Action, message.Payload)
		} else {
			result, err = a.executeCommand(message.Action, message.Payload)
		}
		response := remoteEnvelope{Type: "response", RequestID: message.RequestID, Action: message.Action}
		if err != nil {
			response.Error = err.Error()
		} else if result != nil {
			response.Payload, _ = json.Marshal(result)
		}
		a.sendP2PPeer(peer, response)
	}()
}

func (a *remoteAgent) handleP2PUpload(peer *p2pPeer, action string, raw json.RawMessage) (interface{}, error) {
	switch action {
	case "uploads.start":
		var body struct {
			UploadID string `json:"uploadId"`
			Name     string `json:"name"`
			MimeType string `json:"mimeType"`
			Size     int    `json:"size"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || body.UploadID == "" {
			return nil, errors.New("上传参数无效")
		}
		if !strings.HasPrefix(body.MimeType, "image/") || body.Size <= 0 || body.Size > p2pMaxUploadBytes {
			return nil, errors.New("图片附件无效或超过 10MB")
		}
		if !isSafeP2PID(body.UploadID) {
			return nil, errors.New("上传标识无效")
		}
		if err := os.MkdirAll(filepath.Join(a.store.dataDir, "uploads"), 0o755); err != nil {
			return nil, err
		}
		ext := extensionForMime(body.MimeType)
		path := filepath.Join(a.store.dataDir, "uploads", randomID()+ext)
		file, err := os.Create(path)
		if err != nil {
			return nil, err
		}
		peer.mu.Lock()
		if old := peer.uploads[body.UploadID]; old != nil {
			_ = old.File.Close()
			_ = os.Remove(old.Path)
		}
		peer.uploads[body.UploadID] = &p2pUpload{ID: body.UploadID, Name: body.Name, MimeType: body.MimeType, Path: path, Size: body.Size, File: file}
		peer.mu.Unlock()
		return map[string]string{"uploadId": body.UploadID}, nil
	case "uploads.chunk":
		var body struct {
			UploadID string `json:"uploadId"`
			Data     string `json:"data"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, errors.New("上传分块格式无效")
		}
		chunk, err := base64.RawStdEncoding.DecodeString(body.Data)
		if err != nil || len(chunk) == 0 || len(chunk) > p2pChunkBytes {
			return nil, errors.New("上传分块无效")
		}
		peer.mu.Lock()
		upload := peer.uploads[body.UploadID]
		if upload != nil && upload.Received+len(chunk) <= upload.Size {
			_, err = upload.File.Write(chunk)
			if err == nil {
				upload.Received += len(chunk)
			}
		}
		valid := upload != nil && err == nil
		peer.mu.Unlock()
		if !valid {
			return nil, errors.New("上传分块无法写入")
		}
		return map[string]bool{"ok": true}, nil
	case "uploads.finish":
		var body struct {
			UploadID string `json:"uploadId"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, errors.New("上传完成参数无效")
		}
		peer.mu.Lock()
		upload := peer.uploads[body.UploadID]
		if upload != nil {
			delete(peer.uploads, body.UploadID)
		}
		peer.mu.Unlock()
		if upload == nil {
			return nil, errors.New("上传不存在")
		}
		if upload.Received != upload.Size {
			_ = upload.File.Close()
			_ = os.Remove(upload.Path)
			return nil, errors.New("上传内容不完整")
		}
		if err := upload.File.Close(); err != nil {
			_ = os.Remove(upload.Path)
			return nil, err
		}
		return Attachment{ID: upload.ID, Name: firstNonEmptyString(upload.Name, filepath.Base(upload.Path)), MimeType: upload.MimeType, Path: upload.Path, URL: "/uploads/" + filepath.Base(upload.Path)}, nil
	default:
		return nil, fmt.Errorf("未知上传操作: %s", action)
	}
}

func (a *remoteAgent) sendP2PPeer(peer *p2pPeer, message remoteEnvelope) {
	raw, err := json.Marshal(message)
	if err != nil {
		return
	}
	peer.mu.Lock()
	dataChannel := peer.dc
	peer.mu.Unlock()
	if dataChannel == nil || dataChannel.ReadyState() != webrtc.DataChannelStateOpen {
		return
	}
	peer.write.Lock()
	defer peer.write.Unlock()
	_ = dataChannel.SendText(string(raw))
}

func (a *remoteAgent) sendP2P(message remoteEnvelope) bool {
	a.p2pMu.Lock()
	peers := make([]*p2pPeer, 0, len(a.p2pPeers))
	for _, peer := range a.p2pPeers {
		peers = append(peers, peer)
	}
	a.p2pMu.Unlock()
	ready := false
	for _, peer := range peers {
		peer.mu.Lock()
		open := peer.dc != nil && peer.dc.ReadyState() == webrtc.DataChannelStateOpen
		peer.mu.Unlock()
		if open {
			a.sendP2PPeer(peer, message)
			ready = true
		}
	}
	return ready
}

func (a *remoteAgent) closeP2PPeer(clientID string) {
	a.p2pMu.Lock()
	peer := a.p2pPeers[clientID]
	delete(a.p2pPeers, clientID)
	a.p2pMu.Unlock()
	if peer == nil {
		return
	}
	peer.closeOnce.Do(func() {
		peer.mu.Lock()
		for _, upload := range peer.uploads {
			_ = upload.File.Close()
			_ = os.Remove(upload.Path)
		}
		peer.uploads = map[string]*p2pUpload{}
		peer.mu.Unlock()
		_ = peer.pc.Close()
	})
}

func isSafeP2PID(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func splitP2PList(value string) []string {
	items := []string{}
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}

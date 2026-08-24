package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
)

const (
	portMappingDataChannel    = "codex-port-map"
	portMappingConnectTimeout = 8 * time.Second
	portMappingBufferSize     = 16 * 1024
)

type portP2PSignal struct {
	ClientID         string   `json:"clientId"`
	Kind             string   `json:"kind"`
	SDP              string   `json:"sdp,omitempty"`
	Candidate        string   `json:"candidate,omitempty"`
	SDPMid           *string  `json:"sdpMid,omitempty"`
	SDPMLineIndex    *uint16  `json:"sdpMLineIndex,omitempty"`
	UsernameFragment *string  `json:"usernameFragment,omitempty"`
	ICEServers       []string `json:"iceServers,omitempty"`
}

type portMappingManager struct {
	server    *relayServer
	mu        sync.Mutex
	listeners map[string]*portMappingListener
	peers     map[string]*portP2PPeer
	failures  map[string]string
}

type portMappingListener struct {
	manager   *portMappingManager
	mapping   PortMapping
	listener  net.Listener
	active    atomic.Int64
	done      chan struct{}
	closeOnce sync.Once
}

func newPortMappingManager(server *relayServer) *portMappingManager {
	return &portMappingManager{server: server, listeners: map[string]*portMappingListener{}, peers: map[string]*portP2PPeer{}, failures: map[string]string{}}
}

func (m *portMappingManager) start() error {
	return m.reconcile()
}

func (m *portMappingManager) stop() {
	m.mu.Lock()
	listeners := make([]*portMappingListener, 0, len(m.listeners))
	for id, listener := range m.listeners {
		delete(m.listeners, id)
		listeners = append(listeners, listener)
	}
	peers := make([]*portP2PPeer, 0, len(m.peers))
	for id, peer := range m.peers {
		delete(m.peers, id)
		peers = append(peers, peer)
	}
	m.mu.Unlock()
	for _, listener := range listeners {
		listener.close()
	}
	for _, peer := range peers {
		peer.close(errors.New("服务端已关闭"))
	}
}

func (m *portMappingManager) reconcile() error {
	if m.server == nil {
		return errors.New("端口映射管理器未绑定服务端")
	}
	mappings, err := m.server.store.listAllPortMappings()
	if err != nil {
		return err
	}
	current := make(map[string]PortMapping, len(mappings))
	for _, mapping := range mappings {
		current[mapping.ID] = mapping
	}
	m.mu.Lock()
	for id, listener := range m.listeners {
		mapping, ok := current[id]
		if !ok || !mapping.Enabled || !samePortMapping(listener.mapping, mapping) {
			delete(m.listeners, id)
			listener.close()
			for clientID, peer := range m.peers {
				if peer.mapping.ID == id {
					delete(m.peers, clientID)
					go peer.close(errors.New("端口映射已停用或更新"))
				}
			}
		}
	}
	for id := range m.failures {
		if _, ok := current[id]; !ok {
			delete(m.failures, id)
		}
	}
	m.mu.Unlock()
	for _, mapping := range mappings {
		if !mapping.Enabled {
			continue
		}
		m.ensureListener(mapping)
	}
	return nil
}

func samePortMapping(left, right PortMapping) bool {
	return left.DeviceID == right.DeviceID && left.Name == right.Name && left.TargetHost == right.TargetHost && left.TargetPort == right.TargetPort && left.ListenPort == right.ListenPort && left.Protocol == right.Protocol && left.Enabled == right.Enabled
}

func (m *portMappingManager) ensureListener(mapping PortMapping) {
	m.mu.Lock()
	if existing := m.listeners[mapping.ID]; existing != nil {
		m.mu.Unlock()
		return
	}
	if mapping.ListenPort == serverPort(m.server) {
		m.failures[mapping.ID] = fmt.Sprintf("公开端口与服务端 HTTP 端口 %d 冲突", mapping.ListenPort)
		m.mu.Unlock()
		return
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(mapping.ListenPort)))
	if err != nil {
		m.failures[mapping.ID] = "监听公开端口失败: " + err.Error()
		m.mu.Unlock()
		return
	}
	m.failures[mapping.ID] = ""
	item := &portMappingListener{manager: m, mapping: mapping, listener: listener, done: make(chan struct{})}
	m.listeners[mapping.ID] = item
	m.mu.Unlock()
	go item.acceptLoop()
}

func serverPort(server *relayServer) int {
	port, err := strconv.Atoi(env("PORT", "8787"))
	if err != nil || port < 1 || port > 65535 {
		return 8787
	}
	return port
}

func (m *portMappingManager) setFailure(mappingID, message string) {
	m.mu.Lock()
	if _, exists := m.listeners[mappingID]; exists {
		m.failures[mappingID] = message
	}
	m.mu.Unlock()
}

func (m *portMappingManager) addPeer(peer *portP2PPeer) {
	m.mu.Lock()
	m.peers[peer.clientID] = peer
	m.failures[peer.mapping.ID] = ""
	m.mu.Unlock()
}

func (m *portMappingManager) removePeer(peer *portP2PPeer) {
	m.mu.Lock()
	if m.peers[peer.clientID] == peer {
		delete(m.peers, peer.clientID)
	}
	m.mu.Unlock()
}

func (m *portMappingManager) peer(clientID string) *portP2PPeer {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.peers[clientID]
}

func (m *portMappingManager) closeDevice(deviceID string) {
	m.mu.Lock()
	peers := make([]*portP2PPeer, 0)
	for _, peer := range m.peers {
		if peer.deviceID == deviceID {
			peers = append(peers, peer)
		}
	}
	m.mu.Unlock()
	for _, peer := range peers {
		peer.close(errors.New("目标设备连接已断开"))
	}
}

func (m *portMappingManager) runtimeMappings(userID string) ([]PortMapping, error) {
	mappings, err := m.server.store.listPortMappings(userID)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for index := range mappings {
		mapping := &mappings[index]
		if listener := m.listeners[mapping.ID]; listener != nil {
			mapping.Listening = true
			for _, peer := range m.peers {
				if peer.mapping.ID == mapping.ID && peer.connected.Load() {
					mapping.P2PConnected = true
					break
				}
			}
		}
		mapping.LastError = m.failures[mapping.ID]
	}
	return mappings, nil
}

func (l *portMappingListener) close() {
	l.closeOnce.Do(func() {
		close(l.done)
		_ = l.listener.Close()
	})
}

func (l *portMappingListener) acceptLoop() {
	for {
		conn, err := l.listener.Accept()
		if err != nil {
			select {
			case <-l.done:
				return
			default:
			}
			l.manager.setFailure(l.mapping.ID, "监听公开端口已停止: "+err.Error())
			return
		}
		l.active.Add(1)
		go l.handle(conn)
	}
}

func (l *portMappingListener) handle(conn net.Conn) {
	defer l.active.Add(-1)
	defer conn.Close()
	peer, err := newPortP2PPeer(l.manager.server, l.mapping, conn)
	if err != nil {
		l.manager.setFailure(l.mapping.ID, "P2P 连接失败，已拒绝访问: "+err.Error())
		return
	}
	defer peer.close(nil)
	<-peer.done
}

type portP2PPeer struct {
	server               *relayServer
	mapping              PortMapping
	userID               string
	deviceID             string
	clientID             string
	tcpConn              net.Conn
	pc                   *webrtc.PeerConnection
	dc                   *webrtc.DataChannel
	write                sync.Mutex
	done                 chan struct{}
	ready                chan struct{}
	readyOnce            sync.Once
	connected            atomic.Bool
	closeOnce            sync.Once
	remoteDescriptionSet bool
	pendingCandidates    []webrtc.ICECandidateInit
}

func newPortP2PPeer(server *relayServer, mapping PortMapping, tcpConn net.Conn) (*portP2PPeer, error) {
	if server.webrtcAPI == nil {
		return nil, errors.New("WebRTC 固定 UDP 端口未启用")
	}
	agent := server.agentPeer(mapping.DeviceID)
	if agent == nil || agent.userID != mapping.UserID {
		return nil, errors.New("目标设备不在线")
	}
	peerConnection, err := server.webrtcAPI.NewPeerConnection(webrtc.Configuration{ICEServers: server.p2pICEServers()})
	if err != nil {
		return nil, err
	}
	peer := &portP2PPeer{server: server, mapping: mapping, userID: mapping.UserID, deviceID: mapping.DeviceID, clientID: "port-" + randomID(), tcpConn: tcpConn, pc: peerConnection, done: make(chan struct{}), ready: make(chan struct{})}
	peerConnection.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		init := candidate.ToJSON()
		peer.sendSignal(portP2PSignal{ClientID: peer.clientID, Kind: "candidate", Candidate: init.Candidate, SDPMid: init.SDPMid, SDPMLineIndex: init.SDPMLineIndex, UsernameFragment: init.UsernameFragment})
	})
	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			peer.close(errors.New("WebRTC 连接失败"))
		}
	})
	peer.dc, err = peerConnection.CreateDataChannel(portMappingDataChannel, nil)
	if err != nil {
		_ = peerConnection.Close()
		return nil, err
	}
	peer.dc.OnOpen(func() {
		peer.connected.Store(true)
		peer.readyOnce.Do(func() { close(peer.ready) })
		init := map[string]interface{}{"type": "port-map-init", "targetHost": mapping.TargetHost, "targetPort": mapping.TargetPort}
		if err := peer.dc.SendText(string(mustJSON(init))); err != nil {
			peer.close(err)
			return
		}
		go peer.readTCP()
	})
	peer.dc.OnMessage(func(message webrtc.DataChannelMessage) {
		if message.IsString {
			return
		}
		if _, err := peer.tcpConn.Write(message.Data); err != nil {
			peer.close(err)
		}
	})
	peer.dc.OnClose(func() { peer.close(errors.New("P2P 数据通道已关闭")) })
	peer.dc.OnError(func(err error) { peer.close(err) })
	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		peer.close(err)
		return nil, err
	}
	if err := peerConnection.SetLocalDescription(offer); err != nil {
		peer.close(err)
		return nil, err
	}
	server.portMappings.addPeer(peer)
	peer.sendSignal(portP2PSignal{ClientID: peer.clientID, Kind: "offer", SDP: offer.SDP, ICEServers: server.p2pICEURLs()})
	go func() {
		select {
		case <-peer.done:
		case <-peer.ready:
		case <-time.After(portMappingConnectTimeout):
			peer.close(errors.New("P2P 连接超时，已拒绝访问"))
		}
	}()
	return peer, nil
}

func (p *portP2PPeer) sendSignal(signal portP2PSignal) {
	agent := p.server.agentPeer(p.deviceID)
	if agent == nil || agent.userID != p.userID {
		p.close(errors.New("目标设备连接已断开"))
		return
	}
	_ = agent.writeJSON(envelope{Type: "p2p.signal", Payload: mustJSON(signal)})
}

func (p *portP2PPeer) handleSignal(signal portP2PSignal) {
	switch signal.Kind {
	case "answer":
		if signal.SDP == "" {
			return
		}
		if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: signal.SDP}); err != nil {
			p.close(err)
			return
		}
		p.write.Lock()
		p.remoteDescriptionSet = true
		candidates := append([]webrtc.ICECandidateInit(nil), p.pendingCandidates...)
		p.pendingCandidates = nil
		p.write.Unlock()
		for _, candidate := range candidates {
			_ = p.pc.AddICECandidate(candidate)
		}
	case "candidate":
		candidate := webrtc.ICECandidateInit{Candidate: signal.Candidate, SDPMid: signal.SDPMid, SDPMLineIndex: signal.SDPMLineIndex, UsernameFragment: signal.UsernameFragment}
		p.write.Lock()
		ready := p.remoteDescriptionSet
		if !ready {
			p.pendingCandidates = append(p.pendingCandidates, candidate)
		}
		p.write.Unlock()
		if ready {
			_ = p.pc.AddICECandidate(candidate)
		}
	case "error":
		reason := signal.SDP
		if reason == "" {
			reason = "客户端 P2P 协商失败"
		}
		p.close(errors.New(reason))
	}
}

func (p *portP2PPeer) readTCP() {
	buffer := make([]byte, portMappingBufferSize)
	for {
		n, err := p.tcpConn.Read(buffer)
		if n > 0 {
			p.write.Lock()
			dc := p.dc
			if dc != nil && dc.ReadyState() == webrtc.DataChannelStateOpen {
				err = dc.Send(buffer[:n])
			}
			p.write.Unlock()
		}
		if err != nil {
			p.close(err)
			return
		}
	}
}

func (p *portP2PPeer) close(reason error) {
	p.closeOnce.Do(func() {
		p.connected.Store(false)
		if reason != nil {
			p.server.portMappings.setFailure(p.mapping.ID, "P2P 连接失败，已拒绝访问: "+reason.Error())
		}
		close(p.done)
		p.server.portMappings.removePeer(p)
		_ = p.tcpConn.Close()
		_ = p.pc.Close()
		if agent := p.server.agentPeer(p.deviceID); agent != nil {
			_ = agent.writeJSON(envelope{Type: "p2p.close", Payload: mustJSON(map[string]string{"clientId": p.clientID})})
		}
	})
}

func (s *relayServer) p2pICEURLs() []string {
	urls := append([]string(nil), s.iceServers...)
	host := strings.TrimSpace(s.stunHost)
	if host == "" {
		s.mu.Lock()
		host = s.publicHost
		s.mu.Unlock()
	}
	port := strings.TrimSpace(s.stunPublicPort)
	if host != "" && port != "" && !strings.EqualFold(port, "disabled") {
		candidate := "stun:" + net.JoinHostPort(strings.Trim(host, "[]"), port)
		for _, existing := range urls {
			if existing == candidate {
				return urls
			}
		}
		urls = append(urls, candidate)
	}
	return urls
}

func (s *relayServer) p2pICEServers() []webrtc.ICEServer {
	urls := s.p2pICEURLs()
	if len(urls) == 0 {
		return nil
	}
	return []webrtc.ICEServer{{URLs: urls}}
}

func (s *relayServer) userPortMappings(w http.ResponseWriter, request *http.Request) {
	_ = s.iceServersForRequest(request)
	user, ok := s.userFromRequest(request)
	if !ok {
		writeErrorStatus(w, http.StatusUnauthorized, "请先登录")
		return
	}
	switch request.Method {
	case http.MethodGet:
		mappings, err := s.portMappings.runtimeMappings(user.ID)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, map[string]interface{}{"mappings": mappings})
	case http.MethodPost:
		var input portMappingInput
		if err := decodeJSON(request, &input); err != nil {
			writeErrorStatus(w, http.StatusBadRequest, "请求格式不正确")
			return
		}
		input = normalizePortMappingInput(input)
		if input.ListenPort == serverPort(s) {
			writeErrorStatus(w, http.StatusConflict, "公开端口不能使用服务端 HTTP 端口")
			return
		}
		mapping, err := s.store.createPortMapping(user.ID, input)
		if err != nil {
			writePortMappingError(w, err)
			return
		}
		if err := s.portMappings.reconcile(); err != nil {
			writeError(w, err)
			return
		}
		mappings, _ := s.portMappings.runtimeMappings(user.ID)
		writeJSONStatus(w, http.StatusCreated, map[string]interface{}{"mapping": findPortMapping(mappings, mapping.ID)})
	default:
		methodNotAllowed(w)
	}
}

func (s *relayServer) userPortMappingItem(w http.ResponseWriter, request *http.Request) {
	_ = s.iceServersForRequest(request)
	user, ok := s.userFromRequest(request)
	if !ok {
		writeErrorStatus(w, http.StatusUnauthorized, "请先登录")
		return
	}
	mappingID := strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/port-mappings/"), "/")
	if mappingID == "" || strings.Contains(mappingID, "/") {
		writeErrorStatus(w, http.StatusNotFound, "端口映射不存在")
		return
	}
	switch request.Method {
	case http.MethodPatch:
		var input portMappingInput
		if err := decodeJSON(request, &input); err != nil {
			writeErrorStatus(w, http.StatusBadRequest, "请求格式不正确")
			return
		}
		input = normalizePortMappingInput(input)
		if input.ListenPort == serverPort(s) {
			writeErrorStatus(w, http.StatusConflict, "公开端口不能使用服务端 HTTP 端口")
			return
		}
		mapping, err := s.store.updatePortMapping(user.ID, mappingID, input)
		if err != nil {
			writePortMappingError(w, err)
			return
		}
		if err := s.portMappings.reconcile(); err != nil {
			writeError(w, err)
			return
		}
		mappings, _ := s.portMappings.runtimeMappings(user.ID)
		writeJSON(w, map[string]interface{}{"mapping": findPortMapping(mappings, mapping.ID)})
	case http.MethodDelete:
		if err := s.store.deletePortMapping(user.ID, mappingID); err != nil {
			writePortMappingError(w, err)
			return
		}
		if err := s.portMappings.reconcile(); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func findPortMapping(mappings []PortMapping, id string) PortMapping {
	for _, mapping := range mappings {
		if mapping.ID == id {
			return mapping
		}
	}
	return PortMapping{ID: id}
}

func writePortMappingError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if err.Error() == "端口映射不存在" || err.Error() == "目标设备不存在或不属于当前用户" {
		status = http.StatusNotFound
	}
	if strings.Contains(err.Error(), "公开端口已经") || strings.Contains(err.Error(), "不能使用服务端") {
		status = http.StatusConflict
	}
	writeErrorStatus(w, status, err.Error())
}

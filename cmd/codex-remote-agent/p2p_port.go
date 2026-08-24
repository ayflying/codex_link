package main

import (
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

const (
	p2pPortBufferSize      = 16 * 1024
	p2pPortDialTimeout     = 8 * time.Second
	portMappingDataChannel = "codex-port-map"
)

type p2pPortInit struct {
	Type       string `json:"type"`
	TargetHost string `json:"targetHost"`
	TargetPort int    `json:"targetPort"`
}

type p2pPortSession struct {
	dataChannel *webrtc.DataChannel
	conn        net.Conn
	write       sync.Mutex
	mu          sync.Mutex
	initialized bool
	closeOnce   sync.Once
}

func (a *remoteAgent) acceptP2PPortDataChannel(peer *p2pPeer, dataChannel *webrtc.DataChannel) {
	session := &p2pPortSession{dataChannel: dataChannel}
	peer.mu.Lock()
	if peer.port != nil {
		peer.mu.Unlock()
		return
	}
	peer.port = session
	peer.mu.Unlock()
	dataChannel.OnMessage(func(message webrtc.DataChannelMessage) {
		session.handleMessage(message.Data, message.IsString)
	})
	dataChannel.OnClose(func() { a.closeP2PPeer(peer.clientID) })
	dataChannel.OnError(func(err error) { a.closeP2PPeer(peer.clientID) })
}

func (s *p2pPortSession) handleMessage(raw []byte, isString bool) {
	s.mu.Lock()
	initialized := s.initialized
	conn := s.conn
	s.mu.Unlock()
	if !initialized {
		if !isString {
			s.close()
			return
		}
		var init p2pPortInit
		if err := json.Unmarshal(raw, &init); err != nil || validateP2PPortInit(init) != nil {
			s.close()
			return
		}
		if strings.TrimSpace(init.TargetHost) == "" || init.TargetPort < 1 || init.TargetPort > 65535 {
			s.close()
			return
		}
		dialAddress := net.JoinHostPort(strings.TrimSpace(init.TargetHost), strconv.Itoa(init.TargetPort))
		localConn, err := net.DialTimeout("tcp", dialAddress, p2pPortDialTimeout)
		if err != nil {
			s.close()
			return
		}
		s.mu.Lock()
		s.conn = localConn
		s.initialized = true
		s.mu.Unlock()
		go s.readLocal()
		return
	}
	if isString || conn == nil {
		return
	}
	if _, err := conn.Write(raw); err != nil {
		s.close()
	}
}

func (s *p2pPortSession) readLocal() {
	buffer := make([]byte, p2pPortBufferSize)
	for {
		s.mu.Lock()
		conn := s.conn
		s.mu.Unlock()
		if conn == nil {
			s.close()
			return
		}
		n, err := conn.Read(buffer)
		if n > 0 {
			s.write.Lock()
			dc := s.dataChannel
			if dc != nil && dc.ReadyState() == webrtc.DataChannelStateOpen {
				err = dc.Send(buffer[:n])
			}
			s.write.Unlock()
		}
		if err != nil {
			s.close()
			return
		}
	}
}

func (s *p2pPortSession) close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		conn := s.conn
		s.conn = nil
		s.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		if s.dataChannel != nil {
			_ = s.dataChannel.Close()
		}
	})
}

func validateP2PPortInit(init p2pPortInit) error {
	if init.Type != "port-map-init" {
		return errors.New("端口映射初始化消息无效")
	}
	if strings.TrimSpace(init.TargetHost) == "" || init.TargetPort < 1 || init.TargetPort > 65535 {
		return errors.New("端口映射目标地址无效")
	}
	return nil
}

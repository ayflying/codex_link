package main

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/pion/stun/v3"
)

// stunBindingResponse handles only STUN Binding requests. It never accepts or
// forwards WebRTC, TURN, or application data.
func stunBindingResponse(raw []byte, remoteAddr *net.UDPAddr) ([]byte, bool) {
	if remoteAddr == nil {
		return nil, false
	}
	request := new(stun.Message)
	if err := stun.Decode(raw, request); err != nil || request.Type != stun.BindingRequest {
		return nil, false
	}
	response := stun.New()
	response.Type = stun.BindingSuccess
	response.TransactionID = request.TransactionID
	if err := (stun.XORMappedAddress{
		IP:   remoteAddr.IP,
		Port: remoteAddr.Port,
	}).AddTo(response); err != nil {
		return nil, false
	}
	response.Encode()
	return response.Raw, true
}

func (s *relayServer) startSTUN() error {
	port := strings.TrimSpace(s.stunPort)
	if port == "" || strings.EqualFold(port, "disabled") {
		return nil
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("WEBRTC_STUN_PORT 无效: %q", port)
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: portNumber})
	if err != nil {
		return err
	}
	s.stunConn = conn
	s.stunWG.Add(1)
	go func() {
		defer s.stunWG.Done()
		buffer := make([]byte, 1500)
		for {
			n, remoteAddr, readErr := conn.ReadFromUDP(buffer)
			if readErr != nil {
				return
			}
			response, ok := stunBindingResponse(buffer[:n], remoteAddr)
			if ok {
				_, _ = conn.WriteToUDP(response, remoteAddr)
			}
		}
	}()
	return nil
}

func (s *relayServer) iceServersForRequest(request *http.Request) []string {
	servers := append([]string(nil), s.iceServers...)
	if s.stunConn == nil || s.stunPublicPort == "" || strings.EqualFold(s.stunPublicPort, "disabled") {
		return servers
	}
	publicPort, err := strconv.Atoi(s.stunPublicPort)
	if err != nil || publicPort < 1 || publicPort > 65535 {
		return servers
	}
	host := strings.TrimSpace(s.stunHost)
	if host == "" && request != nil {
		host = request.Host
		if parsedHost, _, splitErr := net.SplitHostPort(host); splitErr == nil {
			host = parsedHost
		}
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return servers
	}
	server := "stun:" + net.JoinHostPort(host, strconv.Itoa(publicPort))
	for _, existing := range servers {
		if existing == server {
			return servers
		}
	}
	return append(servers, server)
}

func (s *relayServer) closeSTUN() {
	if s.stunConn != nil {
		_ = s.stunConn.Close()
		s.stunWG.Wait()
		s.stunConn = nil
	}
}

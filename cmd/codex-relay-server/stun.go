package main

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/stun/v3"
	"github.com/pion/webrtc/v4"
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
	muxConn := &stunMuxPacketConn{conn: conn}
	s.iceUDPMux = ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: muxConn})
	settingEngine := webrtc.SettingEngine{}
	settingEngine.SetICEUDPMux(s.iceUDPMux)
	settingEngine.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	if publicIPs := resolvePublicIPs(s.stunHost); len(publicIPs) > 0 {
		settingEngine.SetNAT1To1IPs(publicIPs, webrtc.ICECandidateTypeHost)
	}
	s.webrtcAPI = webrtc.NewAPI(webrtc.WithSettingEngine(settingEngine))
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
	if s.stunHost == "" {
		s.mu.Lock()
		if s.publicHost == "" {
			s.publicHost = host
		}
		s.mu.Unlock()
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
	if s.iceUDPMux != nil {
		_ = s.iceUDPMux.Close()
		s.iceUDPMux = nil
		s.stunConn = nil
		return
	}
	if s.stunConn != nil {
		_ = s.stunConn.Close()
		s.stunConn = nil
	}
}

type stunMuxPacketConn struct {
	conn *net.UDPConn
}

func (c *stunMuxPacketConn) ReadFrom(buffer []byte) (int, net.Addr, error) {
	n, remoteAddr, err := c.conn.ReadFromUDP(buffer)
	if err != nil {
		return n, remoteAddr, err
	}
	if response, ok := stunBindingResponse(buffer[:n], remoteAddr); ok {
		_, _ = c.conn.WriteToUDP(response, remoteAddr)
	}
	return n, remoteAddr, nil
}

func (c *stunMuxPacketConn) WriteTo(buffer []byte, address net.Addr) (int, error) {
	return c.conn.WriteTo(buffer, address)
}

func (c *stunMuxPacketConn) Close() error { return c.conn.Close() }

func (c *stunMuxPacketConn) LocalAddr() net.Addr { return c.conn.LocalAddr() }

func (c *stunMuxPacketConn) SetDeadline(deadline time.Time) error {
	return c.conn.SetDeadline(deadline)
}

func (c *stunMuxPacketConn) SetReadDeadline(deadline time.Time) error {
	return c.conn.SetReadDeadline(deadline)
}

func (c *stunMuxPacketConn) SetWriteDeadline(deadline time.Time) error {
	return c.conn.SetWriteDeadline(deadline)
}

func resolvePublicIPs(host string) []string {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil {
			return []string{ip.To4().String()}
		}
		return nil
	}
	addresses, err := net.LookupIP(host)
	if err != nil {
		return nil
	}
	result := make([]string, 0, len(addresses))
	for _, address := range addresses {
		if ipv4 := address.To4(); ipv4 != nil {
			result = append(result, ipv4.String())
		}
	}
	return result
}

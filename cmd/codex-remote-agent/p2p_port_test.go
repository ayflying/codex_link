package main

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestValidateP2PPortInitAllowsLANHost(t *testing.T) {
	if err := validateP2PPortInit(p2pPortInit{Type: "port-map-init", TargetHost: "192.168.50.42", TargetPort: 5000}); err != nil {
		t.Fatalf("LAN target rejected: %v", err)
	}
}

func TestP2PPortSessionForwardsTCPBothDirections(t *testing.T) {
	targetListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetListener.Close()
	targetPort := targetListener.Addr().(*net.TCPAddr).Port
	targetReady := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := targetListener.Accept()
		if acceptErr != nil {
			return
		}
		targetReady <- conn
		defer conn.Close()
		buffer := make([]byte, 64)
		if count, readErr := conn.Read(buffer); readErr == nil {
			_, _ = conn.Write(append([]byte("echo:"), buffer[:count]...))
		}
	}()

	serverPC, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer serverPC.Close()
	agentPC, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer agentPC.Close()
	agent := &remoteAgent{p2pPeers: map[string]*p2pPeer{}}
	peer := &p2pPeer{clientID: "port-test", pc: agentPC, uploads: map[string]*p2pUpload{}}
	agent.p2pPeers[peer.clientID] = peer
	agentPC.OnDataChannel(func(dataChannel *webrtc.DataChannel) {
		if dataChannel.Label() == portMappingDataChannel {
			agent.acceptP2PPortDataChannel(peer, dataChannel)
		}
	})

	publicServer, publicClient := net.Pipe()
	defer publicServer.Close()
	defer publicClient.Close()
	serverDataChannel, err := serverPC.CreateDataChannel(portMappingDataChannel, nil)
	if err != nil {
		t.Fatal(err)
	}
	serverDataChannel.OnOpen(func() {
		init, _ := json.Marshal(p2pPortInit{Type: "port-map-init", TargetHost: "127.0.0.1", TargetPort: targetPort})
		_ = serverDataChannel.SendText(string(init))
		go func() {
			buffer := make([]byte, p2pPortBufferSize)
			for {
				count, readErr := publicServer.Read(buffer)
				if count > 0 {
					_ = serverDataChannel.Send(buffer[:count])
				}
				if readErr != nil {
					return
				}
			}
		}()
	})
	serverDataChannel.OnMessage(func(message webrtc.DataChannelMessage) {
		if !message.IsString {
			_, _ = publicServer.Write(message.Data)
		}
	})

	offer, err := serverPC.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := serverPC.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-webrtc.GatheringCompletePromise(serverPC)
	if err := agentPC.SetRemoteDescription(*serverPC.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err := agentPC.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := agentPC.SetLocalDescription(answer); err != nil {
		t.Fatal(err)
	}
	<-webrtc.GatheringCompletePromise(agentPC)
	if err := serverPC.SetRemoteDescription(*agentPC.LocalDescription()); err != nil {
		t.Fatal(err)
	}

	_ = publicClient.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := publicClient.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 64)
	count, err := publicClient.Read(response)
	if err != nil {
		t.Fatal(err)
	}
	if string(response[:count]) != "echo:ping" {
		t.Fatalf("unexpected TCP response: %q", response[:count])
	}
	select {
	case conn := <-targetReady:
		_ = conn.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not connect to the configured local target")
	}
}

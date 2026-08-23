package main

import (
	"net"
	"net/http/httptest"
	"testing"

	"github.com/pion/stun/v3"
)

func TestSTUNICEServerAddress(t *testing.T) {
	server := &relayServer{
		iceServers:     []string{"stun:stun.example:19302"},
		stunHost:       "relay.example.com",
		stunPublicPort: "45678",
		stunConn:       &net.UDPConn{},
	}
	servers := server.iceServersForRequest(httptest.NewRequest("GET", "https://ignored.example/", nil))
	if len(servers) != 2 || servers[1] != "stun:relay.example.com:45678" {
		t.Fatalf("unexpected ICE servers: %#v", servers)
	}
}

func TestSTUNBindingResponse(t *testing.T) {
	request := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	remoteAddr := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 54321}
	raw, ok := stunBindingResponse(request.Raw, remoteAddr)
	if !ok {
		t.Fatal("expected a STUN Binding response")
	}
	response := new(stun.Message)
	if err := stun.Decode(raw, response); err != nil {
		t.Fatalf("decode STUN response: %v", err)
	}
	if response.Type != stun.BindingSuccess {
		t.Fatalf("unexpected STUN response type: %v", response.Type)
	}
	if response.TransactionID != request.TransactionID {
		t.Fatal("STUN transaction ID was not preserved")
	}
}

func TestSTUNRejectsNonBindingPackets(t *testing.T) {
	request := stun.MustBuild(stun.TransactionID, stun.BindingSuccess)
	if _, ok := stunBindingResponse(request.Raw, &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 54321}); ok {
		t.Fatal("non-binding STUN packet must not receive a response")
	}
	if _, ok := stunBindingResponse([]byte("application-data"), &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 54321}); ok {
		t.Fatal("non-STUN packet must not receive a response")
	}
}

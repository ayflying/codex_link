package main

import "testing"

func TestValidatePortMappingInput(t *testing.T) {
	valid := portMappingInput{DeviceID: "device", Name: "远程调试", TargetHost: "127.0.0.1", TargetPort: 9222, ListenPort: 19022, Protocol: "tcp"}
	if err := validatePortMappingInput(valid); err != nil {
		t.Fatalf("valid mapping rejected: %v", err)
	}
	for _, test := range []struct {
		name  string
		input portMappingInput
	}{
		{"invalid target port", portMappingInput{DeviceID: "device", Name: "debug", TargetHost: "127.0.0.1", TargetPort: 0, ListenPort: 19022, Protocol: "tcp"}},
		{"invalid listen port", portMappingInput{DeviceID: "device", Name: "debug", TargetHost: "127.0.0.1", TargetPort: 9222, ListenPort: 65536, Protocol: "tcp"}},
		{"unsupported protocol", portMappingInput{DeviceID: "device", Name: "debug", TargetHost: "127.0.0.1", TargetPort: 9222, ListenPort: 19022, Protocol: "udp"}},
		{"missing device", portMappingInput{DeviceID: "", Name: "debug", TargetHost: "127.0.0.1", TargetPort: 9222, ListenPort: 19022, Protocol: "tcp"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validatePortMappingInput(test.input); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestNormalizePortMappingInput(t *testing.T) {
	input := normalizePortMappingInput(portMappingInput{DeviceID: " device ", Name: " debug ", TargetPort: 9222, ListenPort: 19022})
	if input.DeviceID != "device" || input.Name != "debug" || input.TargetHost != "127.0.0.1" || input.Protocol != "tcp" || input.Enabled == nil || !*input.Enabled {
		t.Fatalf("unexpected normalized input: %#v", input)
	}
}

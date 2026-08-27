//go:build !windows

package main

import "fmt"

func startClientUpdateHelper(_, _ string, _ []string) error {
	return fmt.Errorf("自动更新仅支持 Windows x64 客户端")
}

func applyClientUpdateHelper(_ []string) error {
	return fmt.Errorf("自动更新仅支持 Windows x64 客户端")
}

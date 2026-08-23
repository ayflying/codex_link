//go:build !windows

package main

import "errors"

func runClientGUI(root, cwd, dataDir string) error {
	return errors.New("客户端图形界面仅支持 Windows")
}

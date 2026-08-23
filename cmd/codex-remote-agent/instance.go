package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
)

var errInstanceAlreadyRunning = errors.New("同一个客户端实例已经在运行")

func instanceMutexName(role, dataDir string) string {
	value := strings.ToLower(role + "\x00" + filepath.Clean(dataDir))
	digest := sha256.Sum256([]byte(value))
	return "CodexLink-" + role + "-" + hex.EncodeToString(digest[:])
}

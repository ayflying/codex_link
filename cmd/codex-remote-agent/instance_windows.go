//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32Instance     = syscall.NewLazyDLL("kernel32.dll")
	createMutexWInstance = kernel32Instance.NewProc("CreateMutexW")
	closeHandleInstance  = kernel32Instance.NewProc("CloseHandle")
)

func acquireInstance(role, dataDir string) (func(), error) {
	name, err := syscall.UTF16PtrFromString("Local\\" + instanceMutexName(role, dataDir))
	if err != nil {
		return func() {}, fmt.Errorf("创建客户端实例锁名称失败: %w", err)
	}
	handle, _, callErr := createMutexWInstance.Call(0, 1, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return func() {}, fmt.Errorf("创建客户端实例锁失败: %w", callErr)
	}
	if callErr == syscall.ERROR_ALREADY_EXISTS {
		_, _, _ = closeHandleInstance.Call(handle)
		return func() {}, errInstanceAlreadyRunning
	}
	return func() {
		_, _, _ = closeHandleInstance.Call(handle)
	}, nil
}

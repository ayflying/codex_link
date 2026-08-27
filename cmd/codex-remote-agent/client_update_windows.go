//go:build windows

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

const (
	processSynchronize = 0x00100000
	waitObject0        = 0
	waitTimeout        = 0x00000102
	errorInvalidParam  = syscall.Errno(87)
)

var (
	kernel32ClientUpdate      = syscall.NewLazyDLL("kernel32.dll")
	openProcessClientUpdate   = kernel32ClientUpdate.NewProc("OpenProcess")
	waitForSingleObjectUpdate = kernel32ClientUpdate.NewProc("WaitForSingleObject")
	closeHandleClientUpdate   = kernel32ClientUpdate.NewProc("CloseHandle")
)

func startClientUpdateHelper(executable, pending string, originalArgs []string) error {
	helper := clientUpdateArtifactPath(executable, ".updater-"+strconv.Itoa(os.Getpid()))
	if err := copyClientUpdateHelper(executable, helper); err != nil {
		return err
	}
	arguments := []string{
		clientUpdateHelperFlag,
		"--parent-pid", strconv.Itoa(os.Getpid()),
		"--target", executable,
		"--pending", pending,
		"--",
	}
	arguments = append(arguments, originalArgs...)
	command := exec.Command(helper, arguments...)
	command.Dir = filepath.Dir(executable)
	if err := command.Start(); err != nil {
		_ = os.Remove(helper)
		return fmt.Errorf("启动客户端更新助手失败: %w", err)
	}
	return nil
}

func applyClientUpdateHelper(arguments []string) error {
	flags := flag.NewFlagSet(clientUpdateHelperFlag, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	parentPID := flags.Int("parent-pid", 0, "")
	target := flags.String("target", "", "")
	pending := flags.String("pending", "", "")
	if err := flags.Parse(arguments); err != nil {
		return fmt.Errorf("读取客户端更新参数失败: %w", err)
	}
	if *parentPID <= 0 || *target == "" || *pending == "" {
		return errors.New("客户端更新参数不完整")
	}
	if filepath.Clean(filepath.Dir(*target)) != filepath.Clean(filepath.Dir(*pending)) {
		return errors.New("客户端更新文件必须位于同一目录")
	}
	if err := waitForClientProcessExit(*parentPID); err != nil {
		return err
	}
	return replaceClientExecutable(*target, *pending, flags.Args())
}

func copyClientUpdateHelper(sourcePath, helperPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("读取当前客户端失败: %w", err)
	}
	defer source.Close()
	helper, err := os.OpenFile(helperPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return fmt.Errorf("创建客户端更新助手失败: %w", err)
	}
	if _, err := io.Copy(helper, source); err != nil {
		_ = helper.Close()
		_ = os.Remove(helperPath)
		return fmt.Errorf("复制客户端更新助手失败: %w", err)
	}
	if err := helper.Close(); err != nil {
		_ = os.Remove(helperPath)
		return fmt.Errorf("保存客户端更新助手失败: %w", err)
	}
	return nil
}

func waitForClientProcessExit(processID int) error {
	handle, _, callErr := openProcessClientUpdate.Call(processSynchronize, 0, uintptr(processID))
	if handle == 0 {
		if callErr == errorInvalidParam {
			return nil
		}
		return fmt.Errorf("等待客户端退出失败: %w", callErr)
	}
	defer closeHandleClientUpdate.Call(handle)
	result, _, callErr := waitForSingleObjectUpdate.Call(handle, 90_000)
	switch result {
	case waitObject0:
		return nil
	case waitTimeout:
		return errors.New("等待客户端退出超时")
	default:
		return fmt.Errorf("等待客户端退出失败: %w", callErr)
	}
}

func replaceClientExecutable(target, pending string, originalArgs []string) error {
	previous := clientUpdateArtifactPath(target, ".previous")
	if err := os.Remove(previous); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("清理旧客户端备份失败: %w", err)
	}
	for attempt := 0; attempt < 60; attempt++ {
		if err := os.Rename(target, previous); err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if err := os.Rename(pending, target); err != nil {
			_ = os.Rename(previous, target)
			return fmt.Errorf("替换客户端失败: %w", err)
		}
		command := exec.Command(target, originalArgs...)
		command.Dir = filepath.Dir(target)
		if err := command.Start(); err != nil {
			return fmt.Errorf("启动更新后的客户端失败: %w", err)
		}
		return nil
	}
	return errors.New("客户端仍在运行，无法完成自动更新")
}

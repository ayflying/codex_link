//go:build windows

package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/lxn/win"
)

const (
	clientWindowClass = "CodexLinkClientWindow"
	controlServer     = 1001
	controlToken      = 1002
	controlDevice     = 1003
	controlConnect    = 1004
	controlHide       = 1005
	trayShow          = 2001
	trayExit          = 2002
	wmTray            = win.WM_APP + 1
	wmConnectResult   = win.WM_APP + 2
	wmAgentExited     = win.WM_APP + 3
	wmPollState       = win.WM_APP + 4
	wmTrayError       = win.WM_APP + 5
	wmInitializeTray  = win.WM_APP + 6
	guiLogTimer       = 1

	guiWindowColor    = win.COLORREF(0x00f8f7f5)
	guiInputColor     = win.COLORREF(0x00ffffff)
	guiLogColor       = win.COLORREF(0x00f8f7f5)
	guiInkColor       = win.COLORREF(0x00332b20)
	guiMutedColor     = win.COLORREF(0x006b6155)
	guiAccentColor    = win.COLORREF(0x00766f0f)
	guiLogTextColor   = win.COLORREF(0x00443b2f)
	guiLogTimerPeriod = 750
)

type clientGUI struct {
	hwnd          win.HWND
	serverEdit    win.HWND
	tokenEdit     win.HWND
	deviceEdit    win.HWND
	statusLabel   win.HWND
	detailLabel   win.HWND
	logLabel      win.HWND
	logEdit       win.HWND
	connectButton win.HWND
	titleLabel    win.HWND
	helperLabel   win.HWND

	windowBrush win.HBRUSH
	inputBrush  win.HBRUSH
	logBrush    win.HBRUSH

	root    string
	cwd     string
	dataDir string

	mu       sync.Mutex
	config   remoteAgentConfig
	process  *exec.Cmd
	pollStop chan struct{}
	starting bool
	quitting bool

	trayMu           sync.Mutex
	tray             win.NOTIFYICONDATA
	trayReady        bool
	trayInitializing bool
	trayInitDone     chan struct{}
	trayError        string
	icon             win.HICON
	ownsIcon         bool

	connectResult *guiConnectResult
	agentExitErr  string
	pollState     string
	lastLogText   string
	lastLogState  string
}

type guiConnectResult struct {
	config remoteAgentConfig
	err    error
}

func runClientGUI(root, cwd, dataDir string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	releaseInstance, err := acquireInstance("gui", dataDir)
	if err != nil {
		if errors.Is(err, errInstanceAlreadyRunning) {
			win.MessageBox(0, syscall.StringToUTF16Ptr("客户端已经启动，请在系统托盘中打开已有窗口。"), syscall.StringToUTF16Ptr("Codex Link"), win.MB_ICONINFORMATION|win.MB_OK)
			return nil
		}
		return err
	}
	defer releaseInstance()

	config, _ := loadRemoteAgentConfig(dataDir)
	instance := win.GetModuleHandle(nil)
	icon, ownsIcon := createClientIcon()
	windowBrush := createSolidBrush(guiWindowColor)
	inputBrush := createSolidBrush(guiInputColor)
	logBrush := createSolidBrush(guiLogColor)
	if windowBrush == 0 || inputBrush == 0 || logBrush == 0 {
		deleteBrush(windowBrush)
		deleteBrush(inputBrush)
		deleteBrush(logBrush)
		if ownsIcon {
			win.DestroyIcon(icon)
		}
		return errors.New("创建客户端界面配色失败")
	}
	className := syscall.StringToUTF16Ptr(clientWindowClass)
	windowClass := win.WNDCLASSEX{
		CbSize:        uint32(unsafe.Sizeof(win.WNDCLASSEX{})),
		LpfnWndProc:   syscall.NewCallback(clientWindowProc),
		HInstance:     instance,
		HIcon:         icon,
		HIconSm:       icon,
		HCursor:       win.LoadCursor(0, win.MAKEINTRESOURCE(win.IDC_ARROW)),
		HbrBackground: windowBrush,
		LpszClassName: className,
	}
	if win.RegisterClassEx(&windowClass) == 0 {
		deleteBrush(windowBrush)
		deleteBrush(inputBrush)
		deleteBrush(logBrush)
		if ownsIcon {
			win.DestroyIcon(icon)
		}
		return fmt.Errorf("注册客户端窗口失败: %v", win.GetLastError())
	}

	gui := &clientGUI{
		root: root, cwd: cwd, dataDir: dataDir, config: config, icon: icon, ownsIcon: ownsIcon,
		windowBrush: windowBrush, inputBrush: inputBrush, logBrush: logBrush,
	}
	hwnd := win.CreateWindowEx(
		win.WS_EX_CONTROLPARENT,
		className,
		syscall.StringToUTF16Ptr("Codex Link 客户端"),
		win.WS_OVERLAPPEDWINDOW,
		win.CW_USEDEFAULT,
		win.CW_USEDEFAULT,
		600,
		590,
		0,
		0,
		instance,
		unsafe.Pointer(gui),
	)
	if hwnd == 0 {
		gui.releaseBrushes()
		if ownsIcon {
			win.DestroyIcon(icon)
		}
		return fmt.Errorf("创建客户端窗口失败: %v", win.GetLastError())
	}

	win.ShowWindow(hwnd, win.SW_SHOWNORMAL)
	win.UpdateWindow(hwnd)
	// 先进入消息循环，再异步初始化托盘，避免 Shell API 阻塞窗口创建阶段。
	win.PostMessage(hwnd, wmInitializeTray, 0, 0)

	var message win.MSG
	for win.GetMessage(&message, 0, 0, 0) > 0 {
		win.TranslateMessage(&message)
		win.DispatchMessage(&message)
	}
	gui.stopAgent()
	return nil
}

func clientWindowProc(hwnd win.HWND, message uint32, wParam, lParam uintptr) uintptr {
	gui := guiFromWindow(hwnd)
	if message == win.WM_NCCREATE {
		create := (*win.CREATESTRUCT)(unsafe.Pointer(lParam))
		gui = (*clientGUI)(unsafe.Pointer(create.CreateParams))
		gui.hwnd = hwnd
		win.SetWindowLongPtr(hwnd, win.GWLP_USERDATA, uintptr(unsafe.Pointer(gui)))
	}
	if gui == nil {
		return win.DefWindowProc(hwnd, message, wParam, lParam)
	}

	switch message {
	case win.WM_CREATE:
		gui.createControls()
	case win.WM_CTLCOLORSTATIC:
		return gui.controlColor(win.HDC(wParam), win.HWND(lParam), false)
	case win.WM_CTLCOLOREDIT:
		return gui.controlColor(win.HDC(wParam), win.HWND(lParam), true)
	case win.WM_COMMAND:
		gui.handleCommand(win.LOWORD(uint32(wParam)))
	case wmTray:
		gui.handleTrayMessage(uint32(lParam))
	case wmConnectResult:
		gui.handleConnectResult()
	case wmAgentExited:
		gui.handleAgentExited()
	case wmPollState:
		gui.handlePollState()
	case wmTrayError:
		gui.handleTrayError()
	case wmInitializeTray:
		gui.startTrayInitialization()
	case win.WM_TIMER:
		if wParam == guiLogTimer {
			gui.refreshLogView()
		}
	case win.WM_CLOSE:
		if gui.isQuitting() {
			win.DestroyWindow(hwnd)
		} else {
			win.ShowWindow(hwnd, win.SW_HIDE)
		}
	case win.WM_DESTROY:
		win.KillTimer(hwnd, guiLogTimer)
		gui.removeTray()
		gui.releaseIcon()
		gui.releaseBrushes()
		win.PostQuitMessage(0)
	default:
		return win.DefWindowProc(hwnd, message, wParam, lParam)
	}
	return 0
}

func guiFromWindow(hwnd win.HWND) *clientGUI {
	value := win.GetWindowLongPtr(hwnd, win.GWLP_USERDATA)
	if value == 0 {
		return nil
	}
	return (*clientGUI)(unsafe.Pointer(value))
}

func (g *clientGUI) createControls() {
	instance := win.GetModuleHandle(nil)
	font := uintptr(win.GetStockObject(win.DEFAULT_GUI_FONT))
	add := func(className, text string, style, exStyle uint32, id, x, y, width, height int32) win.HWND {
		control := win.CreateWindowEx(
			exStyle,
			syscall.StringToUTF16Ptr(className),
			syscall.StringToUTF16Ptr(text),
			style|win.WS_CHILD|win.WS_VISIBLE,
			x, y, width, height,
			g.hwnd,
			win.HMENU(uintptr(id)),
			instance,
			nil,
		)
		if control != 0 {
			win.SendMessage(control, win.WM_SETFONT, font, 1)
		}
		return control
	}

	g.titleLabel = add("STATIC", "CODEX LINK", win.SS_LEFT, 0, 0, 24, 22, 532, 24)
	g.helperLabel = add("STATIC", "连接本机 Codex 到远程工作区。配置保存后，下次双击即可启动。", win.SS_LEFT, 0, 0, 24, 52, 532, 24)
	add("STATIC", "服务端地址", win.SS_LEFT, 0, 0, 24, 94, 106, 24)
	g.serverEdit = add("EDIT", g.config.ServerURL, win.WS_TABSTOP|win.ES_LEFT|win.ES_AUTOHSCROLL, win.WS_EX_CLIENTEDGE, controlServer, 140, 90, 416, 28)
	add("STATIC", "Token", win.SS_LEFT, 0, 0, 24, 134, 106, 24)
	g.tokenEdit = add("EDIT", g.config.Token, win.WS_TABSTOP|win.ES_LEFT|win.ES_AUTOHSCROLL|win.ES_PASSWORD, win.WS_EX_CLIENTEDGE, controlToken, 140, 130, 416, 28)
	add("STATIC", "设备名称", win.SS_LEFT, 0, 0, 24, 174, 106, 24)
	g.deviceEdit = add("EDIT", firstNonEmptyString(g.config.DeviceName, localDeviceName()), win.WS_TABSTOP|win.ES_LEFT|win.ES_AUTOHSCROLL, win.WS_EX_CLIENTEDGE, controlDevice, 140, 170, 416, 28)
	add("STATIC", "状态", win.SS_LEFT, 0, 0, 24, 216, 106, 24)
	g.statusLabel = add("STATIC", "未连接", win.SS_LEFT, 0, 0, 140, 216, 416, 24)
	g.detailLabel = add("STATIC", "填写服务端地址和 Token 后点击连接。", win.SS_LEFT, 0, 0, 24, 250, 532, 28)
	g.logLabel = add("STATIC", "运行日志（自动滚动）", win.SS_LEFT, 0, 0, 24, 294, 532, 22)
	g.logEdit = add("EDIT", "等待后台运行日志...", win.WS_TABSTOP|win.WS_VSCROLL|win.ES_LEFT|win.ES_MULTILINE|win.ES_AUTOVSCROLL|win.ES_READONLY|win.ES_NOHIDESEL, win.WS_EX_CLIENTEDGE, 0, 24, 320, 532, 170)
	g.connectButton = add("BUTTON", "连接并启动", win.BS_DEFPUSHBUTTON|win.WS_TABSTOP, 0, controlConnect, 336, 514, 116, 32)
	add("BUTTON", "隐藏到托盘", win.BS_PUSHBUTTON|win.WS_TABSTOP, 0, controlHide, 460, 514, 96, 32)
	g.refreshLogView()
	win.SetTimer(g.hwnd, guiLogTimer, guiLogTimerPeriod, 0)
}

func createSolidBrush(color win.COLORREF) win.HBRUSH {
	return win.CreateBrushIndirect(&win.LOGBRUSH{LbStyle: win.BS_SOLID, LbColor: color})
}

func deleteBrush(brush win.HBRUSH) {
	if brush != 0 {
		win.DeleteObject(win.HGDIOBJ(brush))
	}
}

func (g *clientGUI) releaseBrushes() {
	deleteBrush(g.windowBrush)
	deleteBrush(g.inputBrush)
	deleteBrush(g.logBrush)
	g.windowBrush = 0
	g.inputBrush = 0
	g.logBrush = 0
}

func (g *clientGUI) controlColor(hdc win.HDC, control win.HWND, edit bool) uintptr {
	if edit {
		if control == g.logEdit {
			win.SetTextColor(hdc, guiLogTextColor)
			win.SetBkColor(hdc, guiLogColor)
			return uintptr(g.logBrush)
		}
		win.SetTextColor(hdc, guiInkColor)
		win.SetBkColor(hdc, guiInputColor)
		return uintptr(g.inputBrush)
	}

	color := guiMutedColor
	switch control {
	case g.titleLabel:
		color = guiInkColor
	case g.statusLabel, g.logLabel:
		color = guiAccentColor
	case g.detailLabel, g.helperLabel:
		color = guiMutedColor
	}
	win.SetTextColor(hdc, color)
	win.SetBkColor(hdc, guiWindowColor)
	win.SetBkMode(hdc, win.TRANSPARENT)
	return uintptr(g.windowBrush)
}

func (g *clientGUI) refreshLogView() {
	if g.logEdit == 0 {
		return
	}
	text, err := readAgentGUILogTail(agentGUILogPath(g.dataDir), guiLogViewLimit)
	if err != nil {
		if os.IsNotExist(err) {
			text = "等待后台运行日志..."
		} else {
			text = "读取运行日志失败: " + err.Error()
		}
	}
	if text == "" {
		text = "等待后台运行日志..."
	}
	if text == g.lastLogText {
		return
	}
	g.lastLogText = text
	setWindowText(g.logEdit, text)
	end := win.SendMessage(g.logEdit, win.WM_GETTEXTLENGTH, 0, 0)
	win.SendMessage(g.logEdit, win.EM_SETSEL, end, end)
	win.SendMessage(g.logEdit, win.EM_SCROLLCARET, 0, 0)
}

func (g *clientGUI) resetLog(message string) {
	path := agentGUILogPath(g.dataDir)
	if err := os.MkdirAll(g.dataDir, 0o700); err != nil {
		return
	}
	if err := os.WriteFile(path, []byte(formatGUILogLine(message)), 0o600); err == nil {
		g.lastLogText = ""
		g.refreshLogView()
	}
}

func (g *clientGUI) appendLog(message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	file, err := os.OpenFile(agentGUILogPath(g.dataDir), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = file.WriteString(formatGUILogLine(message))
	_ = file.Close()
	g.refreshLogView()
}

func formatGUILogLine(message string) string {
	return time.Now().Format("2006/01/02 15:04:05") + " [界面] " + redactSensitiveText(strings.TrimSpace(message)) + "\r\n"
}

func (g *clientGUI) startTrayInitialization() {
	g.trayMu.Lock()
	if g.trayInitializing || g.trayReady {
		g.trayMu.Unlock()
		return
	}
	g.trayInitializing = true
	g.trayError = ""
	g.trayInitDone = make(chan struct{})
	icon := g.icon
	g.trayMu.Unlock()
	go g.initializeTray(icon)
}

func (g *clientGUI) initializeTray(icon win.HICON) {
	tray := win.NOTIFYICONDATA{
		CbSize:           uint32(unsafe.Sizeof(win.NOTIFYICONDATA{})),
		HWnd:             g.hwnd,
		UID:              1,
		UFlags:           win.NIF_MESSAGE | win.NIF_ICON | win.NIF_TIP,
		UCallbackMessage: wmTray,
		HIcon:            icon,
	}
	copy(tray.SzTip[:], syscall.StringToUTF16("Codex Link 客户端"))
	added := win.Shell_NotifyIcon(win.NIM_ADD, &tray)
	if added {
		tray.UVersion = win.NOTIFYICON_VERSION
		win.Shell_NotifyIcon(win.NIM_SETVERSION, &tray)
	}
	quitting := g.isQuitting()
	g.trayMu.Lock()
	done := g.trayInitDone
	g.trayInitializing = false
	if added && !quitting {
		g.tray = tray
		g.trayReady = true
	} else if !added && !quitting {
		g.trayError = fmt.Sprintf("创建系统托盘图标失败: %v", win.GetLastError())
	}
	if done != nil {
		close(done)
		g.trayInitDone = nil
	}
	g.trayMu.Unlock()
	if added && quitting {
		win.Shell_NotifyIcon(win.NIM_DELETE, &tray)
	}
	if !added && !quitting {
		win.PostMessage(g.hwnd, wmTrayError, 0, 0)
	}
}

func (g *clientGUI) removeTray() {
	g.trayMu.Lock()
	tray := g.tray
	ready := g.trayReady
	g.trayReady = false
	g.trayMu.Unlock()
	if ready {
		win.Shell_NotifyIcon(win.NIM_DELETE, &tray)
	}
}

func (g *clientGUI) releaseIcon() {
	g.trayMu.Lock()
	icon := g.icon
	ownsIcon := g.ownsIcon
	done := g.trayInitDone
	initializing := g.trayInitializing
	g.icon = 0
	g.ownsIcon = false
	g.trayMu.Unlock()
	if !ownsIcon || icon == 0 {
		return
	}
	destroy := func() { win.DestroyIcon(icon) }
	if initializing && done != nil {
		go func() {
			<-done
			destroy()
		}()
		return
	}
	destroy()
}

func (g *clientGUI) handleCommand(id uint16) {
	switch id {
	case controlConnect:
		g.connect()
	case controlHide:
		win.ShowWindow(g.hwnd, win.SW_HIDE)
	case trayShow:
		g.showWindow()
	case trayExit:
		g.exit()
	}
}

func (g *clientGUI) handleTrayMessage(message uint32) {
	switch message {
	case win.WM_LBUTTONUP, win.WM_LBUTTONDBLCLK:
		g.showWindow()
	case win.WM_RBUTTONUP:
		g.showTrayMenu()
	}
}

func (g *clientGUI) showWindow() {
	win.ShowWindow(g.hwnd, win.SW_SHOWNORMAL)
	win.SetForegroundWindow(g.hwnd)
	win.SetFocus(g.serverEdit)
}

func (g *clientGUI) showTrayMenu() {
	menu := win.CreatePopupMenu()
	if menu == 0 {
		return
	}
	defer win.DestroyMenu(menu)
	insertTrayMenuItem(menu, trayShow, "显示窗口")
	insertTrayMenuItem(menu, trayExit, "退出客户端")
	var point win.POINT
	if !win.GetCursorPos(&point) {
		return
	}
	win.SetForegroundWindow(g.hwnd)
	command := win.TrackPopupMenu(menu, win.TPM_RIGHTBUTTON|win.TPM_RETURNCMD, point.X, point.Y, 0, g.hwnd, nil)
	switch command {
	case trayShow:
		g.showWindow()
	case trayExit:
		g.exit()
	}
}

func insertTrayMenuItem(menu win.HMENU, id uint32, text string) {
	value := syscall.StringToUTF16(text)
	item := win.MENUITEMINFO{
		CbSize:     uint32(unsafe.Sizeof(win.MENUITEMINFO{})),
		FMask:      win.MIIM_ID | win.MIIM_STRING,
		WID:        id,
		DwTypeData: &value[0],
		Cch:        uint32(len(value) - 1),
	}
	position := win.GetMenuItemCount(menu)
	if position < 0 {
		position = 0
	}
	win.InsertMenuItem(menu, uint32(position), true, &item)
}

func (g *clientGUI) connect() {
	g.mu.Lock()
	running := g.process != nil
	starting := g.starting
	g.mu.Unlock()
	if running {
		g.stopAgent()
		setWindowText(g.statusLabel, "客户端已停止")
		setWindowText(g.detailLabel, "后台客户端已停止。")
		g.appendLog("已停止后台客户端。")
		g.updateConnectButton()
		return
	}
	if starting {
		return
	}

	serverURL := strings.TrimSpace(readWindowText(g.serverEdit))
	token := strings.TrimSpace(readWindowText(g.tokenEdit))
	deviceName := strings.TrimSpace(readWindowText(g.deviceEdit))
	if serverURL == "" || token == "" {
		g.showError("请填写服务端地址和 Token。")
		return
	}
	g.mu.Lock()
	if g.quitting || g.process != nil || g.starting {
		g.mu.Unlock()
		return
	}
	g.starting = true
	g.mu.Unlock()

	g.resetLog("开始连接服务端。")
	g.setBusy(true)
	setWindowText(g.statusLabel, "正在登录")
	setWindowText(g.detailLabel, "正在向服务端登记设备，请稍候。")
	go func() {
		config, err := loginRemoteAgentConfig(g.dataDir, serverURL, token, deviceName)
		if err == nil {
			err = g.startAgent(config)
		}
		g.mu.Lock()
		if !g.quitting {
			g.connectResult = &guiConnectResult{config: config, err: err}
		}
		hwnd := g.hwnd
		quitting := g.quitting
		g.mu.Unlock()
		if !quitting {
			win.PostMessage(hwnd, wmConnectResult, 0, 0)
		}
	}()
}

func (g *clientGUI) startAgent(config remoteAgentConfig) error {
	g.mu.Lock()
	quitting := g.quitting
	g.mu.Unlock()
	if quitting {
		return errors.New("客户端正在退出")
	}

	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.Command(executable, "agent")
	command.Dir = g.root
	command.Env = append(os.Environ(), "DATA_DIR="+g.dataDir, "CODEX_CWD="+g.cwd)
	logFile, err := openAgentGUILog(g.dataDir)
	if err != nil {
		return err
	}
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return err
	}

	g.mu.Lock()
	if g.quitting {
		g.mu.Unlock()
		_ = command.Process.Kill()
		_ = logFile.Close()
		return errors.New("客户端正在退出")
	}
	g.config = config
	g.process = command
	g.pollStop = make(chan struct{})
	g.lastLogState = ""
	pollStop := g.pollStop
	g.mu.Unlock()
	go g.waitForAgent(command)
	go g.pollAgent(config, command, pollStop)
	return nil
}

func (g *clientGUI) waitForAgent(command *exec.Cmd) {
	err := command.Wait()
	if logFile, ok := command.Stdout.(*os.File); ok {
		_ = logFile.Close()
	}
	g.mu.Lock()
	if g.process != command {
		g.mu.Unlock()
		return
	}
	g.process = nil
	stop := g.pollStop
	g.pollStop = nil
	quitting := g.quitting
	if err != nil {
		g.agentExitErr = agentExitDetail(err, filepath.Join(g.dataDir, "agent-gui.log"))
	} else {
		g.agentExitErr = "后台客户端已停止。"
	}
	hwnd := g.hwnd
	g.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if !quitting {
		win.PostMessage(hwnd, wmAgentExited, 0, 0)
	}
}

func (g *clientGUI) pollAgent(config remoteAgentConfig, command *exec.Cmd, stop <-chan struct{}) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			err := validateRemoteAgentConfig(config)
			state := "waiting"
			if err == nil {
				state = "connected"
			} else if err == errAgentAuth {
				state = "auth"
			}
			g.mu.Lock()
			active := !g.quitting && g.process == command
			if active {
				g.pollState = state
			}
			hwnd := g.hwnd
			g.mu.Unlock()
			if active {
				win.PostMessage(hwnd, wmPollState, 0, 0)
			}
		}
	}
}

func (g *clientGUI) handleConnectResult() {
	g.mu.Lock()
	result := g.connectResult
	g.connectResult = nil
	g.starting = false
	g.mu.Unlock()
	if result == nil || g.isQuitting() {
		return
	}
	g.setBusy(false)
	if result.err != nil {
		g.updateConnectButton()
		setWindowText(g.statusLabel, "连接失败")
		setWindowText(g.detailLabel, result.err.Error())
		g.appendLog("连接失败: " + result.err.Error())
		messageBox(g.hwnd, result.err.Error(), "Codex Link", win.MB_ICONERROR|win.MB_OK)
		return
	}
	setWindowText(g.statusLabel, "客户端已启动")
	setWindowText(g.detailLabel, "后台连接已启动，关闭窗口会隐藏到系统托盘。")
	g.appendLog("后台客户端已启动，等待服务端连接。")
	g.updateConnectButton()
}

func (g *clientGUI) handleAgentExited() {
	g.mu.Lock()
	detail := g.agentExitErr
	g.agentExitErr = ""
	g.mu.Unlock()
	if g.isQuitting() {
		return
	}
	g.updateConnectButton()
	setWindowText(g.statusLabel, "客户端已停止")
	setWindowText(g.detailLabel, detail)
	g.appendLog("后台客户端已停止。")
	if strings.HasPrefix(detail, "后台客户端异常退出:") {
		messageBox(g.hwnd, detail, "Codex Link 客户端启动失败", win.MB_ICONERROR|win.MB_OK)
	}
}

func (g *clientGUI) handlePollState() {
	g.mu.Lock()
	state := g.pollState
	g.pollState = ""
	g.mu.Unlock()
	if g.isQuitting() || state == "" {
		return
	}
	switch state {
	case "connected":
		setWindowText(g.statusLabel, "已连接")
		setWindowText(g.detailLabel, "客户端正在后台运行，网页可以连接此设备。")
		g.logPollState(state, "已连接到服务端，网页可以连接此设备。")
	case "auth":
		setWindowText(g.statusLabel, "Token 已失效")
		setWindowText(g.detailLabel, "请更新 Token 后重新连接。")
		g.logPollState(state, "Token 已失效，请更新 Token 后重新连接。")
	default:
		setWindowText(g.statusLabel, "等待服务端")
		setWindowText(g.detailLabel, "服务端暂时不可用，客户端会自动重试。")
		g.logPollState(state, "服务端暂时不可用，客户端会自动重试。")
	}
}

func (g *clientGUI) logPollState(state, message string) {
	if state == g.lastLogState {
		return
	}
	g.lastLogState = state
	g.appendLog(message)
}

func (g *clientGUI) handleTrayError() {
	g.trayMu.Lock()
	detail := g.trayError
	g.trayError = ""
	g.trayMu.Unlock()
	if detail == "" || g.isQuitting() {
		return
	}
	setWindowText(g.statusLabel, "托盘不可用")
	setWindowText(g.detailLabel, detail+" 客户端仍可正常使用，请稍后重试。")
	g.appendLog(detail)
}

func (g *clientGUI) stopAgent() {
	g.mu.Lock()
	command := g.process
	stop := g.pollStop
	g.process = nil
	g.pollStop = nil
	g.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if command != nil && command.Process != nil {
		_ = command.Process.Kill()
	}
}

func (g *clientGUI) setBusy(busy bool) {
	enabled := !busy
	win.EnableWindow(g.serverEdit, enabled)
	win.EnableWindow(g.tokenEdit, enabled)
	win.EnableWindow(g.deviceEdit, enabled)
	win.EnableWindow(g.connectButton, enabled)
	if busy {
		setWindowText(g.connectButton, "正在连接...")
	} else {
		g.updateConnectButton()
	}
}

func (g *clientGUI) updateConnectButton() {
	if g.isRunning() {
		setWindowText(g.connectButton, "停止客户端")
	} else {
		setWindowText(g.connectButton, "连接并启动")
	}
}

func (g *clientGUI) showError(message string) {
	setWindowText(g.statusLabel, "配置不完整")
	setWindowText(g.detailLabel, message)
	g.appendLog(message)
	messageBox(g.hwnd, message, "Codex Link", win.MB_ICONWARNING|win.MB_OK)
}

func (g *clientGUI) exit() {
	g.mu.Lock()
	if g.quitting {
		g.mu.Unlock()
		return
	}
	g.quitting = true
	g.mu.Unlock()
	win.DestroyWindow(g.hwnd)
}

func (g *clientGUI) isRunning() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.process != nil
}

func (g *clientGUI) isQuitting() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.quitting
}

func readWindowText(hwnd win.HWND) string {
	if hwnd == 0 {
		return ""
	}
	length := int(win.SendMessage(hwnd, win.WM_GETTEXTLENGTH, 0, 0))
	if length == 0 {
		return ""
	}
	buffer := make([]uint16, length+1)
	win.SendMessage(hwnd, win.WM_GETTEXT, uintptr(len(buffer)), uintptr(unsafe.Pointer(&buffer[0])))
	return syscall.UTF16ToString(buffer)
}

func setWindowText(hwnd win.HWND, text string) {
	if hwnd != 0 {
		value := syscall.StringToUTF16Ptr(text)
		win.SendMessage(hwnd, win.WM_SETTEXT, 0, uintptr(unsafe.Pointer(value)))
	}
}

func messageBox(hwnd win.HWND, message, title string, flags uint32) {
	win.MessageBox(hwnd, syscall.StringToUTF16Ptr(message), syscall.StringToUTF16Ptr(title), flags)
}

func openAgentGUILog(dataDir string) (*os.File, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建客户端数据目录失败: %w", err)
	}
	path := agentGUILogPath(dataDir)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("创建客户端诊断日志失败: %w", err)
	}
	return file, nil
}

func agentExitDetail(exitError error, logPath string) string {
	detail := fmt.Sprintf("后台客户端异常退出: %v", exitError)
	logText, err := readAgentGUILogTail(logPath, 3000)
	if err != nil {
		return detail
	}
	if logText == "" {
		return detail
	}
	return detail + "\r\n\r\n诊断日志:\r\n" + logText
}

func createClientIcon() (win.HICON, bool) {
	const size = 64
	pixels := make([]uint32, size*size)
	fillRoundedIconRect(pixels, size, 3, 3, 60, 60, 16, iconColor(18, 37, 55))
	strokeIconRing(pixels, size, 25, 25, 12, 5, iconColor(113, 223, 210))
	strokeIconRing(pixels, size, 39, 39, 12, 5, iconColor(217, 95, 82))
	strokeIconLine(pixels, size, 23, 32, 41, 32, 3, iconColor(247, 251, 255))
	fillIconCircle(pixels, size, 32, 32, 4, iconColor(247, 251, 255))

	header := win.BITMAPINFOHEADER{
		BiSize:        uint32(unsafe.Sizeof(win.BITMAPINFOHEADER{})),
		BiWidth:       size,
		BiHeight:      -size,
		BiPlanes:      1,
		BiBitCount:    32,
		BiCompression: win.BI_RGB,
	}
	var bits unsafe.Pointer
	bitmap := win.CreateDIBSection(0, &header, win.DIB_RGB_COLORS, &bits, 0, 0)
	if bitmap == 0 || bits == nil {
		return win.LoadIcon(0, win.MAKEINTRESOURCE(win.IDI_APPLICATION)), false
	}
	copy(unsafe.Slice((*uint32)(bits), len(pixels)), pixels)
	mask := win.CreateBitmap(size, size, 1, 1, nil)
	if mask == 0 {
		win.DeleteObject(win.HGDIOBJ(bitmap))
		return win.LoadIcon(0, win.MAKEINTRESOURCE(win.IDI_APPLICATION)), false
	}
	icon := win.CreateIconIndirect(&win.ICONINFO{FIcon: win.TRUE, HbmColor: bitmap, HbmMask: mask})
	win.DeleteObject(win.HGDIOBJ(bitmap))
	win.DeleteObject(win.HGDIOBJ(mask))
	if icon == 0 {
		return win.LoadIcon(0, win.MAKEINTRESOURCE(win.IDI_APPLICATION)), false
	}
	return icon, true
}

func iconColor(red, green, blue byte) uint32 {
	return uint32(0xff)<<24 | uint32(red)<<16 | uint32(green)<<8 | uint32(blue)
}

func fillRoundedIconRect(pixels []uint32, size, left, top, right, bottom, radius int, color uint32) {
	for y := top; y <= bottom; y++ {
		for x := left; x <= right; x++ {
			nearX := minIcon(maxIcon(x, left+radius), right-radius)
			nearY := minIcon(maxIcon(y, top+radius), bottom-radius)
			if (x-nearX)*(x-nearX)+(y-nearY)*(y-nearY) <= radius*radius {
				pixels[y*size+x] = color
			}
		}
	}
}

func strokeIconRing(pixels []uint32, size, centerX, centerY, radius, width int, color uint32) {
	outer := radius * radius
	inner := (radius - width) * (radius - width)
	for y := centerY - radius; y <= centerY+radius; y++ {
		for x := centerX - radius; x <= centerX+radius; x++ {
			if x < 0 || y < 0 || x >= size || y >= size {
				continue
			}
			distance := (x-centerX)*(x-centerX) + (y-centerY)*(y-centerY)
			if distance <= outer && distance >= inner {
				pixels[y*size+x] = color
			}
		}
	}
}

func strokeIconLine(pixels []uint32, size, fromX, fromY, toX, toY, width int, color uint32) {
	deltaX, deltaY := float64(toX-fromX), float64(toY-fromY)
	lengthSquared := deltaX*deltaX + deltaY*deltaY
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			position := ((float64(x-fromX)*deltaX + float64(y-fromY)*deltaY) / lengthSquared)
			position = math.Max(0, math.Min(1, position))
			distance := math.Hypot(float64(x-fromX)-position*deltaX, float64(y-fromY)-position*deltaY)
			if distance <= float64(width) {
				pixels[y*size+x] = color
			}
		}
	}
}

func fillIconCircle(pixels []uint32, size, centerX, centerY, radius int, color uint32) {
	for y := centerY - radius; y <= centerY+radius; y++ {
		for x := centerX - radius; x <= centerX+radius; x++ {
			if x >= 0 && y >= 0 && x < size && y < size && (x-centerX)*(x-centerX)+(y-centerY)*(y-centerY) <= radius*radius {
				pixels[y*size+x] = color
			}
		}
	}
}

func minIcon(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxIcon(left, right int) int {
	if left > right {
		return left
	}
	return right
}

//go:build windows

package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
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
)

type clientGUI struct {
	hwnd          win.HWND
	serverEdit    win.HWND
	tokenEdit     win.HWND
	deviceEdit    win.HWND
	statusLabel   win.HWND
	detailLabel   win.HWND
	connectButton win.HWND

	root    string
	cwd     string
	dataDir string

	mu       sync.Mutex
	config   remoteAgentConfig
	process  *exec.Cmd
	pollStop chan struct{}
	quitting bool

	tray      win.NOTIFYICONDATA
	trayReady bool
	icon      win.HICON
	ownsIcon  bool

	connectResult *guiConnectResult
	agentExitErr  string
	pollState     string
}

type guiConnectResult struct {
	config remoteAgentConfig
	err    error
}

func runClientGUI(root, cwd, dataDir string) error {
	config, _ := loadRemoteAgentConfig(dataDir)
	instance := win.GetModuleHandle(nil)
	icon, ownsIcon := createClientIcon()
	className := syscall.StringToUTF16Ptr(clientWindowClass)
	windowClass := win.WNDCLASSEX{
		CbSize:        uint32(unsafe.Sizeof(win.WNDCLASSEX{})),
		LpfnWndProc:   syscall.NewCallback(clientWindowProc),
		HInstance:     instance,
		HIcon:         icon,
		HIconSm:       icon,
		HCursor:       win.LoadCursor(0, win.MAKEINTRESOURCE(win.IDC_ARROW)),
		HbrBackground: win.GetSysColorBrush(win.COLOR_WINDOW),
		LpszClassName: className,
	}
	if win.RegisterClassEx(&windowClass) == 0 {
		return fmt.Errorf("注册客户端窗口失败: %v", win.GetLastError())
	}

	gui := &clientGUI{root: root, cwd: cwd, dataDir: dataDir, config: config, icon: icon, ownsIcon: ownsIcon}
	hwnd := win.CreateWindowEx(
		win.WS_EX_CONTROLPARENT,
		className,
		syscall.StringToUTF16Ptr("Codex Link 客户端"),
		win.WS_OVERLAPPEDWINDOW,
		win.CW_USEDEFAULT,
		win.CW_USEDEFAULT,
		560,
		410,
		0,
		0,
		instance,
		unsafe.Pointer(gui),
	)
	if hwnd == 0 {
		if ownsIcon {
			win.DestroyIcon(icon)
		}
		return fmt.Errorf("创建客户端窗口失败: %v", win.GetLastError())
	}

	if err := gui.createTray(); err != nil {
		win.DestroyWindow(hwnd)
		return err
	}
	win.ShowWindow(hwnd, win.SW_SHOWNORMAL)
	win.UpdateWindow(hwnd)

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
	case win.WM_CLOSE:
		if gui.isQuitting() {
			win.DestroyWindow(hwnd)
		} else {
			win.ShowWindow(hwnd, win.SW_HIDE)
		}
	case win.WM_DESTROY:
		gui.removeTray()
		gui.releaseIcon()
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

	add("STATIC", "CODEX LINK", win.SS_LEFT, 0, 0, 24, 18, 500, 28)
	add("STATIC", "连接本机 Codex 到远程工作区。配置保存后，下次双击即可启动。", win.SS_LEFT, 0, 0, 24, 52, 500, 26)
	add("STATIC", "服务端地址", win.SS_LEFT, 0, 0, 24, 98, 100, 24)
	g.serverEdit = add("EDIT", g.config.ServerURL, win.WS_TABSTOP|win.ES_LEFT|win.ES_AUTOHSCROLL, win.WS_EX_CLIENTEDGE, controlServer, 140, 94, 390, 26)
	add("STATIC", "Token", win.SS_LEFT, 0, 0, 24, 138, 100, 24)
	g.tokenEdit = add("EDIT", g.config.Token, win.WS_TABSTOP|win.ES_LEFT|win.ES_AUTOHSCROLL|win.ES_PASSWORD, win.WS_EX_CLIENTEDGE, controlToken, 140, 134, 390, 26)
	add("STATIC", "设备名称", win.SS_LEFT, 0, 0, 24, 178, 100, 24)
	g.deviceEdit = add("EDIT", firstNonEmptyString(g.config.DeviceName, localDeviceName()), win.WS_TABSTOP|win.ES_LEFT|win.ES_AUTOHSCROLL, win.WS_EX_CLIENTEDGE, controlDevice, 140, 174, 390, 26)
	add("STATIC", "状态", win.SS_LEFT, 0, 0, 24, 222, 100, 24)
	g.statusLabel = add("STATIC", "未连接", win.SS_LEFT, 0, 0, 140, 222, 390, 24)
	g.detailLabel = add("STATIC", "填写服务端地址和 Token 后点击连接。", win.SS_LEFT, 0, 0, 24, 258, 506, 42)
	g.connectButton = add("BUTTON", "连接并启动", win.BS_DEFPUSHBUTTON|win.WS_TABSTOP, 0, controlConnect, 310, 320, 112, 30)
	add("BUTTON", "隐藏到托盘", win.BS_PUSHBUTTON|win.WS_TABSTOP, 0, controlHide, 430, 320, 100, 30)
}

func (g *clientGUI) createTray() error {
	g.tray = win.NOTIFYICONDATA{
		CbSize:           uint32(unsafe.Sizeof(win.NOTIFYICONDATA{})),
		HWnd:             g.hwnd,
		UID:              1,
		UFlags:           win.NIF_MESSAGE | win.NIF_ICON | win.NIF_TIP,
		UCallbackMessage: wmTray,
		HIcon:            g.icon,
	}
	copy(g.tray.SzTip[:], syscall.StringToUTF16("Codex Link 客户端"))
	if !win.Shell_NotifyIcon(win.NIM_ADD, &g.tray) {
		return fmt.Errorf("创建系统托盘图标失败: %v", win.GetLastError())
	}
	g.tray.UVersion = win.NOTIFYICON_VERSION
	win.Shell_NotifyIcon(win.NIM_SETVERSION, &g.tray)
	g.trayReady = true
	return nil
}

func (g *clientGUI) removeTray() {
	if !g.trayReady {
		return
	}
	win.Shell_NotifyIcon(win.NIM_DELETE, &g.tray)
	g.trayReady = false
}

func (g *clientGUI) releaseIcon() {
	if !g.ownsIcon || g.icon == 0 {
		return
	}
	win.DestroyIcon(g.icon)
	g.icon = 0
	g.ownsIcon = false
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
	if g.isRunning() {
		g.stopAgent()
		setWindowText(g.statusLabel, "客户端已停止")
		setWindowText(g.detailLabel, "后台客户端已停止。")
		g.updateConnectButton()
		return
	}

	serverURL := strings.TrimSpace(readWindowText(g.serverEdit))
	token := strings.TrimSpace(readWindowText(g.tokenEdit))
	deviceName := strings.TrimSpace(readWindowText(g.deviceEdit))
	if serverURL == "" || token == "" {
		g.showError("请填写服务端地址和 Token。")
		return
	}

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
	g.mu.Unlock()
	if result == nil || g.isQuitting() {
		return
	}
	g.setBusy(false)
	if result.err != nil {
		g.updateConnectButton()
		setWindowText(g.statusLabel, "连接失败")
		setWindowText(g.detailLabel, result.err.Error())
		messageBox(g.hwnd, result.err.Error(), "Codex Link", win.MB_ICONERROR|win.MB_OK)
		return
	}
	setWindowText(g.statusLabel, "客户端已启动")
	setWindowText(g.detailLabel, "后台连接已启动，关闭窗口会隐藏到系统托盘。")
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
	case "auth":
		setWindowText(g.statusLabel, "Token 已失效")
		setWindowText(g.detailLabel, "请更新 Token 后重新连接。")
	default:
		setWindowText(g.statusLabel, "等待服务端")
		setWindowText(g.detailLabel, "服务端暂时不可用，客户端会自动重试。")
	}
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
	path := filepath.Join(dataDir, "agent-gui.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("创建客户端诊断日志失败: %w", err)
	}
	return file, nil
}

func agentExitDetail(exitError error, logPath string) string {
	detail := fmt.Sprintf("后台客户端异常退出: %v", exitError)
	raw, err := os.ReadFile(logPath)
	if err != nil {
		return detail
	}
	logText := strings.TrimSpace(string(raw))
	if len(logText) > 3000 {
		logText = logText[len(logText)-3000:]
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

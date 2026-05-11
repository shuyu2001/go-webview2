//go:build windows
// +build windows

package webview2

import (
	"encoding/json"
	"errors"
	"log"
	"reflect"
	"strconv"
	"sync"
	"unsafe"

	"github.com/jchv/go-webview2/internal/w32"
	"github.com/jchv/go-webview2/pkg/edge"

	"golang.org/x/sys/windows"
)

// ----------------------------------------------------------------------------
// Win32 API 补充常量
// ----------------------------------------------------------------------------

const (
	swHide     = 0
	swMaximize = 3
	swShow     = 5
	swMinimize = 6
	swRestore  = 9

	hwndTopmost uintptr = ^uintptr(0) // -1

	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010
	swpFrameChanged = 0x0020

	wmSetIcon      = 0x0080
	iconSmall      = 0
	iconBig        = 1
	imageIcon      = 1
	lrLoadFromFile = 0x0010
)

// ----------------------------------------------------------------------------
// 窗口上下文管理
// ----------------------------------------------------------------------------

var (
	windowContext     = make(map[uintptr]interface{})
	windowContextSync sync.RWMutex
)

func getWindowContext(wnd uintptr) interface{} {
	windowContextSync.RLock()
	defer windowContextSync.RUnlock()
	return windowContext[wnd]
}

func setWindowContext(wnd uintptr, data interface{}) {
	windowContextSync.Lock()
	defer windowContextSync.Unlock()
	windowContext[wnd] = data
}

func deleteWindowContext(wnd uintptr) {
	windowContextSync.Lock()
	defer windowContextSync.Unlock()
	delete(windowContext, wnd)
}

// ----------------------------------------------------------------------------
// 接口定义
// ----------------------------------------------------------------------------

type browser interface {
	Embed(hwnd uintptr) bool
	Resize()
	Navigate(url string)
	NavigateToString(htmlContent string)
	Init(script string)
	Eval(script string)
	NotifyParentWindowPositionChanged() error
	Focus()
}

// WebViewOptions 提供现代化窗口和 Webview 配置
type WebViewOptions struct {
	Title  string
	Width  int
	Height int

	Center          bool // 是否居中屏幕
	StartMaximized  bool // 启动时是否最大化
	AlwaysOnTop     bool // 是否窗口置顶
	DisableResize   bool // 是否禁用窗口改变大小
	DisableMaximize bool // 是否单独禁用最大化按钮

	// 必须外部传入的核心实例
	Chromium *edge.Chromium

	Debug     bool // 开启右键菜单和 F12 控制台
	AutoFocus bool // 窗口激活时自动聚焦 WebView
}

// ----------------------------------------------------------------------------
// webview 核心结构
// ----------------------------------------------------------------------------

type webview struct {
	hwnd       uintptr
	mainthread uintptr
	browser    browser
	autofocus  bool
	maxsz      w32.Point
	minsz      w32.Point
	mu         sync.Mutex
	bindings   map[string]interface{}
	dispatchq  []func()
}

// NewWithOptions 创建一个新的 WebView 实例
func NewWithOptions(opts WebViewOptions) WebView {
	if opts.Chromium == nil {
		log.Fatal("Chromium instance must be provided via WebViewOptions.Chromium")
		return nil
	}

	w := &webview{
		bindings:  make(map[string]interface{}),
		autofocus: opts.AutoFocus,
	}

	opts.Chromium.MessageCallback = w.msgcb
	opts.Chromium.SetPermission(
		edge.CoreWebView2PermissionKindClipboardRead,
		edge.CoreWebView2PermissionStateAllow,
	)
	w.browser = opts.Chromium

	w.mainthread, _, _ = w32.Kernel32GetCurrentThreadID.Call()

	if !w.createWindow(opts) {
		return nil
	}

	if settings, err := opts.Chromium.GetSettings(); err == nil {
		_ = settings.PutAreDefaultContextMenusEnabled(opts.Debug)
		_ = settings.PutAreDevToolsEnabled(opts.Debug)
	}

	return w
}

// ----------------------------------------------------------------------------
// 窗口创建
// ----------------------------------------------------------------------------

func (w *webview) createWindow(opts WebViewOptions) bool {
	var hinstance windows.Handle
	if err := windows.GetModuleHandleEx(0, nil, &hinstance); err != nil {
		log.Printf("GetModuleHandleEx failed: %v", err)
	}

	// 加载默认图标
	icow, _, _ := w32.User32GetSystemMetrics.Call(w32.SystemMetricsCxIcon)
	icoh, _, _ := w32.User32GetSystemMetrics.Call(w32.SystemMetricsCyIcon)
	icon, _, _ := w32.User32LoadImageW.Call(
		uintptr(hinstance), 32512,
		icow, icoh, 0,
	)

	className, err := windows.UTF16PtrFromString("webview")
	if err != nil {
		log.Printf("UTF16PtrFromString(className) failed: %v", err)
		return false
	}

	wc := w32.WndClassExW{
		CbSize:        uint32(unsafe.Sizeof(w32.WndClassExW{})),
		HInstance:     hinstance,
		LpszClassName: className,
		HIcon:         windows.Handle(icon),
		HIconSm:       windows.Handle(icon),
		LpfnWndProc:   windows.NewCallback(wndproc),
	}
	w32.User32RegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	// 窗口尺寸默认值
	windowWidth := opts.Width
	if windowWidth <= 0 {
		windowWidth = 800
	}
	windowHeight := opts.Height
	if windowHeight <= 0 {
		windowHeight = 600
	}

	// 计算窗口位置
	posX, posY := int(w32.CW_USEDEFAULT), int(w32.CW_USEDEFAULT)
	if opts.Center {
		screenW, _, _ := w32.User32GetSystemMetrics.Call(w32.SM_CXSCREEN)
		screenH, _, _ := w32.User32GetSystemMetrics.Call(w32.SM_CYSCREEN)
		posX = (int(screenW) - windowWidth) / 2
		posY = (int(screenH) - windowHeight) / 2
		if posX < 0 {
			posX = 0
		}
		if posY < 0 {
			posY = 0
		}
	}

	// 构建窗口样式
	style := uint32(0xCF0000) // WS_OVERLAPPEDWINDOW
	if opts.DisableResize {
		style &^= (w32.WSThickFrame | w32.WSMaximizeBox)
	} else if opts.DisableMaximize {
		style &^= w32.WSMaximizeBox
	}

	windowName, err := windows.UTF16PtrFromString(opts.Title)
	if err != nil {
		log.Printf("UTF16PtrFromString(title) failed: %v", err)
		return false
	}

	w.hwnd, _, _ = w32.User32CreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		uintptr(style),
		uintptr(posX), uintptr(posY),
		uintptr(windowWidth), uintptr(windowHeight),
		0, 0, uintptr(hinstance), 0,
	)
	if w.hwnd == 0 {
		log.Println("CreateWindowExW failed: hwnd is 0")
		return false
	}

	setWindowContext(w.hwnd, w)

	// 窗口置顶
	if opts.AlwaysOnTop {
		w32.User32SetWindowPos.Call(
			w.hwnd, hwndTopmost,
			0, 0, 0, 0,
			swpNoMove|swpNoSize|swpNoActivate,
		)
	}

	// 显示窗口
	showMode := uintptr(swShow)
	if opts.StartMaximized {
		showMode = swMaximize
	}
	w32.User32ShowWindow.Call(w.hwnd, showMode)
	w32.User32UpdateWindow.Call(w.hwnd)
	w32.User32SetFocus.Call(w.hwnd)

	// 嵌入浏览器
	if !w.browser.Embed(w.hwnd) {
		log.Println("browser.Embed failed")
		return false
	}
	w.browser.Resize()
	return true
}

// ----------------------------------------------------------------------------
// 窗口控制 API
// ----------------------------------------------------------------------------

func (w *webview) Hide() {
	w32.User32ShowWindow.Call(w.hwnd, swHide)
}

func (w *webview) Show() {
	w32.User32ShowWindow.Call(w.hwnd, swShow)
}

func (w *webview) Maximize() {
	w32.User32ShowWindow.Call(w.hwnd, swMaximize)
}

func (w *webview) Minimize() {
	w32.User32ShowWindow.Call(w.hwnd, swMinimize)
}

func (w *webview) Restore() {
	w32.User32ShowWindow.Call(w.hwnd, swRestore)
}

func (w *webview) Center() {
	var rect w32.Rect
	w32.User32GetWindowRect.Call(w.hwnd, uintptr(unsafe.Pointer(&rect)))

	width := rect.Right - rect.Left
	height := rect.Bottom - rect.Top

	screenW, _, _ := w32.User32GetSystemMetrics.Call(w32.SM_CXSCREEN)
	screenH, _, _ := w32.User32GetSystemMetrics.Call(w32.SM_CYSCREEN)

	x := (int32(screenW) - width) / 2
	y := (int32(screenH) - height) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	w32.User32SetWindowPos.Call(
		w.hwnd, 0,
		uintptr(x), uintptr(y),
		0, 0,
		swpNoZOrder|swpNoSize|swpNoActivate,
	)
}

func (w *webview) DisableMaximizeButton() {
	style := w32.GetWindowLong(w.hwnd, w32.GWLStyle)
	style &^= w32.WSMaximizeBox
	w32.SetWindowLong(w.hwnd, w32.GWLStyle, style)
	w32.User32SetWindowPos.Call(
		w.hwnd, 0, 0, 0, 0, 0,
		swpNoMove|swpNoSize|swpNoZOrder|swpFrameChanged,
	)
}

func (w *webview) EnableMaximizeButton() {
	style := w32.GetWindowLong(w.hwnd, w32.GWLStyle)
	style |= w32.WSMaximizeBox
	w32.SetWindowLong(w.hwnd, w32.GWLStyle, style)
	w32.User32SetWindowPos.Call(
		w.hwnd, 0, 0, 0, 0, 0,
		swpNoMove|swpNoSize|swpNoZOrder|swpFrameChanged,
	)
}

func (w *webview) SetIconFromFile(iconPath string) {
	ptr, err := windows.UTF16PtrFromString(iconPath)
	if err != nil {
		log.Printf("SetIconFromFile: invalid path: %v", err)
		return
	}

	smallIcon, _, _ := w32.User32LoadImageW.Call(
		0, uintptr(unsafe.Pointer(ptr)),
		imageIcon, 16, 16, lrLoadFromFile,
	)
	largeIcon, _, _ := w32.User32LoadImageW.Call(
		0, uintptr(unsafe.Pointer(ptr)),
		imageIcon, 32, 32, lrLoadFromFile,
	)

	if smallIcon == 0 && largeIcon == 0 {
		log.Printf("SetIconFromFile: failed to load icon from %q", iconPath)
		return
	}

	if smallIcon != 0 {
		w32.User32SendMessageW.Call(w.hwnd, wmSetIcon, iconSmall, smallIcon)
	}
	if largeIcon != 0 {
		w32.User32SendMessageW.Call(w.hwnd, wmSetIcon, iconBig, largeIcon)
	}
}

func (w *webview) SetSize(width, height int) {
	w.Restore()
	screenW, _, _ := w32.User32GetSystemMetrics.Call(0)
	screenH, _, _ := w32.User32GetSystemMetrics.Call(1)

	x := (int(screenW) - width) / 2
	y := (int(screenH) - height) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	w32.User32SetWindowPos.Call(
		w.hwnd, 0,
		uintptr(x), uintptr(y),
		uintptr(width), uintptr(height),
		swpNoZOrder|swpNoActivate,
	)

	// 强制 WebView 重新适配新窗口尺寸
	w.browser.Resize()
}

// ----------------------------------------------------------------------------
// RPC 消息绑定
// ----------------------------------------------------------------------------

type rpcMessage struct {
	ID     int               `json:"id"`
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

func jsString(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (w *webview) msgcb(msg string) {
	d := rpcMessage{}
	if err := json.Unmarshal([]byte(msg), &d); err != nil {
		log.Printf("invalid RPC message: %v", err)
		return
	}

	id := strconv.Itoa(d.ID)
	res, err := w.callbinding(d)
	if err != nil {
		errStr := jsString(err.Error())
		w.Dispatch(func() {
			w.Eval("window._rpc[" + id + "].reject(" + errStr + ");" +
				"window._rpc[" + id + "] = undefined")
		})
		return
	}

	b, err := json.Marshal(res)
	if err != nil {
		errStr := jsString(err.Error())
		w.Dispatch(func() {
			w.Eval("window._rpc[" + id + "].reject(" + errStr + ");" +
				"window._rpc[" + id + "] = undefined")
		})
		return
	}

	result := string(b)
	w.Dispatch(func() {
		w.Eval("window._rpc[" + id + "].resolve(" + result + ");" +
			"window._rpc[" + id + "] = undefined")
	})
}

func (w *webview) callbinding(d rpcMessage) (interface{}, error) {
	w.mu.Lock()
	f, ok := w.bindings[d.Method]
	w.mu.Unlock()
	if !ok {
		return nil, nil
	}

	v := reflect.ValueOf(f)
	t := v.Type()
	isVariadic := t.IsVariadic()
	numIn := t.NumIn()

	if isVariadic {
		if len(d.Params) < numIn-1 {
			return nil, errors.New("function arguments mismatch")
		}
	} else {
		if len(d.Params) != numIn {
			return nil, errors.New("function arguments mismatch")
		}
	}

	args := make([]reflect.Value, 0, len(d.Params))
	for i, param := range d.Params {
		var argType reflect.Type
		if isVariadic && i >= numIn-1 {
			argType = t.In(numIn - 1).Elem()
		} else {
			argType = t.In(i)
		}
		arg := reflect.New(argType)
		if err := json.Unmarshal(param, arg.Interface()); err != nil {
			return nil, err
		}
		args = append(args, arg.Elem())
	}

	errorType := reflect.TypeOf((*error)(nil)).Elem()
	res := v.Call(args)

	switch len(res) {
	case 0:
		return nil, nil
	case 1:
		if res[0].Type().Implements(errorType) {
			if !res[0].IsNil() {
				return nil, res[0].Interface().(error)
			}
			return nil, nil
		}
		return res[0].Interface(), nil
	case 2:
		if !res[1].Type().Implements(errorType) {
			return nil, errors.New("second return value must be an error")
		}
		if res[1].IsNil() {
			return res[0].Interface(), nil
		}
		return res[0].Interface(), res[1].Interface().(error)
	default:
		return nil, errors.New("unexpected number of return values")
	}
}

// ----------------------------------------------------------------------------
// 窗口消息处理
// ----------------------------------------------------------------------------

func wndproc(hwnd, msg, wp, lp uintptr) uintptr {
	w, ok := getWindowContext(hwnd).(*webview)
	if !ok {
		r, _, _ := w32.User32DefWindowProcW.Call(hwnd, msg, wp, lp)
		return r
	}

	switch msg {
	case w32.WMMove, w32.WMMoving:
		_ = w.browser.NotifyParentWindowPositionChanged()

	case w32.WMNCLButtonDown:
		w32.User32SetFocus.Call(w.hwnd)
		r, _, _ := w32.User32DefWindowProcW.Call(hwnd, msg, wp, lp)
		return r

	case w32.WMSize:
		w.browser.Resize()

	case w32.WMActivate:
		if wp != w32.WAInactive && w.autofocus {
			w.browser.Focus()
		}

	case w32.WMClose:
		w32.User32DestroyWindow.Call(hwnd)

	case w32.WMDestroy:
		deleteWindowContext(hwnd)
		w.Terminate()

	case w32.WMGetMinMaxInfo:
		lpmmi := (*w32.MinMaxInfo)(unsafe.Pointer(lp))
		if w.maxsz.X > 0 && w.maxsz.Y > 0 {
			lpmmi.PtMaxSize = w.maxsz
			lpmmi.PtMaxTrackSize = w.maxsz
		}
		if w.minsz.X > 0 && w.minsz.Y > 0 {
			lpmmi.PtMinTrackSize = w.minsz
		}

	default:
		r, _, _ := w32.User32DefWindowProcW.Call(hwnd, msg, wp, lp)
		return r
	}

	return 0
}

// ----------------------------------------------------------------------------
// 事件循环
// ----------------------------------------------------------------------------

func (w *webview) Run() {
	var msg w32.Msg
	for {
		ret, _, _ := w32.User32GetMessageW.Call(
			uintptr(unsafe.Pointer(&msg)), 0, 0, 0,
		)
		// GetMessage 返回 -1 表示错误，0 表示 WM_QUIT
		switch int32(ret) {
		case -1:
			log.Println("GetMessageW error")
			return
		case 0:
			return
		}

		if msg.Message == w32.WMApp {
			w.mu.Lock()
			q := make([]func(), len(w.dispatchq))
			copy(q, w.dispatchq)
			w.dispatchq = w.dispatchq[:0]
			w.mu.Unlock()
			for _, fn := range q {
				fn()
			}
			continue
		}

		r, _, _ := w32.User32GetAncestor.Call(uintptr(msg.Hwnd), w32.GARoot)
		r, _, _ = w32.User32IsDialogMessage.Call(r, uintptr(unsafe.Pointer(&msg)))
		if r != 0 {
			continue
		}
		w32.User32TranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		w32.User32DispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// ----------------------------------------------------------------------------
// WebView 标准接口实现
// ----------------------------------------------------------------------------

func (w *webview) Destroy() {
	w32.User32PostMessageW.Call(w.hwnd, w32.WMClose, 0, 0)
}

func (w *webview) Terminate() {
	w32.User32PostQuitMessage.Call(0)
}

func (w *webview) Window() unsafe.Pointer {
	return unsafe.Pointer(w.hwnd)
}

func (w *webview) Navigate(url string) {
	w.browser.Navigate(url)
}

func (w *webview) SetHtml(html string) {
	w.browser.NavigateToString(html)
}

func (w *webview) SetTitle(title string) {
	titleUTF16, err := windows.UTF16FromString(title)
	if err != nil {
		titleUTF16, _ = windows.UTF16FromString("")
	}
	w32.User32SetWindowTextW.Call(w.hwnd, uintptr(unsafe.Pointer(&titleUTF16[0])))
}

func (w *webview) Init(js string) {
	w.browser.Init(js)
}

func (w *webview) Eval(js string) {
	w.browser.Eval(js)
}

func (w *webview) Dispatch(f func()) {
	w.mu.Lock()
	w.dispatchq = append(w.dispatchq, f)
	w.mu.Unlock()
	w32.User32PostThreadMessageW.Call(w.mainthread, w32.WMApp, 0, 0)
}

func (w *webview) Bind(name string, f interface{}) error {
	v := reflect.ValueOf(f)
	if v.Kind() != reflect.Func {
		return errors.New("only functions can be bound")
	}
	if n := v.Type().NumOut(); n > 2 {
		return errors.New("function may only return a value or a value+error")
	}

	w.mu.Lock()
	w.bindings[name] = f
	w.mu.Unlock()

	w.Init("(function() { var name = " + jsString(name) + ";" + `
		var RPC = window._rpc = (window._rpc || {nextSeq: 1});
		window[name] = function() {
			var seq = RPC.nextSeq++;
			var promise = new Promise(function(resolve, reject) {
				RPC[seq] = { resolve: resolve, reject: reject };
			});
			window.external.invoke(JSON.stringify({
				id: seq,
				method: name,
				params: Array.prototype.slice.call(arguments),
			}));
			return promise;
		};
	})()`)

	return nil
}

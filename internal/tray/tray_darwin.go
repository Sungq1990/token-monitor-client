//go:build darwin

// macOS 菜单栏托盘（Go 侧）。Objective-C 实现在 tray_darwin.m：
// Wails 运行时与 fyne/systray 的 NSApplication AppDelegate 冲突（wails#1521），
// tray_impl.go 在 darwin 下编不了，这里直接用原生 NSStatusItem 实现
// —— 只往状态栏加一个图标，不接管 NSApp 的生命周期，绕开冲突。
// .m 文件里还顺带补了两件 Wails 没做的事：
//   - 给 Wails 的 WindowDelegate 补 windowShouldMiniaturize:，把「最小化」变成
//     隐藏窗口，不产生程序坞的迷你窗口（点黄色按钮 / Cmd+M 都走这里）；
//   - 给 Wails 的 AppDelegate 补 applicationShouldHandleReopen:hasVisibleWindows:，
//     窗口隐藏后点程序坞图标也能唤回主窗口。
//
// 注意：//export 和 Objective-C 实现必须分文件——cgo 会把带 //export 的文件
// 的 preamble 编译两遍，实现放在里面会符号重复。
package tray

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa

#include <stdlib.h>

void traySetup(char *iconBytes, int iconLen);
void trayTeardown();
*/
import "C"

import (
	_ "embed"
	"unsafe"
)

// actions 由 Run 注入；托盘事件都在主线程回调，回调里再交给 Wails runtime 派发
var actions Actions

//go:embed icon.png
var iconPNG []byte

func Run(a Actions) {
	actions = a
	p := C.CBytes(iconPNG)
	C.traySetup((*C.char)(unsafe.Pointer(p)), C.int(len(iconPNG)))
	C.free(unsafe.Pointer(p))
}

// Quit 移除菜单栏图标（程序退出时调用）
func Quit() { C.trayTeardown() }

//export trayGoShow
func trayGoShow() { call(actions.Show) }

//export trayGoSync
func trayGoSync() { call(actions.Sync) }

//export trayGoPanel
func trayGoPanel() { call(actions.Panel) }

//export trayGoHide
func trayGoHide() { call(actions.Hide) }

//export trayGoQuit
func trayGoQuit() { call(actions.Quit) }

func call(f func()) {
	if f != nil {
		f()
	}
}

//go:build !darwin

// Windows / Linux：系统托盘实现。
// macOS 下与 Wails 运行时的 AppDelegate 符号冲突（见 wails#1521），故 darwin 不编译本文件。
package tray

import (
	_ "embed"
	"runtime"

	"fyne.io/systray"
)

//go:embed icon.ico
var iconICO []byte

// Run 阻塞运行托盘消息循环；Wails 占主线程，Windows/Linux 在 goroutine 里调用。
//
// 必须把当前 goroutine 绑定到 OS 线程：systray 在 Windows 上把托盘窗口创建在调用线程，
// 并在同一 goroutine 里循环 GetMessage，但它自己只在 init() 里锁了主 goroutine（假定由 main 调用）。
// 我们在独立 goroutine 里跑，若不锁线程，Go 调度器把 goroutine 迁到别的线程后，
// GetMessage 再也收不到托盘窗口的消息——表现为运行一段时间后托盘图标还在但点击/右键无反应。
func Run(a Actions) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	systray.Run(func() {
		systray.SetIcon(iconICO)
		systray.SetTooltip("Token Monitor — 后台采集中")
		mShow := systray.AddMenuItem("打开 Token Monitor", "显示设置窗口")
		mSync := systray.AddMenuItem("立即同步", "马上采集并上报一轮")
		mPanel := systray.AddMenuItem("打开网页面板", "在浏览器打开服务端面板")
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("退出", "停止采集并退出")
		go func() {
			for {
				select {
				case <-mShow.ClickedCh:
					if a.Show != nil {
						a.Show()
					}
				case <-mSync.ClickedCh:
					if a.Sync != nil {
						a.Sync()
					}
				case <-mPanel.ClickedCh:
					if a.Panel != nil {
						a.Panel()
					}
				case <-mQuit.ClickedCh:
					if a.Quit != nil {
						a.Quit()
					}
					return
				}
			}
		}()
	}, nil)
}

// Quit 停止托盘消息循环（程序退出时调用）
func Quit() { systray.Quit() }

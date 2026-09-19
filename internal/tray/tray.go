// Package tray 系统托盘：窗口关掉后可从托盘找回、立即同步、退出。
// macOS 上 Wails 占用主线程，托盘需主线程消息循环，故 mac 未接托盘（Windows/Linux 可用）。
package tray

import (
	_ "embed"

	"fyne.io/systray"
)

//go:embed icon.ico
var iconICO []byte

// Actions 托盘菜单触发的回调，由宿主（Wails App）注入
type Actions struct {
	Show  func() // 显示主窗口
	Sync  func() // 立即同步一轮
	Panel func() // 浏览器打开服务端面板
	Quit  func() // 退出程序
}

// Run 阻塞运行托盘消息循环；Wails 占主线程，Windows/Linux 下在 goroutine 里调用。
func Run(a Actions) {
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

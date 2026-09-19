package tray

// Actions 托盘菜单触发的回调，由宿主（Wails App）注入
type Actions struct {
	Show  func() // 显示主窗口
	Sync  func() // 立即同步一轮
	Panel func() // 浏览器打开服务端面板
	Quit  func() // 退出程序
}

//go:build darwin

// macOS：Wails 运行时与 systray 的 AppDelegate 符号冲突，无法共用系统托盘。
// 关闭窗口 = 正常退出（由 main.go 在 darwin 下不启用隐藏式关闭）。
package tray

func Run(_ Actions) {}

func Quit() {}

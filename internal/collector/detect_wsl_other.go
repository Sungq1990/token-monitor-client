//go:build !windows

package collector

// detectWSL 仅 Windows 实现；macOS/Linux 没有 WSL，宿主机目录就是全部来源。
func detectWSL() []Detection { return nil }

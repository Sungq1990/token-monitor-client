//go:build windows

// WSL 数据目录发现：注册表枚举发行版，再经 \\wsl.localhost\<发行版>\home\<用户>\ 扫描。
// 注意：访问未运行的发行版会把它拉起来，可能卡住，故每个发行版限时。
package collector

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	winreg "golang.org/x/sys/windows/registry"
)

const wslScanTimeout = 15 * time.Second

func detectWSL() []Detection {
	root, err := winreg.OpenKey(winreg.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Lxss`, winreg.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	defer root.Close()
	names, err := root.ReadSubKeyNames(-1)
	if err != nil {
		return nil
	}
	var out []Detection
	for _, name := range names {
		k, err := winreg.OpenKey(root, name, winreg.QUERY_VALUE)
		if err != nil {
			continue
		}
		distro, _, err := k.GetStringValue("DistributionName")
		k.Close()
		if err != nil || strings.TrimSpace(distro) == "" || strings.HasPrefix(distro, "docker-desktop") {
			continue
		}
		out = append(out, scanDistro(distro)...)
	}
	return out
}

// scanDistro 扫一个发行版：列 \\wsl.localhost\<distro>\home\* 下的用户目录，
// 逐个检查各类 Agent 的数据子目录。整个扫描限时，超时整组放弃（goroutine 自然结束后被回收）。
func scanDistro(distro string) []Detection {
	done := make(chan []Detection, 1)
	go func() {
		done <- scanDistroNow(distro)
	}()
	select {
	case d := <-done:
		return d
	case <-time.After(wslScanTimeout):
		return nil
	}
}

func scanDistroNow(distro string) []Detection {
	base := `\\wsl.localhost\` + distro
	// 家目录候选：/home/<user> 之外还有 /root——在 WSL 里用 root 跑 Agent 很常见，漏了它就扫不到
	var homes []string
	if users, err := os.ReadDir(filepath.Join(base, "home")); err == nil {
		for _, u := range users {
			if u.IsDir() {
				homes = append(homes, filepath.Join(base, "home", u.Name()))
			}
		}
	}
	if _, err := os.Stat(filepath.Join(base, "root")); err == nil {
		homes = append(homes, filepath.Join(base, "root"))
	}
	var out []Detection
	for _, home := range homes {
		for _, kind := range detectKinds {
			d := Detection{Key: kind, Label: LabelFor(kind), Origin: "WSL:" + distro, Counts: map[string]int{}}
			for _, rel := range agentRelPaths(kind, false) {
				p := filepath.Join(home, filepath.FromSlash(rel))
				if n, ok := probePath(kind, p); ok {
					d.Paths = append(d.Paths, p)
					d.Counts[p] = n
				}
			}
			if len(d.Paths) > 0 {
				out = append(out, d)
			}
		}
	}
	return out
}

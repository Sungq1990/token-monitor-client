// 自动发现本机已装的 Agent 数据目录：宿主机用户目录 + Windows 下的 WSL 发行版。
// 仅供设置页「自动扫描」使用；发现的路径与手填走同一套采集/展开逻辑。
package collector

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Detection 一组同源发现：某类 Agent 在某个来源（宿主机/某 WSL 发行版）下的全部候选路径。
// Counts 记录每条路径下解析器能发现的数据源数量（0 表示目录存在但没有用量记录）。
type Detection struct {
	Key    string         `json:"key"`
	Label  string         `json:"label"`
	Origin string         `json:"origin"`
	Paths  []string       `json:"paths"`
	Counts map[string]int `json:"counts"`
}

var detectKinds = []string{"claude", "codex", "opencode", "zcode"}

// agentRelPaths 某 Agent 在目标环境（宿主机或 WSL）用户目录下的候选子路径。
// windows 为 true 时给出 Windows 风格子路径（opencode 走 %LOCALAPPDATA%，仅宿主机有意义）。
func agentRelPaths(kind string, windows bool) []string {
	switch kind {
	case "claude", "codex", "zcode":
		return []string{"." + kind}
	case "opencode":
		if windows {
			if lad := os.Getenv("LOCALAPPDATA"); lad != "" {
				return []string{filepath.Join(lad, "opencode")}
			}
		}
		return []string{".local/share/opencode"}
	}
	return nil
}

// probePath 路径是目录则返回其下的数据源数量；第二个返回值表示目录是否存在。
func probePath(kind, path string) (int, bool) {
	st, err := os.Stat(path)
	if err != nil || !st.IsDir() {
		return 0, false
	}
	col := New(kind, kind, Options{Paths: []string{path}})
	if col == nil {
		return 0, true
	}
	return len(col.Sources()), true
}

// Detect 扫描本机：宿主机用户目录总是扫；Windows 上额外枚举 WSL 发行版。
func Detect() []Detection {
	out := detectHost()
	out = append(out, detectWSL()...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := detectKindsIndex(out[i].Key), detectKindsIndex(out[j].Key)
		if a != b {
			return a < b
		}
		return out[i].Origin < out[j].Origin
	})
	return out
}

func detectKindsIndex(k string) int {
	for i, x := range detectKinds {
		if x == k {
			return i
		}
	}
	return len(detectKinds)
}

// detectHost 扫描宿主机用户目录。macOS/Linux 就这一个来源；Windows 上 opencode 额外试 LOCALAPPDATA。
func detectHost() []Detection {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	win := runtime.GOOS == "windows"
	var out []Detection
	for _, kind := range detectKinds {
		d := Detection{Key: kind, Label: LabelFor(kind), Origin: "宿主机", Counts: map[string]int{}}
		for _, rel := range agentRelPaths(kind, win) {
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
	return out
}

func LabelFor(kind string) string {
	if l, ok := Labels[kind]; ok {
		return l
	}
	return kind
}

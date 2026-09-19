// Package collector 采集各 AI Agent 的本地用量记录。逻辑从 ai-token-monitor 的 Python 采集器逐条移植。
package collector

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// UsageItem 一条真实 token usage；MessageID 为空表示仅更新 session 元信息。JSON 字段与服务端一致。
type UsageItem struct {
	MessageID string `json:"message_id"`
	SessionID string `json:"session_id,omitempty"`
	Model     string `json:"model,omitempty"`
	Provider  string `json:"provider,omitempty"`

	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
	TotalTokens      int64 `json:"total_tokens"`

	Cost       *float64   `json:"cost,omitempty"`
	OccurredAt *time.Time `json:"occurred_at,omitempty"`

	SessionTitle         string     `json:"session_title,omitempty"`
	SessionStartedAt     *time.Time `json:"session_started_at,omitempty"`
	SessionLastMessageAt *time.Time `json:"session_last_message_at,omitempty"`
}

// Batch 一个数据源的一次采集结果；Cursor 在上报成功后才提交。
type Batch struct {
	Source string      `json:"source"`
	Cursor string      `json:"-"`
	Items  []UsageItem `json:"items"`
}

// CursorStore 游标读写接口（客户端用本地 JSON 文件实现）。
type CursorStore interface {
	Get(agent, source string) string
	Set(agent, source, cursor string)
}

// Collector 各 Agent 采集器统一接口。
type Collector interface {
	Name() string
	// Sources 列出当前可读取的数据源
	Sources() []string
	// Collect 增量采集，逐 source 产出 Batch；单条解析失败跳过不报错
	Collect(cursors CursorStore, logf func(string, ...any)) []Batch
}

// Options 每个 agent 的路径配置
type Options struct {
	Paths         []string
	ProjectsDir   string
	FileGlob      string
	DBFile        string
	WindowSeconds int
}

// Spec 描述一种采集器及其默认选项（供设置页展示）
type Spec struct {
	Key      string            `json:"key"`
	Label    string            `json:"label"`
	Defaults map[string]string `json:"defaults"`
	// DefaultPaths 按操作系统给出的默认根目录（设置页留空时的提示）
	DefaultPaths []string `json:"default_paths"`
}

type factory func(name string, o Options) Collector

var registry = map[string]factory{
	"claude":   func(n string, o Options) Collector { return newClaude(n, o) },
	"codex":    func(n string, o Options) Collector { return newCodex(n, o) },
	"opencode": func(n string, o Options) Collector { return newOpenCode(n, o) },
	"zcode":    func(n string, o Options) Collector { return newZCode(n, o) },
}

var Labels = map[string]string{"claude": "Claude Code", "opencode": "OpenCode", "zcode": "ZCode", "codex": "Codex"}

func Supported(kind string) bool { _, ok := registry[kind]; return ok }

// New 按类型创建采集器；未知类型返回 nil（设置页标「待适配」）。
func New(kind, name string, o Options) Collector {
	f, ok := registry[kind]
	if !ok {
		return nil
	}
	if o.WindowSeconds <= 0 {
		o.WindowSeconds = 7200
	}
	return f(name, o)
}

func Specs() []Spec {
	home, _ := os.UserHomeDir()
	j := func(parts ...string) string { return filepath.Join(append([]string{home}, parts...)...) }
	return []Spec{
		{Key: "claude", Label: "Claude Code", Defaults: map[string]string{"projects_dir": "projects", "file_glob": "*/*.jsonl"}, DefaultPaths: []string{j(".claude")}},
		{Key: "codex", Label: "Codex", Defaults: map[string]string{"projects_dir": "sessions", "file_glob": "**/*.jsonl"}, DefaultPaths: []string{j(".codex")}},
		{Key: "opencode", Label: "OpenCode", Defaults: map[string]string{"db_file": "opencode.db"}, DefaultPaths: opencodeDefaultPaths(home)},
		{Key: "zcode", Label: "ZCode", Defaults: map[string]string{"db_file": "cli/db/db.sqlite"}, DefaultPaths: []string{j(".zcode")}},
	}
}

func opencodeDefaultPaths(home string) []string {
	if strings.EqualFold(os.Getenv("OS"), "Windows_NT") {
		if lad := os.Getenv("LOCALAPPDATA"); lad != "" {
			return []string{filepath.Join(lad, "opencode")}
		}
	}
	return []string{filepath.Join(home, ".local", "share", "opencode")}
}

// ExpandPath 展开 ~ 与环境变量
func ExpandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return p
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\") {
		if home, err := os.UserHomeDir(); err == nil {
			p = home + p[1:]
		}
	}
	return filepath.Clean(os.ExpandEnv(p))
}

func existingRoots(paths []string) []string {
	var out []string
	for _, p := range paths {
		e := ExpandPath(p)
		if e == "" {
			continue
		}
		if st, err := os.Stat(e); err == nil && st.IsDir() {
			out = append(out, e)
		}
	}
	return out
}

func or(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func toLocal(t time.Time) *time.Time {
	l := t.Local()
	return &l
}

func msToTime(ms int64) *time.Time {
	if ms <= 0 {
		return nil
	}
	return toLocal(time.UnixMilli(ms))
}

func minTime(a, b *time.Time) *time.Time {
	if a == nil {
		return b
	}
	if b == nil || a.Before(*b) {
		return a
	}
	return b
}

func maxTime(a, b *time.Time) *time.Time {
	if a == nil {
		return b
	}
	if b == nil || a.After(*b) {
		return a
	}
	return b
}

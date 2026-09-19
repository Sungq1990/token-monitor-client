// Package config 客户端本地配置：服务端地址、设备标识、采集频率、Agent 路径。
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const AppName = "token-monitor"

type AgentConfig struct {
	Agent       string   `json:"agent"`
	Enabled     bool     `json:"enabled"`
	Paths       []string `json:"paths"`
	ProjectsDir string   `json:"projects_dir,omitempty"`
	FileGlob    string   `json:"file_glob,omitempty"`
	DBFile      string   `json:"db_file,omitempty"`
}

type Config struct {
	ServerURL           string        `json:"server_url,omitempty"`
	DeviceID            string        `json:"device_id"`
	DeviceName          string        `json:"device_name"`
	IntervalMinutes     int           `json:"interval_minutes"`
	RescanWindowSeconds int           `json:"rescan_window_seconds"`
	Agents              []AgentConfig `json:"agents"`
	AutoStart           bool          `json:"auto_start"`
	StartMinimized      bool          `json:"start_minimized"`
}

// Remote 返回推送到服务端的配置副本：剥离 server_url（接口地址是本地引导信息，
// 服务端存了也没意义）；device_id 保留只是便于人读，服务端本身以 URL 路径区分设备。
func (c Config) Remote() Config {
	c.ServerURL = ""
	agents := make([]AgentConfig, len(c.Agents))
	for i, a := range c.Agents {
		agents[i] = a
		agents[i].Paths = append([]string(nil), a.Paths...)
	}
	c.Agents = agents
	return c
}

var (
	deviceIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)
	agentRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,31}$`)
)

// Dir 配置目录：Windows %APPDATA%\token-monitor，macOS ~/Library/Application Support/token-monitor，Linux ~/.config/token-monitor
func Dir() string {
	if v := os.Getenv("TOKEN_MONITOR_HOME"); v != "" {
		return v
	}
	base, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, AppName)
}

func Path() string { return filepath.Join(Dir(), "config.json") }

func defaultAgents() []AgentConfig {
	home, _ := os.UserHomeDir()
	j := func(parts ...string) string { return filepath.Join(append([]string{home}, parts...)...) }
	oc := j(".local", "share", "opencode")
	if runtime.GOOS == "windows" {
		if lad := os.Getenv("LOCALAPPDATA"); lad != "" {
			oc = filepath.Join(lad, "opencode")
		}
	}
	return []AgentConfig{
		{Agent: "claude", Enabled: true, Paths: []string{j(".claude")}},
		{Agent: "opencode", Enabled: true, Paths: []string{oc}},
		{Agent: "zcode", Enabled: true, Paths: []string{j(".zcode")}},
	}
}

func Default() Config {
	host, _ := os.Hostname()
	return Config{
		ServerURL:           "http://127.0.0.1:8765",
		DeviceID:            NewDeviceID(),
		DeviceName:          host,
		IntervalMinutes:     5,
		RescanWindowSeconds: 7200,
		Agents:              defaultAgents(),
	}
}

// NewDeviceID 生成本机唯一标识：hostname 前缀 + 随机 hex，便于面板一眼认出。
func NewDeviceID() string {
	host, _ := os.Hostname()
	host = strings.ToLower(host)
	host = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(host, "-")
	host = strings.Trim(host, "-")
	if len(host) > 20 {
		host = host[:20]
	}
	if host == "" {
		host = runtime.GOOS
	}
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s-%d", host, time.Now().UnixNano()%100000000)
	}
	return host + "-" + hex.EncodeToString(b)
}

type Store struct {
	mu  sync.Mutex
	cfg Config
}

// Load 读取配置；不存在则生成默认配置并落盘（此时 device_id 即被固定）。
func Load() (*Store, error) {
	s := &Store{}
	data, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		s.cfg = Default()
		return s, s.save()
	}
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config.json 解析失败: %w", err)
	}
	changed := false
	if cfg.DeviceID == "" {
		cfg.DeviceID = NewDeviceID()
		changed = true
	}
	if cfg.IntervalMinutes < 1 || cfg.IntervalMinutes > 1440 {
		cfg.IntervalMinutes = 5
		changed = true
	}
	if cfg.RescanWindowSeconds <= 0 {
		cfg.RescanWindowSeconds = 7200
	}
	if cfg.Agents == nil {
		cfg.Agents = defaultAgents()
		changed = true
	}
	// 自愈：剔除名字为空的无效 Agent（防止坏数据被一直采纳）
	healed := make([]AgentConfig, 0, len(cfg.Agents))
	for _, a := range cfg.Agents {
		if strings.TrimSpace(a.Agent) != "" {
			healed = append(healed, a)
		}
	}
	if len(healed) != len(cfg.Agents) {
		cfg.Agents = healed
		changed = true
	}
	s.cfg = cfg
	if changed {
		return s, s.save()
	}
	return s, nil
}

func (s *Store) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cfg
	c.Agents = append([]AgentConfig(nil), s.cfg.Agents...)
	for i := range c.Agents {
		c.Agents[i].Paths = append([]string(nil), c.Agents[i].Paths...)
	}
	return c
}

// Validate 校验并规整（去空路径、去重名）
func Validate(c *Config) error {
	c.ServerURL = strings.TrimRight(strings.TrimSpace(c.ServerURL), "/")
	if c.ServerURL == "" {
		return errors.New("服务端地址不能为空")
	}
	if !strings.HasPrefix(c.ServerURL, "http://") && !strings.HasPrefix(c.ServerURL, "https://") {
		return errors.New("服务端地址需以 http:// 或 https:// 开头")
	}
	c.DeviceID = strings.TrimSpace(c.DeviceID)
	if !deviceIDRe.MatchString(c.DeviceID) {
		return errors.New("设备标识只能包含字母、数字、_ . : -，且不超过 64 位")
	}
	c.DeviceName = strings.TrimSpace(c.DeviceName)
	if len(c.DeviceName) > 128 {
		return errors.New("设备名称过长")
	}
	if c.IntervalMinutes < 1 || c.IntervalMinutes > 1440 {
		return errors.New("采集间隔需在 1~1440 分钟之间")
	}
	if c.RescanWindowSeconds <= 0 {
		c.RescanWindowSeconds = 7200
	}
	seen := map[string]bool{}
	for i := range c.Agents {
		a := &c.Agents[i]
		a.Agent = strings.TrimSpace(a.Agent)
		if !agentRe.MatchString(a.Agent) {
			return fmt.Errorf("Agent 名称「%s」非法：1-32 位字母数字 _ . -", a.Agent)
		}
		key := strings.ToLower(a.Agent)
		if seen[key] {
			return fmt.Errorf("Agent 名称重复：%s", a.Agent)
		}
		seen[key] = true
		var paths []string
		pseen := map[string]bool{}
		for _, p := range a.Paths {
			p = strings.TrimSpace(p)
			if p == "" || pseen[p] {
				continue
			}
			pseen[p] = true
			paths = append(paths, p)
		}
		a.Paths = paths
		if a.Enabled && len(paths) == 0 {
			return fmt.Errorf("Agent「%s」已启用，请至少添加一条数据路径", a.Agent)
		}
		a.ProjectsDir, a.FileGlob, a.DBFile = strings.TrimSpace(a.ProjectsDir), strings.TrimSpace(a.FileGlob), strings.TrimSpace(a.DBFile)
	}
	return nil
}

func (s *Store) Save(c Config) error {
	if err := Validate(&c); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = c
	return s.save()
}

// MergeRemote 用服务端保存的配置覆盖本地（配置以服务端为准）。
// server_url / device_id 保留本地值——它们是连上服务端的引导信息，不能被覆盖；
// 其余字段（设备名、间隔、回扫窗口、Agent 路径、启动选项）以服务端为准。
// 返回是否发生了实际变更。
func (s *Store) MergeRemote(remote Config) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	if v := strings.TrimSpace(remote.DeviceName); v != "" && v != s.cfg.DeviceName {
		s.cfg.DeviceName, changed = v, true
	}
	if remote.IntervalMinutes >= 1 && remote.IntervalMinutes <= 1440 && remote.IntervalMinutes != s.cfg.IntervalMinutes {
		s.cfg.IntervalMinutes, changed = remote.IntervalMinutes, true
	}
	if remote.RescanWindowSeconds > 0 && remote.RescanWindowSeconds != s.cfg.RescanWindowSeconds {
		s.cfg.RescanWindowSeconds, changed = remote.RescanWindowSeconds, true
	}
	if remote.AutoStart != s.cfg.AutoStart {
		s.cfg.AutoStart, changed = remote.AutoStart, true
	}
	if remote.StartMinimized != s.cfg.StartMinimized {
		s.cfg.StartMinimized, changed = remote.StartMinimized, true
	}
	valid := make([]AgentConfig, 0, len(remote.Agents))
	for _, a := range remote.Agents {
		if strings.TrimSpace(a.Agent) != "" {
			valid = append(valid, a)
		}
	}
	if len(valid) > 0 && !agentsEqual(valid, s.cfg.Agents) {
		s.cfg.Agents = valid
		changed = true
	}
	if changed {
		_ = s.save()
	}
	return changed
}

func agentsEqual(a, b []AgentConfig) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Agent != b[i].Agent || a[i].Enabled != b[i].Enabled ||
			a[i].ProjectsDir != b[i].ProjectsDir || a[i].FileGlob != b[i].FileGlob || a[i].DBFile != b[i].DBFile ||
			len(a[i].Paths) != len(b[i].Paths) {
			return false
		}
		for j := range a[i].Paths {
			if a[i].Paths[j] != b[i].Paths[j] {
				return false
			}
		}
	}
	return true
}

func (s *Store) save() error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, Path())
}

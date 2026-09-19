// Package agent 采集调度：定时扫描本地 Agent 数据、上报服务端、心跳。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"

	"token-monitor-client/internal/client"
	"token-monitor-client/internal/collector"
	"token-monitor-client/internal/config"
	"token-monitor-client/internal/cursors"
)

const Version = "1.0.0"

// AgentStatus 每个 agent 上一轮采集的结果（心跳时上报服务端 + 设置页展示）
type AgentStatus struct {
	Agent      string   `json:"agent"`
	Enabled    bool     `json:"enabled"`
	Supported  bool     `json:"supported"`
	Sources    []string `json:"sources"`
	UsageRows  int      `json:"usage_rows"`
	LastRunAt  string   `json:"last_run_at,omitempty"`
	LastSyncAt string   `json:"last_sync_at,omitempty"`
	Error      string   `json:"error,omitempty"`
}

type Status struct {
	Running       bool                   `json:"running"`
	Syncing       bool                   `json:"syncing"`
	Connected     bool                   `json:"connected"`
	ServerVersion string                 `json:"server_version,omitempty"`
	ServerError   string                 `json:"server_error,omitempty"`
	LastRunAt     string                 `json:"last_run_at,omitempty"`
	LastSuccessAt string                 `json:"last_success_at,omitempty"`
	NextRunAt     string                 `json:"next_run_at,omitempty"`
	TotalUploaded int64                  `json:"total_uploaded"`
	Agents        map[string]AgentStatus `json:"agents"`
	Log           []string               `json:"log"`
}

type Runner struct {
	cfg  *config.Store
	cur  *cursors.File
	mu   sync.Mutex
	st   Status
	log  []string
	wake chan struct{}
	stop chan struct{}
	once sync.Once
	// OnChange 状态变化回调（Wails 事件推送）
	OnChange func(Status)
	// OnConfigChange 服务端配置被采纳后回调（Wails 事件推送，前端刷新设置页）
	OnConfigChange func(config.Config)
	// OnServerDown 服务端从可达变为不可达（每次断线沿触发一次）
	OnServerDown func(err error)
	// OnServerUp 服务端恢复可达
	OnServerUp func()
}

func NewRunner(cfg *config.Store, cur *cursors.File) *Runner {
	return &Runner{cfg: cfg, cur: cur, wake: make(chan struct{}, 1), stop: make(chan struct{}), st: Status{Agents: map[string]AgentStatus{}}}
}

func (r *Runner) Logf(format string, args ...any) {
	line := time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, args...)
	log.Printf(format, args...)
	r.mu.Lock()
	r.log = append(r.log, line)
	if len(r.log) > 200 {
		r.log = r.log[len(r.log)-200:]
	}
	r.mu.Unlock()
}

func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.st
	s.Agents = map[string]AgentStatus{}
	for k, v := range r.st.Agents {
		s.Agents[k] = v
	}
	s.Log = append([]string(nil), r.log...)
	return s
}

func (r *Runner) update(f func(*Status)) {
	r.mu.Lock()
	f(&r.st)
	s := r.st
	cb := r.OnChange
	r.mu.Unlock()
	if cb != nil {
		cb(s)
	}
}

// SyncNow 立即触发一轮采集
func (r *Runner) SyncNow() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *Runner) Stop() { r.once.Do(func() { close(r.stop) }) }

// StartHeartbeat 每分钟探测一次服务端健康（GET /api/health）：
// 正常则顺带发一次心跳刷新服务端 last_seen；探测失败/恢复时触发 OnServerDown / OnServerUp。
func (r *Runner) StartHeartbeat() {
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		wasDown := false
		for {
			select {
			case <-r.stop:
				return
			case <-t.C:
				cfg := r.cfg.Get()
				c := client.New(cfg.ServerURL)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_, err := c.Health(ctx)
				if err == nil {
					// 顺带心跳：让面板「在线」状态按分钟级刷新
					if hbErr := c.Heartbeat(ctx, r.deviceInfo(cfg, r.Status().Agents)); hbErr != nil {
						err = hbErr
					}
				}
				cancel()
				down := err != nil
				if down {
					r.update(func(s *Status) { s.Connected = false; s.ServerError = err.Error() })
				} else {
					r.update(func(s *Status) { s.Connected = true; s.ServerError = "" })
				}
				if down && !wasDown && r.OnServerDown != nil {
					r.OnServerDown(err)
				}
				if !down && wasDown && r.OnServerUp != nil {
					r.OnServerUp()
				}
				wasDown = down
			}
		}
	}()
}

// Run 主循环：启动即跑一轮，之后按配置间隔；配置改动后 SyncNow 会重新计算间隔。
func (r *Runner) Run() {
	r.update(func(s *Status) { s.Running = true })
	first := true
	for {
		if !first {
			interval := time.Duration(r.cfg.Get().IntervalMinutes) * time.Minute
			next := time.Now().Add(interval)
			r.update(func(s *Status) { s.NextRunAt = next.Format("15:04:05") })
			select {
			case <-r.stop:
				r.update(func(s *Status) { s.Running = false })
				return
			case <-r.wake:
			case <-time.After(interval):
			}
		}
		first = false
		r.RunOnce()
	}
}

func (r *Runner) deviceInfo(cfg config.Config, status map[string]AgentStatus) client.DeviceInfo {
	host, _ := os.Hostname()
	agents := make([]map[string]any, 0, len(cfg.Agents))
	for _, a := range cfg.Agents {
		agents = append(agents, map[string]any{"agent": a.Agent, "enabled": a.Enabled, "paths": a.Paths, "supported": collector.Supported(a.Agent)})
	}
	return client.DeviceInfo{
		DeviceID: cfg.DeviceID, Name: cfg.DeviceName, OS: runtime.GOOS + "/" + runtime.GOARCH, Hostname: host,
		ClientVersion: Version, IntervalMinutes: cfg.IntervalMinutes, Agents: agents,
		Status: map[string]any{"agents": status, "client_time": time.Now().Format(time.RFC3339)},
	}
}

// Register 向服务端注册/更新设备信息（保存设置后调用）。
// 成功后同步配置：先拉服务端配置（若有则采纳，服务端为准），再把本地配置快照推送到服务端。
func (r *Runner) Register(ctx context.Context) error {
	cfg := r.cfg.Get()
	c := client.New(cfg.ServerURL)
	resp, err := c.Register(ctx, r.deviceInfo(cfg, r.Status().Agents))
	if err != nil {
		r.update(func(s *Status) { s.Connected = false; s.ServerError = err.Error() })
		return err
	}
	r.update(func(s *Status) { s.Connected = true; s.ServerError = ""; s.ServerVersion = resp.ServerVersion })
	r.syncConfig(ctx, c)
	return nil
}

// syncConfig 配置以服务端为准：
//  1. 服务端存有配置 → 采纳覆盖本地（server_url / device_id 除外），有变化则通知 UI；
//  2. 把最终配置快照推回服务端持久化。
//
// 保存设置路径不会走冲突覆盖：SaveConfig 先推送新配置再调用 Register，拉到的即最新值。
func (r *Runner) syncConfig(ctx context.Context, c *client.Client) {
	cfg := r.cfg.Get()
	if remote, err := c.GetDeviceConfig(ctx, cfg.DeviceID); err != nil {
		r.Logf("拉取服务端配置失败: %v", err)
	} else if len(remote) > 0 {
		var rc config.Config
		if json.Unmarshal(remote, &rc) != nil {
			r.Logf("服务端配置解析失败，忽略")
		} else if r.cfg.MergeRemote(rc) {
			r.Logf("已采纳服务端下发的配置")
			if r.OnConfigChange != nil {
				r.OnConfigChange(r.cfg.Get())
			}
		}
	}
	cfg = r.cfg.Get()
	if err := c.PutDeviceConfig(ctx, cfg.DeviceID, cfg.Remote()); err != nil {
		r.Logf("配置保存到服务端失败: %v", err)
	} else {
		r.Logf("配置已保存到服务端")
	}
}

// RunOnce 跑一轮：逐 agent 采集 → 上报 → 成功后提交游标；最后心跳。
func (r *Runner) RunOnce() {
	r.mu.Lock()
	if r.st.Syncing {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	r.update(func(s *Status) { s.Syncing = true; s.LastRunAt = time.Now().Format("15:04:05") })
	defer r.update(func(s *Status) { s.Syncing = false })

	cfg := r.cfg.Get()
	c := client.New(cfg.ServerURL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	allOK := true
	var resMu sync.Mutex
	statuses := map[string]AgentStatus{}
	now := time.Now().Format("2006-01-02 15:04:05")

	// 各 agent 并行采集上报：互不依赖，网络盘/大库的慢源不再拖累其他 agent
	var wg sync.WaitGroup
	for _, ac := range cfg.Agents {
		ac := ac
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := AgentStatus{Agent: ac.Agent, Enabled: ac.Enabled, Supported: collector.Supported(ac.Agent), Sources: []string{}, LastRunAt: now}
			prev := r.Status().Agents[ac.Agent]
			st.LastSyncAt = prev.LastSyncAt
			if !ac.Enabled || !st.Supported {
				resMu.Lock()
				statuses[ac.Agent] = st
				resMu.Unlock()
				return
			}
			col := collector.New(ac.Agent, ac.Agent, collector.Options{
				Paths: ac.Paths, ProjectsDir: ac.ProjectsDir, FileGlob: ac.FileGlob, DBFile: ac.DBFile, WindowSeconds: cfg.RescanWindowSeconds,
			})
			st.Sources = col.Sources()
			batches := col.Collect(r.cur, r.Logf)
			if len(batches) == 0 {
				resMu.Lock()
				statuses[ac.Agent] = st
				resMu.Unlock()
				return
			}
			// 分块上报，避免单请求过大
			for i := 0; i < len(batches); i += 50 {
				end := i + 50
				if end > len(batches) {
					end = len(batches)
				}
				part := batches[i:end]
				resp, err := c.Ingest(ctx, client.IngestReq{DeviceID: cfg.DeviceID, Agent: ac.Agent, Batches: part})
				if err != nil {
					st.Error = err.Error()
					resMu.Lock()
					allOK = false
					resMu.Unlock()
					r.Logf("%s: 上报失败（游标不推进，下轮重试）: %v", ac.Agent, err)
					r.update(func(s *Status) { s.Connected = false; s.ServerError = err.Error() })
					break
				}
				for _, b := range part {
					if b.Cursor != "" {
						r.cur.Set(ac.Agent, b.Source, b.Cursor)
					}
				}
				st.UsageRows += resp.UsageRows
				st.LastSyncAt = now
				r.update(func(s *Status) { s.Connected = true; s.ServerError = ""; s.TotalUploaded += int64(resp.UsageRows) })
			}
			if st.Error == "" && st.UsageRows > 0 {
				r.Logf("%s: 上报 %d 条用量（%d 个数据源）", ac.Agent, st.UsageRows, len(batches))
			}
			resMu.Lock()
			statuses[ac.Agent] = st
			resMu.Unlock()
		}()
	}
	wg.Wait()
	if err := r.cur.Flush(); err != nil {
		r.Logf("保存游标失败: %v", err)
	}
	collector.CleanupSnapshots()

	// 心跳：带上各 agent 状态
	if err := c.Heartbeat(ctx, r.deviceInfo(cfg, statuses)); err != nil {
		allOK = false
		r.Logf("心跳失败: %v", err)
		r.update(func(s *Status) { s.Connected = false; s.ServerError = err.Error() })
	} else {
		r.update(func(s *Status) { s.Connected = true; s.ServerError = "" })
	}
	r.update(func(s *Status) {
		s.Agents = statuses
		if allOK {
			s.LastSuccessAt = time.Now().Format("15:04:05")
		}
	})
}

// SortedAgents 供前端稳定展示
func SortedAgents(m map[string]AgentStatus) []AgentStatus {
	out := make([]AgentStatus, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Agent < out[j].Agent })
	return out
}

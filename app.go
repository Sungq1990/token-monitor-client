package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"token-monitor-client/internal/agent"
	"token-monitor-client/internal/client"
	"token-monitor-client/internal/collector"
	"token-monitor-client/internal/config"
	"token-monitor-client/internal/cursors"
	"token-monitor-client/internal/tray"
)

// App 暴露给前端的方法集合（window.go.main.App.*）
type App struct {
	ctx    context.Context
	cfg    *config.Store
	cur    *cursors.File
	runner *agent.Runner
	// dialogOpen 断线弹窗是否正在显示（防止堆叠）
	dialogOpen atomic.Bool
}

func NewApp() (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	cur, err := cursors.Open(config.Dir())
	if err != nil {
		return nil, err
	}
	a := &App{cfg: cfg, cur: cur, runner: agent.NewRunner(cfg, cur)}
	return a, nil
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.runner.OnChange = func(s agent.Status) {
		wailsRuntime.EventsEmit(ctx, "status", s)
	}
	a.runner.OnConfigChange = func(c config.Config) {
		wailsRuntime.EventsEmit(ctx, "config", c)
	}
	a.runner.OnServerDown = func(err error) {
		wailsRuntime.EventsEmit(ctx, "serverdown", err.Error())
		// 本机弹窗提醒：每次断线只弹一次，恢复后页面提示。
		// Windows 下 MessageDialog 是在调用方 goroutine 里同步 MessageBox 直到用户点掉，
		// 不能阻塞心跳循环，故放到独立 goroutine；同一时间只保留一个弹窗，避免长期无人值守时堆叠。
		if !a.dialogOpen.CompareAndSwap(false, true) {
			return
		}
		go func() {
			defer a.dialogOpen.Store(false)
			_, _ = wailsRuntime.MessageDialog(ctx, wailsRuntime.MessageDialogOptions{
				Type:    wailsRuntime.ErrorDialog,
				Title:   "Token Monitor：服务端连接断开",
				Message: "无法连接服务端：" + err.Error() + "\n\n采集会自动重试，恢复后页面会提示。",
			})
		}()
	}
	a.runner.OnServerUp = func() {
		wailsRuntime.EventsEmit(ctx, "serverup")
	}
	go a.runner.Run()
	a.runner.StartHeartbeat()
	go func() {
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		_ = a.runner.Register(c)
	}()
	// 系统托盘（Wails 占主线程，Windows/Linux 在 goroutine 里跑消息循环）
	go tray.Run(tray.Actions{
		Show:  a.ShowWindow,
		Sync:  a.SyncNow,
		Panel: func() {
			// 服务端地址末尾斜杠统一规范化后再拼面板路径
			wailsRuntime.BrowserOpenURL(ctx, strings.TrimRight(a.cfg.Get().ServerURL, "/")+"/")
		},
		Quit:  a.QuitApp,
	})
}

func (a *App) shutdown(ctx context.Context) {
	a.runner.Stop()
	_ = a.cur.Flush()
	collector.CleanupSnapshots()
	tray.Quit()
}

// ---- 前端调用 ----

type Info struct {
	Version    string           `json:"version"`
	OS         string           `json:"os"`
	Arch       string           `json:"arch"`
	Hostname   string           `json:"hostname"`
	ConfigPath string           `json:"config_path"`
	Collectors []collector.Spec `json:"collectors"`
}

func (a *App) GetInfo() Info {
	host, _ := os.Hostname()
	return Info{Version: agent.Version, OS: runtime.GOOS, Arch: runtime.GOARCH, Hostname: host, ConfigPath: config.Path(), Collectors: collector.Specs()}
}

func (a *App) GetConfig() config.Config { return a.cfg.Get() }

// SaveConfig 保存设置：校验 → 落盘 → 推送配置到服务端（配置以服务端为准）→ 重新注册 → 立即跑一轮
func (a *App) SaveConfig(c config.Config) error {
	old := a.cfg.Get()
	if err := a.cfg.Save(c); err != nil {
		return err
	}
	// 设备标识改了：旧游标属于旧设备的数据视角，清空以便新标识下全量重传
	if old.DeviceID != strings.TrimSpace(c.DeviceID) {
		_ = a.cur.Reset("")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// 先推送新配置再注册：注册时会拉取服务端配置采纳，先推保证拉到的就是刚保存的值
	if err := client.New(c.ServerURL).PutDeviceConfig(ctx, c.DeviceID, c.Remote()); err != nil {
		return fmt.Errorf("配置已保存到本地，但同步到服务端失败：%v", err)
	}
	if err := a.runner.Register(ctx); err != nil {
		return fmt.Errorf("配置已保存并同步到服务端，但连接服务端失败：%v", err)
	}
	a.runner.SyncNow()
	return nil
}

// ShowWindow 从托盘/最小化恢复显示主窗口
func (a *App) ShowWindow() {
	wailsRuntime.WindowUnminimise(a.ctx)
	wailsRuntime.WindowShow(a.ctx)
}

// QuitApp 完全退出程序（托盘「退出」触发；关窗口不会走到这里，采集继续）
func (a *App) QuitApp() {
	if a.ctx == nil {
		os.Exit(0)
	}
	wailsRuntime.Quit(a.ctx)
}

func (a *App) GetStatus() agent.Status { return a.runner.Status() }

func (a *App) SyncNow() { a.runner.SyncNow() }

// ResetCursors 清空游标（agent 为空则全部），下轮全量重扫；服务端按 message_id 去重不会重复累计
func (a *App) ResetCursors(agentName string) error {
	if err := a.cur.Reset(agentName); err != nil {
		return err
	}
	a.runner.SyncNow()
	return nil
}

func (a *App) NewDeviceID() string { return config.NewDeviceID() }

// TestServer 测试服务端连通性
func (a *App) TestServer(url string) (client.Health, error) {
	url = strings.TrimRight(strings.TrimSpace(url), "/")
	if url == "" {
		return client.Health{}, fmt.Errorf("请输入服务端地址")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return client.New(url).Health(ctx)
}

// ProbePaths 检查路径是否存在、某 agent 类型下能发现多少数据源
func (a *App) ProbePaths(kind string, ac config.AgentConfig) map[string]any {
	out := map[string]any{"exists": map[string]bool{}, "sources": []string{}, "supported": collector.Supported(kind)}
	ex := out["exists"].(map[string]bool)
	for _, p := range ac.Paths {
		st, err := os.Stat(collector.ExpandPath(p))
		ex[p] = err == nil && st.IsDir()
	}
	if col := collector.New(kind, ac.Agent, collector.Options{Paths: ac.Paths, ProjectsDir: ac.ProjectsDir, FileGlob: ac.FileGlob, DBFile: ac.DBFile}); col != nil {
		src := col.Sources()
		if len(src) > 50 {
			src = append(src[:50], fmt.Sprintf("... 共 %d 个", len(col.Sources())))
		}
		out["sources"] = src
	}
	return out
}

func (a *App) PickDirectory(title string) (string, error) {
	return wailsRuntime.OpenDirectoryDialog(a.ctx, wailsRuntime.OpenDialogOptions{Title: title})
}

func (a *App) OpenConfigDir() {
	wailsRuntime.BrowserOpenURL(a.ctx, "file://"+filepath.ToSlash(config.Dir()))
}

func (a *App) OpenURL(url string) { wailsRuntime.BrowserOpenURL(a.ctx, url) }

// ---- 单价：读写服务端 ----

func (a *App) GetPricing() ([]client.PricingItem, error) {
	cfg := a.cfg.Get()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	items, err := client.New(cfg.ServerURL).GetPricing(ctx, "")
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Agent != items[j].Agent {
			return items[i].Agent < items[j].Agent
		}
		if items[i].Provider != items[j].Provider {
			return items[i].Provider < items[j].Provider
		}
		return items[i].Model < items[j].Model
	})
	return items, nil
}

func (a *App) SavePricing(items []client.PricingItem) error {
	cfg := a.cfg.Get()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return client.New(cfg.ServerURL).PutPricing(ctx, cfg.DeviceID, items)
}

# Token Monitor Client

AI Coding Agent Token 用量监控 · **客户端**（Wails 桌面应用，Windows / macOS）。

在每台需要统计的电脑上运行，定时读取本机 Claude Code / OpenCode / ZCode 等的用量记录，增量上报到 [token-monitor-server](../token-monitor-server)。只上传解析出来的 Token 计数与会话标题，不上传文件内容。

```text
本机 Agent 记录 ─► 客户端定时采集 ─► HTTP 上报 ─► 服务端（SQLite）─► 浏览器面板
```

## 功能

- **服务端与设备**：服务端地址（测试连接）、本机唯一标识 device_id（首次启动自动生成，可改/重新生成）、设备名称、采集间隔（1–1440 分钟）、回扫窗口、启动是否隐藏窗口。
- **Agent 路径**：卡片式配置，可新增 / 删除 / 启停。名称自填，claude / codex / opencode / zcode 有解析器，其他名称标「待适配」。可检测路径是否存在、发现了多少数据源。
- **模型单价**：读写服务端的全局单价（¥ / 百万 Tokens）。
- **运行状态**：连接状态、各 Agent 上轮采集结果、日志、单个 Agent 重扫。
- 关闭窗口即最小化到系统托盘，采集在后台继续；托盘菜单：打开窗口 / 立即同步 / 打开网页面板 / 退出。
- 游标本地持久化，上报成功才推进，服务端按 `(device_id, agent, message_id)` 去重，重复上报不会累计。

## 配置存哪里

**配置以服务端为准**：除「服务端地址」和「device_id」这两个引导字段外，全部配置（设备名、采集间隔、回扫窗口、Agent 路径、启动选项）都保存在服务端。客户端启动注册成功后自动拉取并采纳服务端配置；在设置页保存时会先推送服务端再生效。换机器 / 重装系统后，只要 device_id 不变，配置自动恢复。

本地只留一个引导文件（Windows `%APPDATA%\token-monitor\config.json`，macOS `~/Library/Application Support/token-monitor/config.json`，Linux `~/.config/token-monitor/config.json`），内容只有 `server_url` 和 `device_id` 两个字段——没有它客户端不知道连哪台服务端、用哪个设备身份。

## 构建

依赖：Go 1.22+、[Wails v2 CLI](https://wails.io/docs/gettingstarted/installation)（`go install github.com/wailsapp/wails/v2/cmd/wails@latest`）。前端是纯静态 HTML/JS，**不需要 Node**。

```bash
go mod tidy           # 首次拉取依赖并生成 go.sum
wails doctor          # 检查平台依赖（Windows 需要 WebView2，macOS 需要 Xcode CLT）
wails build           # 当前平台
```

产物在 `build/bin/`：Windows 是 `token-monitor.exe`，macOS 是 `token-monitor.app`。

- Windows 安装包（NSIS）：`wails build -nsis`
- macOS 通用二进制：`wails build -platform darwin/universal`
- 跨平台交叉编译：Windows 包可以在 macOS/Linux 上 `wails build -platform windows/amd64`；macOS 包必须在 macOS 上构建。
- 也可以用 `bash build.sh` / `bash build.sh windows`。

`build/` 目录缺少图标等资源时 `wails build` 会自动生成默认资源；想换图标替换 `build/appicon.png` 即可（托盘 / favicon 用同一张图，重新构建时同步到 `static`、`frontend/dist`、`internal/tray`）。

macOS 未签名的 app 首次打开需要：`xattr -cr token-monitor.app`。

## 数据来源与口径

| Agent | 常见位置 | 增量方式 |
| --- | --- | --- |
| Claude Code | `~/.claude/projects/*/*.jsonl` | 文件字节 offset |
| Codex | `~/.codex/sessions/**/*.jsonl` | 文件 offset + 累计计数器差值 |
| OpenCode | `~/.local/share/opencode/opencode.db`（Windows `%LOCALAPPDATA%\opencode`） | 时间游标 + 回扫窗口 |
| ZCode | `~/.zcode/cli/db/db.sqlite` | 时间游标 + 回扫窗口 |

SQLite 以只读方式打开；打不开（WAL + 网络盘）时自动用本地快照。拿不到真实 usage 的记录一律跳过，不估算。本机没装的 Agent（如 Codex）在「Agent 路径」页删除即可，默认配置也不再预置。

# 发布 Release 流程

发布一个新版本的完整步骤。产物构建与 Release 创建全部由 GitHub Actions（`.github/workflows/release.yml`）完成，本地不需要打包。

## 产物清单

推 `v*` tag 后 Actions 自动构建并创建 Release，附带：

| 产物 | 平台 | 说明 |
|---|---|---|
| `token-monitor-windows-amd64.zip` | Windows 10/11 x64 | 绿色版，解压即用（需系统有 WebView2 运行时，Win11 自带） |
| `token-monitor-macos-arm64.zip` | macOS Apple Silicon | `token-monitor.app`，未签名，首次打开需右键 → 打开 |
| `token-monitor-macos-amd64.zip` | macOS Intel | 同上 |

Release 说明由 `generate_release_notes: true` 按 commit 自动生成，可在 GitHub Release 页面手动补充。

## 发布步骤

### 1. 确认主干可用

```bash
go build ./... && go vet ./... && go test ./... -race
```

### 2. 更新版本号

- `wails.json` → `info.productVersion`（exe 元数据 / 文件属性里显示的版本）
- `internal/agent/runner.go` 的 `Version` **不用改**：CI 构建时经 `-ldflags "-X token-monitor-client/internal/agent.Version=<tag名>"` 自动注入（心跳上报给服务端的 `client_version` 就是它）。本地构建显示 `dev` 属正常。

### 3. 提交并推送

```bash
git add -A
git commit -m "..."
git push origin main
```

### 4. 打 tag 触发发布

```bash
git tag -a v1.1.0 -m "v1.1.0：Agent 数据目录自动扫描；游标可靠性加固"
git push origin v1.1.0
```

tag 必须是 `v` + 数字开头（`v*`），否则 workflow 不会触发。

### 5. 等 Actions 跑完并验收

1. 打开仓库 **Actions** 页，确认 `Release` workflow 三个 job（build-windows / build-macos / release）全绿，约 3–5 分钟。
2. 打开 **Releases** 页，确认新 Release 已生成，且三个 zip 附件齐全、体积正常（Windows 约 13 MB）。
3. 抽查：下载 Windows zip，替换本机运行，到设置页确认服务端心跳里的客户端版本号 = tag 名（验证 ldflags 注入生效）。

## 手动重跑

- Actions 页 `Release` workflow 有 `workflow_dispatch`，可手动触发构建（只产出 artifacts 供下载，**不会创建 Release**——release job 仅在 tag push 时执行）。
- 构建失败重跑：Actions 页对应 run → Re-run failed jobs。

## 已知限制

- **NSIS 安装包未接入 CI**：仓库里有 `build/windows/installer/project.nsi` 模板，但 runner 缺 `makensis`，目前只出 zip 绿色版。要出安装器需在 CI 里加 `apt install nsis` 后 `wails build -nsis`。
- **macOS 未签名未公证**：下载者会遇 Gatekeeper 拦截（右键 → 打开可过）。要正式分发需 Apple Developer 证书 + notarytool 步骤。
- 版本号需手动改 `wails.json`，tag 与它不一致只影响文件属性显示，不影响功能。

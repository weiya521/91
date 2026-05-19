# 视频聚合站

把夸克 / 115 / PikPak / 联通沃盘 / OneDrive 作为存储后端的视频聚合前台。按 `video-site-implementation-plan.md` 的设计实现。

- 前端：React 18 + Vite + TypeScript
- 后端：Go 1.23，SQLite（纯 Go 驱动，无 CGO），ffmpeg 生成 teaser 和封面
- 网盘接入：夸克自研 + 115driver SDK + PikPak 自研（参考 OpenList）+ wopan-sdk-go SDK + OneDrive（OpenList 在线续期 + Microsoft Graph 文件接口）

## 当前功能

- 前台需要登录后访问，支持首页、列表页、搜索、分类/标签筛选、分页、详情播放和相关推荐。首页“今日排行”按点赞热度排序并每 2 小时轮换一组，默认展示 10 个；“最新视频”按发布时间倒序展示最新 10 个。
- 视频卡片支持封面、画质、时长、点赞/点踩、移动端点按预览；列表页会记住筛选、分页和滚动位置。
- 播放页会在视频信息中显示来源网盘类型，并提供点赞、标签编辑和 **不再展示**。不再展示是全局隐藏：写入数据库后，该视频不会再出现在首页、列表、相关推荐中，详情接口也会返回 404。
- 管理后台支持网盘管理、视频管理、标签管理和运行时 Teaser 生成开关。
- 视频管理支持按网盘筛选、每页 100 条分页、每个网盘的 Teaser 已生成/待生成/失败统计、单条或全量重生 teaser、编辑标题/作者/分类/标签等元数据。
- 标签管理支持创建标签并自动分类已有视频；内置规则会把常见番号污染归并到 `AV` 等系统标签，降低标签列表噪声。
- 115 生成 teaser 时会顺序取链并分段生成，降低 CDN 403 / WAF 风控导致的大量失败概率；遇到疑似风控会进入冷却并保留任务为 `pending`。

## 快速开始

### 环境要求

- Node.js 18+ 和 npm
- Go 1.23+
- ffmpeg 和 ffprobe（用于生成预览 teaser 和抽封面）

Windows 用户可以把 Go 和 ffmpeg 解压到 `%USERPROFILE%\tools\`，然后把 `\tools\go\bin` 和 `\tools\ffmpeg\bin` 加到 PATH 即可，不需要管理员权限。

### 运行

Linux / WSL 环境推荐用仓库根目录的脚本同时启动前后端：

```bash
npm install
./start.sh               # 前端 9191，后端 9192；默认使用生产预览模式，无热更新
./start.sh --status      # 查看运行状态
./start.sh --restart     # 重启
./start.sh --stop        # 停止
```

如果需要开发热更新，可临时使用 `FRONTEND_MODE=dev ./start.sh --restart`。

也可以分两个终端手动启动：

```bash
# 前端
npm install
npm run build
npm run preview          # 监听 http://127.0.0.1:9191，无热更新

# 后端（另开终端）
cd backend
go run ./cmd/server      # 默认监听 127.0.0.1:9192，依赖已 vendor 入库，无需 go mod tidy
```

首次启动后端会自动生成：

- `backend/config.yaml`（从 `config.example.yaml` 复制）
- `backend/data/video-site.db`（SQLite）
- `backend/data/previews/`（teaser 和封面本地目录）

Vite dev / preview server 都已配置把 `/api`、`/p`、`/admin/api` 反代到 `127.0.0.1:9192`。浏览器访问 `http://127.0.0.1:9191/` 进入前台，`/admin` 进入管理后台（默认 `admin` / `admin123`，请在 `backend/config.yaml` 里改）。如果本地已经存在旧的 `backend/config.yaml`，请确认 `server.listen` 与 Vite 代理端口一致。

## 目录

```
.
├─ src/                       React 前端
├─ backend/                   Go 后端（单体服务）
│  └─ vendor/                 Go 依赖全量源码，入库，支持完全离线构建
├─ OpenList-4.2.1/            OpenList 完整源码，网盘协议对接参考
├─ tests/                     前端纯逻辑测试
├─ start.sh                   本地前后端启动脚本
├─ video-site-implementation-plan.md    完整的设计和实现记录
└─ README.md
```

### 依赖管理

所有 Go 依赖都已通过 `go mod vendor` 打包进 `backend/vendor/` 并入库。别人 clone 仓库后，**无需联网**，直接 `go run ./cmd/server` 就能编译运行。

升级依赖的流程：

```bash
cd backend
go get github.com/SheltonZhu/115driver@<新版本>
go mod tidy
go mod vendor        # 把新依赖同步到 vendor 目录
git add vendor/      # 入库
```

### `vendor-refs/` 要不要在意？

不需要。它只存 OpenList 源码作协议参考，删除或保留都不影响项目编译。

## 加一个网盘

1. 登录 `/admin` → 网盘管理 → 新建
2. 选类型（夸克 / 115 / PikPak / 沃盘 / OneDrive），填名称 + 凭证
3. 保存后会自动触发一次扫描
4. 在 `/admin/videos` 里看扫到了多少视频
5. 侧栏底部 **Teaser 生成** 开关开着，就会按配置给每个视频生成封面和多段 teaser

各网盘的凭证字段：

| 类型 | 凭证字段 | 获取方式 |
|---|---|---|
| 夸克 | `cookie` | pan.quark.cn 登录后 F12 拷 Cookie |
| 115 | `cookie` | 115.com 登录后拷 Cookie（`UID=...; CID=...; SEID=...; KID=...`） |
| PikPak | `username`、`password`，可选 `refresh_token`、`captcha_token`、`device_id`、`platform`、`disable_media_link` | 参考 OpenList PikPak driver；首次登录成功会自动回写 token |
| 沃盘 | `access_token`、`refresh_token`、可选 `family_id` | 第一版只能手动粘贴 token；后续会加扫码/短信登录 |
| OneDrive | `refresh_token`，可选 `access_token`、`api_url_address`、`region`、`is_sharepoint`、`site_id` | 按 OpenList 默认方式调用 `https://api.oplist.org/onedrive/renewapi` 在线刷新 token；`rootId` / `scanRootId` 默认填 `root`，SharePoint 需填 `is_sharepoint=true` 和 `site_id` |

### 115 说明

115 的下载直链对同一个 CDN URL 的多段随机读取比较敏感，尤其是大文件生成多段 teaser 时，容易出现 `403 Forbidden`、WAF 阻断、`moov atom not found` 或 `partial file`。后端对 115 做了专门处理：

- 取流优先使用移动端下载接口，失败再回退到原 chrome 下载接口。
- 生成 teaser 时不再让 ffmpeg 同时打开多个 115 直链；每个 3 秒片段会单独取链、单独生成本地小片段，最后在本地 concat。
- ffmpeg 访问 115 CDN 时会经过进程内本地代理转发 Range 请求，避免直接暴露签名 URL，并统一处理必要请求头。
- 如果 115 返回 403 / 405 / WAF 阻断 / `moov atom not found` / `partial file` 等疑似临时风控错误，当前网盘的封面/teaser worker 会进入默认 5 分钟冷却，当前任务保持 `pending`，避免继续请求导致更多失败。

管理后台的“重生失败 teaser”会把 `failed` 重置为 `pending` 并入队。一次性重生大量 115 视频仍可能触发上游风控；建议点一次后观察日志，如果出现 `transient media source error until=...`，等待冷却结束再继续，不要反复点击。

### PikPak 速度说明

PikPak 的 `disable_media_link` 默认按 `true` 处理，会使用 `web_content_link` 原始下载链接；在当前服务器实测，单连接通常只有约 2.8-3 MiB/s。把该字段设置为 `false` 后，后端会改用 `usage=CACHE` 返回的 media/cache 链接，当前服务器实测 `/p/stream` 64 MiB Range 可到约 8.9 MiB/s。

当前服务器同时存在 sing-box TUN 透明代理，PikPak 默认出站会被 `tun0` 接管；但强制直连物理网卡并没有更快，慢速的主要差异来自 PikPak 取链方式。media/cache CDN 节点仍有波动，偶尔可能遇到慢节点；如果播放变慢，可重新获取直链或重新挂载 PikPak 后再测。

### OneDrive 说明

OneDrive 当前采用 OpenList 在线 API 的续期方式，不要求用户提供 Azure 应用的 `client_id` / `client_secret` / `redirect_uri`。配置时至少填 `refresh_token`；如使用 OpenList 代刷获得的 token，可把 refresh token 填到本项目。普通 OneDrive 的 `rootId` / `scanRootId` 推荐填 `root`，SharePoint 文档库需额外设置 `is_sharepoint=true` 和 `site_id`。

## Teaser 和封面生成策略

- 封面：固定从第 5 秒抽一帧 jpg，不再为封面单独探测视频时长
- Teaser：每段固定 3 秒；30 秒以下最多 3 段，30 秒及以上固定 4 段；长视频在 20% 到 80% 区间均匀取段
- 生成的封面和 teaser 都只保存在本地 `backend/data/previews/`，不会回写到网盘；旧数据中的 `preview_file_id` 会被忽略
- 极短视频会按可容纳的完整 3 秒片段数自动降级
- 首次失败的任务标 `preview_status = failed`，不再自动重试；管理后台可手动重新生成
- 封面或 teaser 生成遇到明确频率限制（如 429）时，对应 worker 固定冷却 5 分钟。
- 服务启动或网盘重新挂载时，如果 Teaser 开关已开启，会自动把历史 `pending` 任务重新入队，避免重启后停在“待生成”。
- 115 使用顺序分段生成：每段独立取链、独立转码，最后本地拼接，避免同一 115 CDN 链接被多输入并发读取。
- OneDrive 直链生成 teaser 时可能触发 Microsoft 429 限流；后端会识别这类错误并让当前网盘进入冷却期，保留任务为 `pending`，避免连续请求触发更严重限流。
- 115 直链生成 teaser 时如果触发 403 / WAF / 截断数据等临时错误，也会让当前网盘进入冷却期，保留任务为 `pending`。
- 详见 plan 15.12 节

## 常用管理能力

- `/admin/drives`：新增/编辑/删除网盘，触发扫描。
- `/admin/videos`：按网盘查看视频、分页浏览、查看各网盘 Teaser 统计、编辑元数据、重生 teaser。
- `/admin/tags`：新增标签并自动匹配已有视频。
- 播放页：视频信息会显示来源网盘类型；“不再展示”是全局隐藏功能。当前没有恢复入口，如需恢复可直接把数据库中对应视频的 `hidden` 字段改回 `0`，后续可在管理后台补恢复 UI。

## 验证

```bash
npm run lint
npm run build
node --test tests/previewIntent.test.ts

cd backend
go test ./... -count=1
```

## 部署到 Linux

```bash
# 本机交叉编译
cd backend
GOOS=linux GOARCH=amd64 go build -o video-server ./cmd/server

# 目标服务器
sudo apt install ffmpeg
scp video-server user@host:/opt/video-site/
# 配 systemd + nginx 反代到 /、/api、/p、/admin
```

完整部署方式见 plan 15.10 节。

## 贡献

任何代码改动请保持和 `video-site-implementation-plan.md` 同步；重要的设计决策追加到第 14 节（实现备注）或第 15 节（后端）。

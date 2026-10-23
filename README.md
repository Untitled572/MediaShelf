# MediaShelf

个人影片收藏管理工具。把本地文件、NFO 元数据、演员收藏、处理进度和下载候选放在一个界面中管理。

主服务运行在 Docker 中，Windows 连接程序负责扫描本机目录、连接本机 qBittorrent 和启动桌面工具。影片留在原来的磁盘上，不上传到服务器。

[源码](https://github.com/Untitled572/MediaShelf) · [容器镜像](https://github.com/Untitled572/MediaShelf/pkgs/container/mediashelf) · [验收记录](docs/ACCEPTANCE.md)

## 功能

| 模块 | 支持内容 |
| --- | --- |
| 媒体库 | 读取 JavSP 等工具生成的 NFO、封面和演员头像；按演员、年份、评分筛选；同一作品的多个版本和分段合并展示 |
| 处理进度 | 分别标记压制、裁剪、转录汉化和验收状态，支持按文件记录和批量编辑；个人评分 0–5 分，支持半分 |
| 演员收藏 | 收藏星标、表头排序、尽量收全／精选收藏／仅关注策略；查看缓存的百科、中文简介与公开社交账号 |
| 收集计划 | 根据演员策略和已同步目录列出未拥有的作品，支持暂缓、恢复和查看候选提交状态 |
| 发现与推荐 | 根据演员、标签、个人评分和来源评分推荐作品及演员；每批最多 5 部作品、3 位演员 |
| 下载管理 | 连接服务器或 Windows 设备上的 qBittorrent；分类／标签隔离、启停、限速、添加与重复检查 |
| RSS | 定时发现候选，支持包含／排除规则；人工审核后提交下载 |
| 本地工具 | 启动已配置的 JavSP、BlackWhiteSplitter、WhisperJAV，后续操作在各工具内完成 |
| 数据与隐私 | SQLite 持久化、元数据备份、48 小时登录、默认隐藏图片的 NSFW 隐私模式 |

设备离线时仍可浏览已入库资料、评分、处理进度和缓存百科；离线文件不能播放。主服务本身需要保持可访问。

## Docker 部署

发布镜像：`ghcr.io/untitled572/mediashelf`，当前构建平台为 **Linux amd64**。版本标签如 `v0.1.1` 用于固定版本，`latest` 跟随稳定版本。

### 使用预构建镜像

```sh
git clone https://github.com/Untitled572/MediaShelf.git
cd MediaShelf
cp .env.example .env
```

编辑 `.env`，将 `MEDIASHELF_PASSWORD` 替换为至少 12 位的服务密码，然后运行：

```sh
docker compose -f compose.ghcr.yaml pull
docker compose -f compose.ghcr.yaml up -d
docker compose -f compose.ghcr.yaml ps
```

默认入口为部署设备的 `http://127.0.0.1:18765`。远程访问可使用 HTTPS 反向代理，或建立 SSH 隧道：

```sh
ssh -N -L 8765:127.0.0.1:18765 user@server
```

随后打开 `http://127.0.0.1:8765`。反向代理与服务在同一宿主机时，可将上游指向 `http://127.0.0.1:18765`；代理运行在另一容器内时，需使用该容器可达的服务地址。

| 环境变量 | 默认值／说明 |
| --- | --- |
| `MEDIASHELF_PASSWORD` | 必填，服务登录密码 |
| `MEDIASHELF_BIND` | `127.0.0.1`，宿主机绑定地址 |
| `MEDIASHELF_PORT` | `18765`，宿主机端口 |
| `MEDIASHELF_IMAGE` | `ghcr.io/untitled572/mediashelf:v0.1.1`，可在 `.env` 中指定其他镜像版本 |
| `MEDIASHELF_ALLOW_SHORT_PASSWORD` | `false`；显式设为 `true` 可接受 6–11 位密码，仍不允许空密码 |

持久数据保存在 `mediashelf-data` 卷中，不需要把影片目录挂载到主服务。

### 从源码构建

```sh
docker compose up -d --build
```

Linux 宿主机上的代理如果只监听 `127.0.0.1`，可改用 host 网络方案：

```sh
docker compose -f compose.host.yaml up -d --build
```

host 方案仍只监听 `127.0.0.1:18765`。三种 Compose 文件是替代方案，选择其中一个使用，不同时启动。

## 配对 Windows 设备

需要 Go 1.26、Node.js 24 和 npm。在仓库根目录运行：

```powershell
./scripts/build.ps1
```

生成 `dist/mediashelf-server.exe` 和 `dist/mediashelf-agent.exe`。主服务已部署在 Docker 时，只需运行设备连接程序。

1. 在 WebUI 的「设备与目录」注册设备，保存仅显示一次的设备令牌。域名／IP 可填写稳定地址，用于设备标识；连接仍通过令牌认证。
2. 为该设备添加媒体目录，填写 Windows 上的绝对路径，并记录目录 ID。
3. 复制 `agent.example.json` 为本地 `agent.json`，填写服务地址、设备令牌以及目录 ID 对应的本机路径。
4. 启动连接程序：

```powershell
./dist/mediashelf-agent.exe --config ./agent.json --tray
```

`libraries`、`tools`、`downloaders` 是本机白名单。网页中的配置与白名单必须一致，服务器不能单方面授权任意本地路径或程序。完整示例见 [agent.example.json](agent.example.json)，参数说明见 [设备连接程序文档](cmd/agent/README.md)。

连接程序在当前 Windows 登录会话中运行，工具窗口才会显示到桌面。配置工具时，先在网页添加工具，再将工具 ID、实际程序路径和工作目录写入本机白名单。工具按钮只负责打开程序，不会自动执行裁剪、汉化或将进度标为完成。JavSP 启动后可能按其自身配置开始工作，使用前请检查 JavSP 配置。

### 扫描与文件记录

- 读取 `<影片名>.nfo` 或 `movie.nfo`、封面与演员头像；不移动、重命名或删除影片。
- 定时扫描默认间隔 60 分钟，也可从网页或托盘手动扫描。
- 完整扫描成功后才判断文件是否缺失；设备离线不会清空索引。
- 修改目录根路径后，旧范围记录会退出当前片库；重新扫描用于建立新目录的索引。
- 删除设备前会展示影响范围并要求输入设备名。删除会永久清理关联数据库记录；共享作品保留其他设备上的文件及元数据，磁盘文件不受影响。

如需浏览或播放文件，可在片库中配置浏览器可访问的 OpenList 基础地址与目录前缀。MediaShelf 不提供影片转码服务。

## qBittorrent 与 RSS

服务器可以直连远程 qBittorrent，也可以通过指定 Windows 设备访问本机下载器。通过设备连接时，本机 `downloaders` 白名单必须包含相同的下载器 ID 与 URL。

- **本机分类管理**：默认只管理 `2D`、`VR`，可通过 `managed_categories` 配置；添加任务选择已有分类并启用 autoTMM，保存路径由 qBittorrent 分类决定。
- **远程无分类管理**：使用独立管理标签，默认 `MediaShelf`；只读取和操作带该标签的任务，新提交任务自动打标。既有其他任务不会被自动纳入。
- **Syncthing**：同步后的本地目录作为独立片库扫描；Windows 同步路径不作为远端下载路径提交。

提交候选时会检查本地编号及目标队列中的重复项。RSS 每小时发现候选，收集策略和推荐也只生成资料或计划，**不会自动下载**。移除下载任务始终保留数据文件。

## 数据源与演员资料

已接入 JavDB UI、JavDB、JavLibrary、FANZA、JavBus、Jav321，不同来源的功能各有区别：

| 来源 | 用途 |
| --- | --- |
| JavDB UI | 公开最新／推荐数据、编号详情和演员目录 |
| JavDB、JavLibrary、FANZA、JavBus | 作品检索与演员目录，受站点和网络可用性限制 |
| Jav321 | 编号检索 |
| Wikipedia、Wikidata | 演员百科、公开资料与已收录的社交账号 |

「更新推荐数据」获取外站元数据；「换一批」轮换已计算的结果。推荐不调用大模型。演员推荐仅接纳来源明确标记为女性的记录，未知或冲突记录不补入推荐。

「尽量收全」将已同步目录中未拥有的普通作品纳入收集计划；「精选收藏」按评分补足目标数量；「仅关注」不生成计划。目录同步有分页和超时上限，“完成”只表示本次来源分页读取结束，不保证覆盖演员全部作品。

演员简介优先使用同一 Wikidata 条目对应的中文维基；没有中文简介时，通过 MyMemory 翻译公开百科文字，保留原文和来源。机器翻译有额度和失败的可能，失败时继续显示缓存或原文。不根据姓名猜测社交账号。

JavLibrary 提供想看、收藏作品和收藏演员的**只读导入入口**，可在设置中填写列表 URL，必要时填写 Cookie。导入影片作为待审核资料，不会直接下载；本地演员收藏回写 JavLibrary 尚未启用。

外站验证、403、TLS 错误或解析失败会明确显示。程序不绕过浏览器验证，也不关闭证书校验。

## 网络、登录与隐私

设置页默认开启全局代理，初始地址为 `http://127.0.0.1:7890`，可修改、关闭或测试。元数据、百科、RSS 及服务端获取的在线图片使用该代理；设备和 qBittorrent 连接直连。Docker 桥接网络中的 `127.0.0.1` 指向容器自身，请配置容器可访问的代理，或使用前述 host 方案。

本地封面优先；在线封面优先采用来源提供的小图。普通在线图片返回 302，**由浏览器直连原始地址，不经过服务端代理**；加密图源保留服务端解码与缓存。图片直连可用性取决于浏览器网络和来源站点。

新浏览器默认开启 NSFW 隐私模式，移除封面、头像和外站 iframe，图片不占位。文字仍可见，开关不清除已存在的浏览器缓存，也不控制外站新窗口。

服务密码登录后会话固定有效 48 小时；支持输入未过期令牌登录和勾选「记住登录」。令牌登录不会延长原有效期。不要将 `agent.json`、`.env`、登录令牌、Cookie 或整个数据目录提交到仓库。

## 备份与升级

设置页 JSON 备份用于元数据迁移，恢复时按 ID 合并；不包含影片、封面文件或登录凭据。完整备份应先停止服务，再复制整个数据卷或 `MEDIASHELF_DATA_DIR`，避免只复制运行中的 SQLite `.db` 而遗漏 WAL。

使用镜像部署时，先备份数据，再调整 `.env` 中的 `MEDIASHELF_IMAGE` 并执行：

```sh
docker compose -f compose.ghcr.yaml pull
docker compose -f compose.ghcr.yaml up -d
```

保留原数据卷即可沿用数据库。不要在需要保留数据库时执行 `docker compose down -v`。

## 开发与发布

```sh
go test ./...
go vet ./...
cd web
npm ci
npm test
npm run build
```

本地启动服务端时，设置 `MEDIASHELF_PASSWORD` 后运行 `go run ./cmd/server`，默认监听 `127.0.0.1:8765`。前端开发使用 `web` 目录中的 `npm run dev`，API 转发到该服务。

GitHub Actions **仅在推送标签时触发**。Windows／Linux 测试、静态检查和前端测试构建通过后，构建并推送 GHCR 镜像；版本标签保留 `v` 前缀，同时提供无前缀版本与稳定版 `latest`。镜像携带源码链接、提交信息和构建来源证明。

历史实测及未验证项见 [docs/ACCEPTANCE.md](docs/ACCEPTANCE.md)，不能用适配器单元测试代替外站实时可用性验证。

## 许可证

[GPL-3.0](LICENSE)。外部工具不随项目分发，第三方来源及参考见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。

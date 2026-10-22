# MediaShelf

个人影片收藏管理：封面与 NFO 导入、演员目录、独立处理进度、个人评分、Windows 工具启动、qBittorrent 管理、RSS 候选和可解释推荐。

## 部署结构

Docker 主服务保存 SQLite、封面和元数据。Windows 设备连接程序主动连接主服务，扫描本机片库并在当前登录会话中启动已配置的软件。服务器不需要挂载 Windows 磁盘，也不会接收影片文件。

- WebUI/API：Go + Vue 3 / TypeScript
- 数据库：SQLite，WAL 模式
- 设备：Windows，命令行或托盘运行
- 外部工具：现有 JavSP、BlackWhiteSplitter、WhisperJAV，不修改原项目

## Docker

复制 `.env.example` 为 `.env`，设置至少 12 位随机密码。运行：

```sh
docker compose up -d --build
docker compose ps
```

默认只监听服务器的 `127.0.0.1:18765`。通过可信私网、HTTPS 反向代理或 SSH 转发访问。例如：

```sh
ssh -N -L 8765:127.0.0.1:18765 user@server
```

然后打开 http://127.0.0.1:8765 。`MEDIASHELF_BIND` 可设为指定局域网 IP；不要将未加 TLS 的管理页面直接公开到互联网。

## Windows 本地开发

需要 Go（版本见 go.mod）、Node.js 24 及 npm。

```powershell
./scripts/build.ps1
$env:MEDIASHELF_PASSWORD = '替换为至少12位的密码'
./dist/mediashelf-server.exe
```

默认 URL 为 http://127.0.0.1:8765 。可通过 `MEDIASHELF_DATA_DIR` 和 `MEDIASHELF_WEB_DIR` 更改数据及静态资源目录。

## 配对设备与扫描

1. 登录后，在“设备与目录”新增设备，保存仅显示一次的设备令牌。
2. 添加片库，选择设备并填写它能访问的绝对目录。复制片库 ID。
3. 复制 `agent.example.json` 为 `agent.json`，填写服务器地址、令牌及本机目录白名单。服务端配置和本机白名单必须同时允许访问。
4. 运行 `mediashelf-agent.exe --config agent.json`。连接程序会周期扫描，也可从网页手动发起。
5. 配置工具时，把网页工具 ID 对应到本机白名单里的实际程序路径和工作目录。

每次扫描只读影片目录。读取 `<影片名>.nfo` 或 `movie.nfo` 和封面；影片不移动、不重命名、不删除。设备离线不意味着文件丢失，只有成功的完整扫描才标记缺失。

同一作品可以有多个文件或分段。压制、裁剪、转录汉化分别记录，可设为无需处理。全部必要步骤完成后手动验收。作品级批量修改会应用于其可用文件，单个文件进度仅影响该文件。个人评分为 0–5 分，支持半分，未评分与 0 分不同。

## 工具与 OpenList

工具按钮只启动已有程序，不传入远程 shell 命令，也不自动认定处理完成。连接程序需要运行在交互登录会话中；Windows 服务/Session 0 无法正常向用户桌面显示这些窗口。

JavSP 可能在启动后按它自己的配置开始扫描，请先确认其配置。裁剪和汉化仍在各软件内操作。MediaShelf 不覆盖这些软件的配置。

OpenList 在片库配置中填写网页基础地址及目录前缀，影片详情提供跳转。该地址由访问 WebUI 的浏览器打开，应选择浏览器能够访问的地址。

## 下载与订阅

下载器可由服务器直连，也可通过指定 Windows 设备连接。后者必须在该设备的 `downloaders` 白名单里配置相同下载器 ID 和 URL。账号密码保存在服务端数据目录，API 列表和任务日志不返回密码。

RSS 每小时发现候选，支持包含/排除正则。候选不会自动下载。提交时检查本地作品和目标下载队列；疑似重复、提交结果不确定时提示处理，避免盲目重试。删除任务始终保留数据文件。

## 演员与数据源

每位演员可选尽量收全、精选收藏或仅关注。目录适配器面向 JavDB、JavLibrary 和 FANZA；可设置站点基础地址、代理与账号会话。外站限制、网页变动或解析失败会显示错误，不把失败当作空目录。

目录同步的“完成”仅指此次来源的可访问分页已经读完，不保证覆盖演员历史全部作品。推荐基于本地评分、演员/标签偏好和外站评分，展示理由，不调用大模型。

## 备份与升级

界面 JSON 备份用于元数据迁移，恢复按 ID 合并覆盖对应记录，不清空其余记录。它不包含视频、封面文件和登录凭据。

完整恢复请备份 Docker 的 `mediashelf-data` 卷或本地 `MEDIASHELF_DATA_DIR`，包括数据库及 covers。运行中的 SQLite 不应只复制单个 `.db`：先停止服务再复制整个数据目录，恢复后重新启动。升级前保留完整备份，再构建并启动新版本。

## 验证

```sh
go test ./...
go vet ./...
cd web && npm ci && npm run build
```

CI 同时检查 Windows、Linux、前端和 Docker 构建。真实环境验收记录见 `docs/ACCEPTANCE.md`；模拟 HTTP 测试不能替代真实站点可用性验证。

## 默认隐私模式

新浏览器默认开启 NSFW 隐私模式。顶栏或设置页可切换，选择保存在当前浏览器。开启时不创建封面、推荐和详情图片元素，也不加载外站 iframe；文字仍可见。已加载过的图片可能仍留在浏览器缓存，隐私模式不会清理历史和缓存。外站新窗口不受本应用开关控制。

## 分类下载和 Syncthing

本机下载器选择“按分类自动管理”，添加任务必须选择 qBittorrent 已有分类；应用启用 autoTMM，不覆盖分类保存路径。远程下载器选择“无分类”，保持分类为空，沿用远端默认路径。Syncthing 同步目录作为独立本地片库扫描，不把 Windows 同步路径提交给远端 qBittorrent。

目录索引只说明发现了文件，不代表下载、校验或后期处理已完成。每个版本独立记录处理进度；无 NFO 暂存副本不会覆盖已有 NFO 元数据。Syncthing 的临时文件不作为视频索引。

## 此部署的桌面入口

本机运行 `pwsh -NoLogo -NoProfile -File ./scripts/start-desktop.ps1`，会复用或建立 SSH 隧道、启动允许目录内的 Windows 连接程序并打开浏览器。默认读取忽略目录 `data/production-agent.json`；其它部署通过脚本参数指定 SSH 目标、端口和配置路径。没有自动安装开机启动。

Linux 宿主机代理只监听 `127.0.0.1` 时，可选择 `docker compose -f compose.host.yaml up -d --build`。此方案使用 host 网络，服务仍只监听 `127.0.0.1:18765`，通过 SSH 转发访问；不同时启动默认 Compose 与 host 方案。默认桥接 Compose 仍可使用容器可达的代理地址。健康检查会跟随服务端端口。

如需使用 6–11 位服务密码，须同时设置 `MEDIASHELF_ALLOW_SHORT_PASSWORD=true`。默认仍要求至少 12 位；启用此选项保留所有接口的登录验证，不接受空密码。

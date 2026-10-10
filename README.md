# RemoteGate

**[部署方式导航](DEPLOYMENT.md)** · [宝塔部署](docs/deployment/baota.md) · [泛域名证书与检测](docs/deployment/external-https.md) · [版本下载](https://github.com/yehao9589/remotegate/releases)

RemoteGate 是一个面向 iStoreOS/OpenWrt 的自建远程访问 MVP。路由器上的 Agent 主动连接国内服务器；浏览器访问不同域名时，服务端把 HTTP 请求通过这条长连接转给对应设备和内网地址。

```text
router.example.com ─┐
lucky.example.com  ─┼─> RemoteGate Server <==TLS/WebSocket==> OpenWrt Agent ─> 127.0.0.1:80 / :16601
nas.example.com    ─┘                                                   └─> 192.168.1.20:5000
```

## 第一版能力

- 设备创建、独立 Token、在线状态和断线重连
- 一个设备配置多个访问域名；每条映射独立保存公网协议、端口、内网目标、备注和启停状态
- 设备卡片内可直接打开、复制、编辑、连通性检测、停用或删除映射
- 每个访问域名映射一个内网 HTTP/HTTPS 地址
- 请求头、查询参数、Cookie 和 16 MiB 以内请求/响应体转发
- Agent 默认只允许回环、私网和链路本地目标
- Linux Agent 可设置 `SO_BINDTODEVICE` 与 `SO_MARK`
- OpenWrt `procd` 开机守护和 OpenClash/防火墙重载触发器
- 首次安装通过网页创建管理员，密码以 bcrypt 哈希保存，登录使用 HttpOnly 会话 Cookie
- 公网 HTTPS 检测：证书在宝塔 / 反向代理申请、部署与续期，后台分别验证主域名和映射实际返回的证书；原有内置 HTTPS 服务保留兼容

当前版本不支持目标站点的 WebSocket、流式下载、SSH/RDP/VNC 和多服务端高可用。这些属于后续版本。

## 服务端启动

Linux 服务器使用 Docker Compose，拉取内置路由器安装包的服务端镜像：

```sh
git clone https://github.com/yehao9589/remotegate.git
cd remotegate
cp .env.example .env
docker compose pull
docker compose up -d
```

编排使用 Linux host 网络，HTTP 只监听 `127.0.0.1:18088`，不需要数据库。已有宝塔网站时按[宝塔部署](docs/deployment/baota.md)启动容器，在宝塔申请并启用主域名与泛域名证书，再通过 RemoteGate 检测。原有独立 HTTPS 启动配置保留兼容，选择对应教程，不混用步骤。

常规部署默认使用 `ghcr.io/yehao9589/remotegate:stable`，跟随最新通过检查的构建。宝塔 compose 与 `.env` 都使用 `:stable`；旧 `.env` 的固定版本会覆盖 compose 默认值。更新时需要重新拉取镜像并重新部署，单纯重启不会升级，数据目录保持不变。只有需要锁定版本时才使用 `:v版本号`。

从 v0.1.3 开始，本机宝塔默认反代可直接使用 `CONSOLE_HOST`，同域名默认 HTTP/HTTPS 入口均可安装和登录；自定义外部端口用 `PUBLIC_URL` 指定，不带后台路径。切换协议无需反复改配置，登录 Cookie 跟随实际访问协议。来源校验仍然启用，不自动信任外部客户端或转发请求头。使用方式见部署指南；旧版镜像需先升级。

GHCR 拉取不便时，在源码目录运行：

```sh
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

首次安装账号默认填写 `admin`，可以设置密码和后台入口路径（默认 `/admin`）。完成后请收藏新地址，根路径和 `/install` 不再提供新的后台入口。在“账号与入口”中可修改入口或密码，保存时需验证当前密码，所有会话会退出。旧安装保留原账号与入口。

按所选访问方案打开安装页，设置管理员账号、密码和确认密码。账号为 3–32 位字母、数字、下划线、点或短横线，密码至少 8 个字符且不超过 72 字节。创建后自动登录，后续使用保存的后台路径；完成后无法再次通过 `/install` 创建管理员。

账号保存在 `data/admin.json`，重启、重建容器或升级时保留整个 `data` 目录。登录会话有效期为 12 小时，服务重启后需要重新登录。旧二进制部署如果仍设置 `ADMIN_USERNAME` 和 `ADMIN_PASSWORD`，首次启动会迁移为哈希配置；已有 `admin.json` 时以保存的账号为准。

DNS 中可以将 `*.remote.example.com` 解析到服务器公网 IP。将 `CONSOLE_HOST` 设为控制台的完整域名。

## 创建设备和映射

打开控制台，使用首次安装时创建的管理员账号登录：

1. 添加设备，保存页面仅显示一次的设备 ID 和 Token。
2. 在设备卡片内添加域名映射，例如公网 `https://router.remote.example.com/` → 内网 `http://127.0.0.1:80`。默认 HTTPS / 443，证书由公网入口提供；使用其他端口时按实际入口填写。
3. 从后台获取一键安装命令，或下载插件在 iStore 手动安装。完整操作见[路由器接入教程](docs/deployment/router.md)。

## 构建 OpenWrt Agent

### iStore 手动安装包

运行 `go run ./scripts/package-istore.go`，生成 `dist/RemoteGate-0.1.0-3-istore.run`，可在 iStore 的“手动安装”中上传。升级会保留 `/etc/config/remotegate` 中的连接配置。

路由器“服务 → RemoteGate”页面包含运行状态、插件版本、折叠的三步接入说明、基础配置和高级设置。状态刷新只更新概览，不会覆盖未保存的表单。进程运行状态不等同于隧道在线状态，后者在服务端设备工作台确认。出口网卡默认留空。

界面源文件位于 `deploy/openwrt/luci/`，与安装包使用同一份文件。测试虚拟机的后台为 `http://localhost:18080/cgi-bin/luci/admin/services/remotegate`，虚拟机连接宿主机服务器时填写 `http://10.0.2.2:18088`。

Windows：

```powershell
./scripts/build-openwrt.ps1
```

输出位于 `dist/`，包括 amd64、arm64 和 armv7 静态二进制。修改 `agent.json.example`：

```json
{
  "serverUrl": "https://remote.example.com",
  "deviceId": "控制台生成的设备 ID",
  "token": "控制台生成的设备 Token",
  "interface": "eth1",
  "mark": 51820,
  "allowPublicTargets": false
}
```

`interface` 必须是 Linux 实际出口设备，例如 `eth1` 或 `pppoe-wan`，不是 LuCI 中显示的逻辑接口名称。可通过下面命令查询：

```sh
ubus call network.interface.wan status | jsonfilter -e '@.l3_device'
```

将二进制安装为 `/usr/sbin/remotegate-agent`，配置保存为 `/etc/remotegate/agent.json`，并安装 `deploy/openwrt/` 下的 init 与 route guard 文件。

路由保护脚本使用 mark `51820` 和路由表 `51820`，并在检测到 OpenClash 输出链时把同一 mark 插到链首。部署前如已有同编号策略路由，请修改脚本和 Agent 配置。

## 开发运行

### 版本发布

版本信息统一维护在 `internal/buildinfo/release.json`：`version` 是服务端与客户端版本，`packageRevision` 是 OpenWrt 插件包修订号。修改插件打包内容时递增修订号。`CHANGELOG.md` 记录各发布版本的变更、更新步骤与限制。

发布流程：更新版本及更新记录，提交并推送，通过检查后推送对应 `v版本号` 标签。GitHub Actions 会核对标签与源码版本，完成真实容器测试，再发布 AMD64 / ARM64 镜像和 GitHub Release。Release 附件只提供服务端 Compose 部署包和校验文件；路由器插件内置在服务端镜像中，通过管理后台下载或后台生成的一键安装命令获取，不单独发布到 GitHub Release。

```sh
git tag v0.1.0
git push origin v0.1.0
```

不能对已经发布的版本移动标签或覆盖附件；修复应发布新版本。后台“安装包与版本”可查看运行版本、构建提交、插件版本及发布记录。

```powershell
$env:ADMIN_USERNAME='admin'
$env:ADMIN_PASSWORD='development-password'
$env:CONSOLE_HOST='console.localhost'
go run ./cmd/server
```

```powershell
go test ./...
```

生产环境必须使用 HTTPS，保持证书验证开启，并限制 `/data/state.json` 的读取权限。

## 域名与 HTTPS 检测

证书在宝塔 / 反向代理申请、部署与续期。后台只检测公网入口，支持：

- 多域名搜索、筛选和分页，按需展开解析接入、HTTPS 检测与检测记录。
- 分别检测主域名和具体映射地址的 DNS、参考 IP、实际证书信任链、域名与有效期。
- 显示颁发机构、覆盖域名和到期时间；错误入口仍展示其实际返回的证书，验证失败不会显示为可用。
- 按域名与端口持久保存最近 40 个地址的最新结果，刷新后保留，超过 24 小时提示复检，临近到期提醒。
- 新增域名默认使用外部证书入口；外部管理域名不执行 RemoteGate 自动续期。

主域名检测通过不会自动证明子域名可用；HTTPS 检测也不证明设备在线或内网服务正常。详见[宝塔泛域名证书与检测](docs/deployment/external-https.md)。无需主机助手或宝塔 API。

原有内置 HTTPS 配置和证书文件保留兼容，显式选择 RemoteGate 的独立部署可继续运行已有服务；新的检测页面不提供申请、导入、续期或迁移入口的按钮。

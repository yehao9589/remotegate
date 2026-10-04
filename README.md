# RemoteGate

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

编排使用 Linux host 网络，HTTP 安装入口只监听 `127.0.0.1:18088`，内置 HTTPS 默认监听 443。先通过 SSH 隧道进入后台并申请第一张证书，之后通过公网 HTTPS 访问。不需要数据库。完整安装、宝塔编排、端口调整与备份步骤见 [DEPLOYMENT.md](DEPLOYMENT.md)。

GHCR 拉取不便时，在源码目录运行：

```sh
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

首次打开后台（本机为 `http://127.0.0.1:18088/`），设置管理员账号、密码和确认密码。账号为 3–32 位字母、数字、下划线、点或短横线，密码至少 8 个字符且不超过 72 字节。创建后自动登录，后续访问显示登录页。也可直接访问 `/install`；完成后无法再次创建管理员。

账号保存在 `data/admin.json`，重启、重建容器或升级时保留整个 `data` 目录。登录会话有效期为 12 小时，服务重启后需要重新登录。旧二进制部署如果仍设置 `ADMIN_USERNAME` 和 `ADMIN_PASSWORD`，首次启动会迁移为哈希配置；已有 `admin.json` 时以保存的账号为准。

DNS 中可以将 `*.remote.example.com` 解析到服务器公网 IP。将 `CONSOLE_HOST` 设为控制台的完整域名。

## 创建设备和映射

打开控制台，使用首次安装时创建的管理员账号登录：

1. 添加设备，保存页面仅显示一次的设备 ID 和 Token。
2. 在设备卡片内添加域名映射，例如公网 `https://router.remote.example.com:443/` → 内网 `http://127.0.0.1:80`。公网协议和端口用于生成完整访问地址，必须与服务器实际监听端口或前置 Nginx/Caddy 配置一致。
3. 构建对应架构的 Agent，把配置和二进制安装到路由器。

## 构建 OpenWrt Agent

### iStore 手动安装包

运行 `go run ./scripts/package-istore.go`，生成 `dist/RemoteGate-0.1.0-2-istore.run`，可在 iStore 的“手动安装”中上传。升级会保留 `/etc/config/remotegate` 中的连接配置。

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

## 域名与证书管理

后台“域名与证书”支持：
- 紧凑的多域名列表、搜索与状态筛选，每页显示 10 个域名；每个域名按需展开“解析接入、证书、任务记录”，刷新时保持展开位置和检测输入。
- 主域名 / 泛域名勾选与自定义域名列表；免费 Let's Encrypt 为默认选项，ZeroSSL 配置 EAB 后可用。切换申请、上传和路径来源时保留弹窗内的填写草稿。
- 从域名下直接申请或立即续期，单独开关自动续期，路径证书重新读取，导出公开证书链 PEM（不含私钥）。记录最近 30 条证书任务与设置变更；服务重启中断的申请显示为中断。
- 公网检测支持输入具体子域名与实际 HTTPS 端口，检查 DNS、参考 IP 和经过验证的公网证书。未指定目标时检查启用的映射及控制台域名，不再固定假设 console 子域名。检测不会修改 DNS；HTTP 映射的默认检测仅检查解析。
- 同时管理多个主域名；每个域名单独选择 DNS 平台、保存授权、安装证书并维护续期状态。
- 自动 DNS 验证支持阿里云 DNS、腾讯云 DNSPod 和 Cloudflare。其他平台可手动管理解析并上传 PEM 证书。
- 证书来源支持 ACME 自动申请、上传 PEM 完整证书链与未加密私钥，以及读取服务端绝对路径。导入时检查密钥匹配、有效期、服务器用途和所属域名；支持具体子域名证书，不再强制泛域名。路径导入是读取快照，外部文件更新后需重新读取。
- ACME 支持 Let's Encrypt 和 ZeroSSL（需该账户的 EAB Key ID / HMAC Key）。证书范围可选主域名、泛域名或同一主域名下的多个具体域名。支持 RSA 2048、RSA 4096、ECDSA P-256。每个主域名当前维护一张活动证书，可在同一张证书中填写多个名称。
- 在域名卡片展开“解析与证书”，点击“添加证书”，填写当前 DNS 平台的凭据和联系邮箱，确认条款后选择“保存并申请”；“仅保存配置”不会发起签发。DNSPod 使用 ID,Token，阿里云使用 AccessKey ID / Secret，Cloudflare 使用 DNS API Token（可选独立 Zone Token）。切换平台必须重新填写凭据。
- DNS 验证仅创建 TXT 记录，按本次返回的 RecordId 清理，不删除其他 TXT 记录。需 `alidns:AddDomainRecord` 和 `alidns:DeleteDomainRecord` 权限；请按实际域名资源限制 RAM 授权。
- 自动续期每小时检查，剩余不足 30 天申请替换证书，失败至少间隔 12 小时重试。上传证书不会自动续期。任务结果显示在后台，失败保留原证书。

### 启用内置 HTTPS

设置 `HTTPS_LISTEN_ADDR=:8443` 并重启。监听器可以在尚未上传证书时启动，但 TLS 握手需等待有效证书。上传或签发成功后立即热更新，新连接使用新证书。

Docker 部署：

```sh
docker compose up -d
```

HTTPS 已在基础编排中启用；确认服务器 443 未被其他服务占用并已放行。修改 `.env` 的 `HTTPS_LISTEN_ADDR` 可以使用其他端口。HTTP 管理端口只监听服务器本机。`CONSOLE_HOST` 设置为控制台域名，例如 `console.fanke.xyz`。证书不会自动改变 DNS、开放云安全组或配置外部 Nginx/Caddy。若已有外部反向代理终止 TLS，需要继续由该代理管理其证书，或改为使用内置 HTTPS。旧 `docker-compose.https.yml` 保留为兼容覆盖文件。

证书、私钥、DNS 授权保存在状态目录旁的 `certificates/`，按域名散列文件名分别保存。ACME 账户按 CA、邮箱和域名隔离。Linux 文件权限 0600、目录 0700；Windows 应限制此目录 ACL。秘密字段不通过状态 API 返回；后台写入只接受 HTTPS 或本机回环连接。请保护数据目录、主机及备份。此实现不接受公网明文 HTTP 上传凭据，也不盲目信任 X-Forwarded-Proto。

更换主域名会使用该新域名的独立证书配置；同一域名重新签发或导入失败时保留原证书。状态页展示有效期、续期、申请结果、HTTPS 监听及映射覆盖情况。DNS/公网证书检测需单独执行。真实 CA 签发需要可用的 DNS 授权和外网连接；自动化测试使用本地证书和模拟签发，不会向 CA 发起测试订单。

# 更新记录

## v0.1.0 · 2026-10-04

RemoteGate 首个发布版本，支持使用自有服务器接入 iStoreOS / OpenWrt 设备。

### 功能

- 首次安装创建管理员、账号密码登录和会话管理；密码以 bcrypt 哈希保存。
- 设备工作台、可折叠的设备功能、紧凑域名映射，以及公网入口协议和端口配置。
- 多域名管理，阿里云 DNS、腾讯云 DNSPod、Cloudflare DNS 验证。
- Let's Encrypt / ZeroSSL ACME 申请与自动续期、证书上传及服务器路径导入。
- 内置 HTTPS，证书更新热加载、证书任务记录与公网检查。
- iStore 手动安装包、后台安装包下载、一次性一键安装脚本。
- 服务端版本、构建提交与时间展示；新插件上报实际插件版本，并保留设备最后上报版本。
- Linux AMD64 / ARM64 Docker 镜像，内置 x86_64 / ARM64 / ARMv7 路由器插件包。

### 安装与更新

- Docker 镜像：`ghcr.io/yehao9589/remotegate:v0.1.0`。`latest`、`stable` 为随构建/发布更新的标签；生产部署可以固定版本。
- 路由器插件包：`RemoteGate-0.1.0-3-istore.run`，从管理后台下载后在 iStore → 手动安装上传，或执行后台生成的一键安装命令；升级保留连接配置。插件随服务端镜像提供，不作为 GitHub Release 附件单独发布。
- 首次安装与宝塔编排步骤见 [DEPLOYMENT.md](https://github.com/yehao9589/remotegate/blob/main/DEPLOYMENT.md)。
- 更新前备份整个 `data` 目录，包含管理员、已安装设备、映射、证书及 DNS 凭据。更新镜像后执行 `docker compose up -d`；不要删除数据目录。

### 验证与限制

- 自动执行 Go 测试、竞态检测、静态检查及前端脚本语法检查。
- 实际容器检查首次安装、账号持久化、安装包校验、一次性安装链接、证书加载、HTTPS 握手及重启恢复。
- 支持内网 HTTP/HTTPS Web 服务，单次请求/响应体最大 16 MiB。
- 目标 WebSocket、流式传输、文件管理、远程终端及 RDP/VNC 尚未实现。
- OpenClash 故障时的直连保护仍需用实际路由器配置验收。

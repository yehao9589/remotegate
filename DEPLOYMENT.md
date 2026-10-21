# RemoteGate 部署指南

**先安装服务器后台，再接入路由器，最后添加访问域名。**

[返回项目介绍](README.md) · [更新记录](CHANGELOG.md)

## 1. 准备

准备一台有公网访问入口的 Linux 服务器（AMD64 / ARM64，已安装 Docker Compose）、一个域名，以及一台支持插件的 iStoreOS / OpenWrt 路由器。

服务器只需一个容器，不需要数据库；路由器不需要公网 IP。

## 2. 安装后台

选择一种方式即可：

| 你习惯的方式 | 阅读这篇教程 |
| --- | --- |
| 在宝塔界面操作 | [宝塔安装](docs/deployment/baota.md) |
| 在服务器终端操作 | [命令行安装](docs/deployment/docker-cli.md) |
| 下载文件后上传服务器 | [部署包安装](docs/deployment/release-package.md) |

**完成标志：** 打开 `https://你的后台域名/install`，创建管理员并进入后台。账号默认 `admin`，密码自行设置，后台路径默认 `/admin`。安装后收藏最终地址。

## 3. 接入路由器

在后台点击“添加设备”，按[路由器接入教程](docs/deployment/router.md)选择一键安装或 iStore 手动安装。插件从后台获取。

**完成标志：** 插件显示“已连接服务器”，后台设备显示在线。

## 4. 添加访问域名

按[域名与 HTTPS 教程](docs/deployment/external-https.md)配置解析、证书和反代，再在设备的“访问域名”里添加内网服务。分别检测公网 HTTPS 和内网目标后打开。

例如：`https://router.gate.example.com/` → 路由器上的 `http://127.0.0.1:80`。

**完成标志：** 访问域名能打开路由器或 NAS 页面。默认需要后台登录，目标网站原有登录也保留。

### 这三种地址别填混

| 用途 | 示例 |
| --- | --- |
| 浏览器打开管理后台 | `https://gate.example.com/admin` |
| 路由器填写服务器地址 | `https://gate.example.com`，不带后台路径 |
| 访问内网服务 | `https://router.gate.example.com/` |

示例域名换成你自己的。内网目标中的 `127.0.0.1` 指运行插件的路由器。

## 5. 后续更新与备份

默认镜像 `ghcr.io/yehao9589/remotegate:stable` 跟随正式发布。更新前备份完整数据目录和部署配置，重新拉取镜像并重新部署，保留原数据目录和 `.env`。**只重启容器不会升级。**

服务器更新后，再检查是否有插件更新。详见[更新与备份](docs/deployment/maintenance.md)。

## 遇到问题先看这里

| 问题 | 从哪里检查 |
| --- | --- |
| 后台打不开或登录提示来源错误 | [后台访问排查](docs/deployment/access.md#访问问题排查) |
| 设备无法上线 | [路由器接入与连接说明](docs/deployment/router.md) |
| 设备在线，访问域名打不开 | [域名、证书和反代排查](docs/deployment/external-https.md#常见问题) |

已有宝塔 / Nginx 提供 HTTPS 时，证书由该入口管理，RemoteGate 只检测公网结果。不需要迁移其他网站的 443，也不需要主机助手或宝塔 API。

**为什么暂用宝塔申请证书？** 宝塔反代已管理公网 HTTPS 端口和证书，RemoteGate 再独立管理同一入口可能造成端口占用，或申请的证书未在公网生效。后续若重新启用后台证书申请，会考虑调整或拆分后台监听端口，并适配证书部署。详见[当前方案与后续计划](docs/deployment/external-https.md#当前方案与后续计划)。

<details>
<summary>开发构建与旧版配置维护</summary>

- [从源码构建](docs/deployment/source.md)
- [历史内置 HTTPS 配置](docs/deployment/standalone.md)
- [历史共用 443 配置](docs/deployment/shared-443.md)

历史教程仅供维护旧实例，新版后台已移除文中的网页证书申请按钮。

</details>

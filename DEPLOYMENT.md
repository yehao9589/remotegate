# RemoteGate 部署指南

一个容器运行管理后台、设备隧道、域名映射和证书管理，不需要 MySQL、Redis、PHP 或宿主机 Node.js/Go。官方镜像提供 Linux AMD64/ARM64 服务端，并内置 x86_64、ARM64、ARMv7 路由器插件包。

## 第一步：选择一种安装方式

**只选一种教程完成容器安装，不需要把四种方式都执行。**

| 方式 | 适用场景 | 操作位置 | 详细教程 |
| --- | --- | --- | --- |
| 1. 宝塔界面部署 | 使用宝塔 Docker，准备通过域名反代访问后台 | 宝塔和浏览器，全程不用电脑 PowerShell | [宝塔教程](docs/deployment/baota.md) |
| 2. 命令行拉取镜像 | 使用服务器终端与 Docker Compose | Linux 服务器终端 | [命令行教程](docs/deployment/docker-cli.md) |
| 3. 下载部署包 | 不克隆仓库，下载编排包部署 | 上传文件后在服务器终端启动 | [下载包教程](docs/deployment/release-package.md) |
| 4. 从源码构建 | 修改代码、构建指定提交或不用 GHCR 服务端镜像 | Linux 服务器终端 | [源码教程](docs/deployment/source.md) |

**你用宝塔并已有后台域名时，直接阅读方式 1。** 其中已经给出可粘贴的 compose、.env、反代目标、安装地址和更新入口。

各方式均需要 Linux 服务器与 Docker Engine/Compose。编排使用 host 网络，直接使用主机端口，不另加 `ports`。默认 HTTP 仅监听服务器本机 `127.0.0.1:18088`。

## 第二步：按所选场景进入安装页

安装方式与访问方式分开选择。不是每种安装都要使用 SSH。

| 当前情况 | 如何首次进入后台 |
| --- | --- |
| 已有宝塔/Nginx 可用域名反代 | 直接打开域名的 `/install`，例如 `https://gate.fanke.xyz/install`；不需要 SSH |
| 暂无反代，准备使用 RemoteGate 内置 HTTPS | 可经 SSH 隧道创建账号与配置第一张证书，再切换公网 HTTPS |

完整说明见[首次访问、后台入口与 HTTPS](docs/deployment/access.md)。宝塔教程已包含第一条流程，不必再执行第二条。

首次账号默认 `admin`，密码自行设置，后台入口默认 `/admin`，可自定义。完成后收藏新地址，`/install` 会关闭；已有实例保留原账号与入口。

## 第三步：接入路由器与访问域名

- [路由器插件安装与映射教程](docs/deployment/router.md)：选择一键安装或 iStore 手动安装，待设备上线后在设备下添加域名。
- [映射域名的 HTTPS 入口](docs/deployment/access.md#设备映射的-https-入口)：选择内置 HTTPS 或前置宝塔/Nginx，按实际入口填写映射协议与端口。

后台域名、设备映射域名及路由器服务器地址不是同一个字段：后台入口可以带自定义路径，路由器服务器地址不带这个路径，每个映射使用具体子域名。

## 后续维护

[更新、备份与恢复](docs/deployment/maintenance.md)分别列出宝塔界面更新、命令行更新、源码更新入口和数据备份方法。

默认镜像是 `ghcr.io/yehao9589/remotegate:stable`，跟随最新通过检查的构建。标签更新不会自动升级运行中的容器；需重新拉取并重新部署，保留原数据目录。

官方 [Release](https://github.com/yehao9589/remotegate/releases) 只提供服务端部署包和校验文件。路由器插件从后台下载或执行后台的一键安装命令，不单独在 Release 发布。

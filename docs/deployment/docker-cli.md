# 命令行安装

[返回部署指南](../../DEPLOYMENT.md)

本篇适用于在 **Linux 服务器终端**操作的用户。先安装服务端，再配置 HTTPS 访问入口。

## 1. 下载配置文件

首次安装执行：

```sh
git clone https://github.com/yehao9589/remotegate.git
cd remotegate
cp .env.example .env
```

已有实例更新时不要重新复制 `.env`，请使用[更新教程](maintenance.md#命令行更新)。

## 2. 编辑 .env

**已有同一台服务器上的宝塔 / Nginx 反代时，使用下面的配置：**

```dotenv
REMOTE_GATE_IMAGE=ghcr.io/yehao9589/remotegate:stable
REMOTE_GATE_DATA_DIR=./data
CONSOLE_HOST=gate.example.com
PUBLIC_URL=
HTTPS_LISTEN_ADDR=
```

把 `gate.example.com` 换成自己的后台域名。其他项目保留 `.env.example` 的默认值。

- `HTTPS_LISTEN_ADDR` 留空，由现有反代提供 HTTPS，避免争用 443。
- `CONSOLE_HOST` 只填域名，不带协议和 `/admin` 等路径。
- 默认 443 入口下 `PUBLIC_URL` 留空；自定义公网端口的设置见[访问说明](access.md#方案-a已有宝塔nginx-反代)。

还没有反代时，暂将 `CONSOLE_HOST`、`PUBLIC_URL` 和 `HTTPS_LISTEN_ADDR` 都留空，启动后按[SSH 临时访问](access.md#方案-b没有反代使用-ssh-隧道)初始化。SSH 初始化不会自动配置公网 HTTPS。

## 3. 启动容器

```sh
docker compose pull
docker compose up -d
docker compose ps
```

容器应显示运行中。需要排查时执行 `docker compose logs --tail=100` 查看日志。

本编排使用 host 网络，不另外添加 `ports`。HTTP 默认只监听服务器本机 `127.0.0.1:18088`，不用对公网开放这个端口。

## 4. 打开安装页

将现有站点反代到 `http://127.0.0.1:18088`，保留原始 Host 和 WebSocket 配置，启用有效 HTTPS 后打开：

```text
https://gate.example.com/install
```

创建管理员，设置并收藏后台入口，例如 `https://gate.example.com/admin`。

**完成标志：** 能登录后台，在“安装与更新中心”查看版本和下载插件。接着按[路由器接入教程](router.md)添加设备。

反代、泛域名与证书的完整配置见[HTTPS 入口教程](external-https.md)。镜像拉取不便时可以选择[源码构建](source.md)，但构建仍需下载基础镜像和依赖。

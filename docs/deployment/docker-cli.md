# 方式二：命令行拉取镜像

[返回部署指南](../../DEPLOYMENT.md)

适用于在 Linux 服务器终端管理 Docker Compose。无需在宿主机安装 Go 或数据库。以下命令都在**服务器终端**执行。

## 1. 获取编排文件

```sh
git clone https://github.com/yehao9589/remotegate.git
cd remotegate
cp .env.example .env
```

## 2. 选择首次访问路径并编辑 .env

只选择下表的一行，不要混用两套配置：

| 场景 | .env 设置 | 启动后访问 |
| --- | --- | --- |
| 系统独立 HTTPS（默认） | 配置 `BOOTSTRAP_DOMAIN`、邮箱、DNS 凭据及服务条款同意；保留 `HTTPS_LISTEN_ADDR=:443` | 首次证书签发后直接进入 HTTPS 安装页，完整可复制配置见[独立部署](standalone.md) |
| 已有本机 HTTPS 反代 | `CONSOLE_HOST` 填后台域名；默认外部 80/443 下 `PUBLIC_URL` 留空，自定义端口时填完整地址；仅使用反代时将 `HTTPS_LISTEN_ADDR` 留空 | [访问方案 A](access.md#方案-a已有宝塔nginx-反代)中的域名安装页，无需 SSH |
| 没有反代，使用内置 HTTPS | 首次 `CONSOLE_HOST`、`PUBLIC_URL` 留空；443 空闲时使用 `HTTPS_LISTEN_ADDR=:443` | [访问方案 B](access.md#方案-b没有反代使用-ssh-隧道)，先经 SSH 创建账号与申请证书；已有宝塔网站希望共用标准端口时阅读[共用 443](shared-443.md) |

默认使用 `REMOTE_GATE_IMAGE=ghcr.io/yehao9589/remotegate:stable`。数据默认放在项目的 `./data`；也可以把 `REMOTE_GATE_DATA_DIR` 改成服务器绝对路径。

HTTP 默认监听 `127.0.0.1:18088`，不直接对公网开放。只有选择内置 HTTPS 才需要放行其实际公网端口；使用现成反代时沿用反代的公网端口。

## 3. 拉取并启动

```sh
docker compose pull
docker compose up -d
docker compose ps
docker compose logs --tail=100
```

`docker compose ps` 显示运行中后，按第 2 步选好的首次访问方案创建管理员。仅容器启动成功，并不代表还未配置证书的内置 HTTPS 可以握手。

编排使用 Linux host 网络，不另加 `ports`。

## 4. 验收

- 能打开安装页，创建管理员并进入保存的后台路径。
- 后台“安装包与版本”显示当前运行版本，官方镜像已内置路由器安装包。
- 在后台刷新，账号会话仍有效。

后续：[路由器接入](router.md) · [命令行更新与备份](maintenance.md#命令行更新) · [访问问题排查](access.md#访问问题排查)。

如果无法拉取 GHCR 镜像，可以选择[源码构建方式](source.md)。源码构建仍需下载基础镜像和 Go 依赖，并非完全离线安装。

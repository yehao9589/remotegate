# 方式三：下载部署包安装

[返回部署指南](../../DEPLOYMENT.md)

适用于不想克隆 Git 仓库，只下载编排文件部署的情况。部署包不是服务端二进制；启动时仍需要 Docker 拉取服务端镜像。

## 1. 下载并上传

打开 [GitHub Releases](https://github.com/yehao9589/remotegate/releases)，下载所选版本的：

- `RemoteGate-v版本号-compose.tar.gz`
- `SHA256SUMS-v版本号.txt`

把这两个文件上传到服务器同一目录，例如 `/opt/remotegate`。Release 不单独发布路由器插件，插件由启动后的后台提供。

## 2. 校验并解压

以下命令在**服务器终端**执行。将命令中的 `v版本号` 换成下载文件的实际版本号：

```sh
cd /opt/remotegate
sha256sum -c SHA256SUMS-v版本号.txt
tar -xzf RemoteGate-v版本号-compose.tar.gz
cp .env.example .env
```

这是首次安装的命令。更新已有实例时保留现有 `.env` 和数据目录，不要用示例覆盖它们。

解压后的 `DEPLOYMENT.md` 是导航页，`docs/deployment/` 中包含各方式的详细教程，可以离线阅读。

## 3. 配置并启动

编辑 `.env`，按[首次访问方案](access.md)选择已有反代或 SSH 初始化。新部署包默认使用 `:stable`；历史包可能固定旧版本，需要检查 `.env` 的 `REMOTE_GATE_IMAGE`。

```sh
docker compose pull
docker compose up -d
docker compose ps
```

随后按选好的访问方案创建管理员，再阅读[路由器接入](router.md)。

如果希望在宝塔纯界面操作，直接使用[宝塔方式](baota.md)中的两段配置即可，无需执行本篇命令。

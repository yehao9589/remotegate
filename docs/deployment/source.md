# 方式四：从源码构建 Docker 镜像

[返回部署指南](../../DEPLOYMENT.md)

适用于需要修改代码、构建指定提交，或无法拉取 GHCR 服务端镜像的情况。构建过程在 Docker 内运行，宿主机不需要单独安装 Go。

仍需要联网下载基础镜像和 Go 依赖。只下载 Release 的 compose 部署包不能从源码构建，必须取得完整 Git 仓库。

## 1. 获取源码

以下命令在**服务器终端**执行：

```sh
git clone https://github.com/yehao9589/remotegate.git
cd remotegate
cp .env.example .env
```

需要指定已发布版本时，在构建前检出相应的 `v版本号` 标签；否则使用当前克隆到的源码。

## 2. 配置访问方式

编辑 `.env`，按照[首次访问方案](access.md)选择现成反代，或经 SSH 初始化内置 HTTPS。数据目录、监听端口和后台外部地址与[命令行镜像方式](docker-cli.md)一致。

源码构建使用 `remotegate:local` 本地镜像，不使用 GHCR 的 `stable`；`.env` 的 `REMOTE_GATE_IMAGE` 不能替代源码更新与重新构建。

## 3. 构建并启动

```sh
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
docker compose -f docker-compose.yml -f docker-compose.build.yml ps
```

构建会生成服务端与 x86_64、ARM64、ARMv7 路由器包，并把插件包放进服务端镜像。无需另外复制宿主机的 `dist` 目录。

启动后按第 2 步选好的访问方案进入安装页。自行构建的镜像可能显示“本地开发构建”，不能仅凭 `stable` 发布记录判断它的源码。

## 4. 后续更新

```sh
git pull --ff-only
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

若检出了版本标签，需要先自行选择要更新到的标签或分支。保留数据目录；修改过的源码应先妥善保存。源码实例更新时继续使用两个 compose 文件，不混用官方镜像的拉取步骤。

后续：[路由器接入](router.md) · [备份与恢复](maintenance.md#备份与恢复)。

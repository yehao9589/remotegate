# RemoteGate · Docker Compose 部署

一个容器运行管理后台、设备隧道、域名映射和证书管理，不需要 MySQL、Redis、PHP 或 Node.js。镜像同时提供 Linux AMD64 / ARM64 服务端，并内置 x86_64、ARM64、ARMv7 路由器安装包。

## 1. 准备服务器

- 使用 Linux 服务器，已安装 Docker Engine 和 Docker Compose 插件。
- 公网访问放行实际使用的 HTTPS TCP 端口，默认 443。
- HTTP 安装入口默认仅监听服务器本机的 `127.0.0.1:18088`。
- 本编排使用 Linux host 网络。容器直接监听主机端口，不能再添加 `ports` 映射；镜像的服务进程以 UID 10001 运行。

443 已被宝塔或其他网站占用时，修改 `HTTPS_LISTEN_ADDR=:8443`，在云安全组和防火墙放行 8443。路由器服务器地址及公网映射端口也使用 8443，例如 `https://console.fanke.xyz:8443`。

## 2. 获取代码并启动

在服务器执行：

```sh
git clone https://github.com/yehao9589/remotegate.git
cd remotegate
cp .env.example .env
docker compose pull
docker compose up -d
docker compose ps
```

镜像为 `ghcr.io/yehao9589/remotegate:stable`。如果服务器无法访问 GHCR，可从源码构建同一镜像：

```sh
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

也可以在 [GitHub Releases](https://github.com/yehao9589/remotegate/releases) 下载 `RemoteGate-v版本号-compose.tar.gz`，解压到安装目录，复制 `.env.example` 为 `.env`，再执行 `docker compose pull` 和 `docker compose up -d`。Release 编排包默认固定到该发布版本。

### 版本与镜像标签

| 标签 | 用法 |
| --- | --- |
| `v0.1.0` 等固定版本 | 指定 GitHub Release 对应版本，适合需要控制升级的部署 |
| `stable` | 跟随通过检查的主分支构建与版本发布，与 YehaoProxy 的发布约定一致 |
| `latest` | 当前主分支或最近版本发布镜像 |
| `sha-完整提交号` | 指定源码提交对应构建 |

后台“安装包与版本”显示服务端版本、构建提交、构建时间和可下载插件版本。客户端运行版本与插件修订号分别展示；旧插件未上报修订号时显示“未上报”。Release 附件只包含服务端编排压缩包和 SHA-256 校验文件。路由器插件从管理后台下载，或在路由器执行后台生成的一键安装命令；无需去 GitHub 下载插件。

本地构建镜像自带安装包，不依赖宿主机的 `dist` 目录。首次构建需要下载 Go 依赖与基础镜像。

### 宝塔容器编排

在宝塔 → Docker → 容器编排 → 添加容器编排中，可直接粘贴下面两段内容，不需要先克隆代码，也不需要数据库。

| 窗口字段 | 填写内容 |
| --- | --- |
| 编排名称 | `RemoteGate` |
| 来源 | 选择“编辑” |
| compose 内容 | 粘贴下面的 YAML |
| .env 内容 | 粘贴下面的环境配置 |
| 同时存为模板 | 可不勾选 |

**compose 内容：**

```yaml
services:
  remotegate:
    image: ${REMOTE_GATE_IMAGE:-ghcr.io/yehao9589/remotegate:v0.1.0}
    restart: unless-stopped
    network_mode: host
    environment:
      CONSOLE_HOST: ${CONSOLE_HOST:-}
      LISTEN_ADDR: 127.0.0.1:18088
      HTTPS_LISTEN_ADDR: ${HTTPS_LISTEN_ADDR:-:8443}
      STATE_PATH: /data/state.json
      HEALTHCHECK_URL: http://127.0.0.1:18088/healthz
      TZ: Asia/Shanghai
    volumes:
      - ${REMOTE_GATE_DATA_DIR:-/www/RemoteGate/data}:/data
    stop_grace_period: 20s
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"
```

**.env 内容：**

```dotenv
REMOTE_GATE_IMAGE=ghcr.io/yehao9589/remotegate:v0.1.0
REMOTE_GATE_DATA_DIR=/www/RemoteGate/data
HTTPS_LISTEN_ADDR=:8443
CONSOLE_HOST=
```

- `REMOTE_GATE_IMAGE` 是服务端镜像版本。此示例固定为 v0.1.0；升级时修改版本后重新拉取并部署。希望跟随最新通过检查的构建时可使用 `:stable`。
- `REMOTE_GATE_DATA_DIR` 是服务器上的数据目录，保存账号、设备、映射和证书。此处使用绝对路径，不依赖宝塔生成的编排工作目录；升级时保持这个路径。
- `HTTPS_LISTEN_ADDR=:8443` 表示公网 HTTPS 使用 8443 端口，方便与宝塔已有网站共存。确认端口空闲，并在云安全组和服务器防火墙放行 TCP 8443。若要用标准 443 且它未被占用，可改为 `:443`。
- `CONSOLE_HOST` 首次安装留空。配置好域名和证书后，再填管理后台域名，例如 `console.fanke.xyz`，只写域名，不写协议或端口。
- 管理员账号、密码在首次安装网页设置，不填写在 `.env` 中。
- 使用 host 网络，不要另加 `ports`。18088 仅供服务器本机和 SSH 隧道访问，不需要向公网开放。

点击“确定”，等待镜像拉取并启动，确认容器状态为运行中。然后在**自己的电脑 PowerShell / 终端**执行以下命令（不是宝塔服务器终端）：

```sh
ssh -N -L 18090:127.0.0.1:18088 root@服务器公网IP
```

把 `服务器公网IP` 换成真实 IP，SSH 用户及端口按服务器实际设置；非 22 端口可加 `-p 你的SSH端口`。此命令连接成功后会一直等待，保持窗口打开。在电脑浏览器打开 `http://127.0.0.1:18090/install`，创建管理员账号。如果电脑的 18090 已被占用，换一个空闲的本地端口。

这里的 `127.0.0.1:18090` 通过 SSH 访问服务器后台；你电脑上原有的 `localhost:18089/install` 是本地测试服务，不是这台云服务器。

随后按下文“配置域名与 HTTPS”添加域名、配置 DNS 和申请证书。使用以上 8443 示例时，最终管理地址为 `https://console.fanke.xyz:8443/`，路由器的服务器地址也填写 `https://console.fanke.xyz:8443`。首次尚无证书时，公网 HTTPS 入口不能正常打开。

如果已经克隆项目，也可以选择“文件”，导入项目目录下的 `docker-compose.yml`，把 `.env.example` 复制为 `.env`。此时确认工作目录与 `REMOTE_GATE_DATA_DIR` 设置正确；直接沿用 `./data` 时，数据会保存在编排工作目录下。

## 3. 首次安装

容器刚启动还没有证书，公网 HTTPS 暂时无法握手。这时先通过 SSH 隧道打开本机安装入口。在你自己的电脑执行：

```sh
ssh -N -L 18088:127.0.0.1:18088 root@服务器公网IP
```

浏览器打开 `http://127.0.0.1:18088/install`，管理员账号默认填写 admin；设置密码、确认密码和后台入口路径（默认 /admin）。完成后进入新入口并关闭 /install，请收藏当前地址。可在后台“账号与入口”修改路径或密码，验证当前密码后保存，所有会话将退出。旧安装的账号与入口保持原样。

如果电脑已有测试后台占用 18088，用其他本地端口：

```sh
ssh -N -L 18089:127.0.0.1:18088 root@服务器公网IP
```

然后访问 `http://127.0.0.1:18089/install`。如果 18089 也被占用，替换成一个空闲本地端口。保留这个 SSH 窗口，继续配置域名与证书。

## 4. 配置域名与 HTTPS

以自有域名 `fanke.xyz` 为例：

1. 在实际负责 DNS 解析的平台添加 A 记录：`console.fanke.xyz` 和 `*.fanke.xyz` 指向服务器公网 IPv4。域名购买平台和实际 DNS 解析平台可以不同。
2. 在后台“域名与证书”添加 `fanke.xyz`，填写服务器 IP，并选择阿里云 DNS、DNSPod、Cloudflare 或手动解析。
3. 在这个域名下添加证书：选择免费 Let's Encrypt 自动申请，勾选主域名和泛域名，填写对应 DNS API 凭据、邮箱并自行确认服务条款。也可以上传已有的完整证书链和私钥。
4. 申请成功后，内置 HTTPS 入口立即加载证书，不需要重启。浏览器访问 `https://console.fanke.xyz/admin`（将 /admin 换成安装时设置的入口路径）；使用非标准端口时带上端口号。
5. 把 `.env` 的 `CONSOLE_HOST` 改为 `console.fanke.xyz`，执行 `docker compose up -d`，让这个域名专门提供管理后台。

在设置 `CONSOLE_HOST` 之后，通过 SSH 隧道进入 HTTP 管理入口时也要保留这个域名。可在电脑 hosts 文件临时加入 `127.0.0.1 console.fanke.xyz`，访问 `http://console.fanke.xyz:18088/admin`（使用自己的入口路径）；配置完成后移除临时 hosts 记录，恢复公网访问。

阿里云使用 AccessKey ID / Secret，DNSPod 使用 DNSPod API 的 ID / Token，Cloudflare 使用可编辑目标区域 DNS 的 API Token。这些只用于证书 DNS 验证；DNS 的 A 记录需要你先创建。签发任务失败会显示原因并保留已有证书。

SSH 隧道把 HTTP 管理请求传到服务器回环地址，证书和 DNS 凭据可以在这个入口配置。完成后可以关闭隧道，正常使用公网 HTTPS 管理。

如果已有 Nginx/Caddy/宝塔 HTTPS 入口，可以继续反向代理到 `127.0.0.1:18088`。保留原始 Host，并启用 `/api/agent/connect` 的 WebSocket 升级；此时 HTTPS 证书由该代理部署，后台内置证书更新不会自动更新外部代理的证书文件。

## 5. 路由器安装与映射

1. 在设备工作台点击添加设备，选择一键安装或下载版本化的 `.run` 包。
2. 服务端地址填可被路由器访问的地址，例如 `https://console.fanke.xyz`，不包含 /admin 等后台路径。不要填电脑的 `127.0.0.1`。
3. 在路由器执行后台生成的安装命令；或进入 iStore → 手动安装，上传安装包，随后在“服务 → RemoteGate”填写连接信息。
4. 设备上线后，在设备下面添加访问域名。例如 `https://ceshi01.fanke.xyz:443/` 映射到路由器的 `http://127.0.0.1:80`，另一个域名映射到 `http://127.0.0.1:16601`。

路由器主动连接服务器，不需要路由器公网 IP 或端口转发。OpenClash 故障时的直连保护仍需用实际路由器配置验收。

## 6. 更新、备份与恢复

更新镜像并重建服务：

```sh
docker compose pull
docker compose up -d
docker compose logs --tail=100 -f
```

从源码构建时更新：

```sh
git pull --ff-only
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

`.env` 的 `REMOTE_GATE_IMAGE` 可改成发布的 `sha-完整提交号` 标签，固定运行版本或回滚；不要删除数据目录。新版本写入的数据未必兼容旧版本，回滚时按需恢复更新前备份。

备份前停止服务，保留一致的数据快照：

```sh
docker compose stop
sudo tar -czf remotegate-data-$(date +%Y%m%d-%H%M%S).tar.gz data .env
docker compose up -d
```

默认数据目录为 `./data`。如果修改了 `REMOTE_GATE_DATA_DIR`，按实际目录备份。管理员、设备及证书均在这个目录：

| 路径 | 内容 |
| --- | --- |
| `data/admin.json` | 管理员账号与密码哈希 |
| `data/state.json` | 已安装设备、域名与映射配置 |
| `data/certificates/` | 证书、私钥、DNS API 凭据及 ACME 账户 |

备份包含秘密凭据，应妥善保管。恢复到新服务器时，停止服务，恢复整个数据目录及 `.env`，再启动。迁移服务器后同步更新域名解析和后台服务器 IP。

## 常见问题

**容器不健康**：查看 `docker compose logs --tail=100`。若修改 HTTP 监听端口，同时修改 `HEALTHCHECK_URL`；如果启动日志显示 HTTPS 端口占用，更改监听端口并同步安全组与映射配置。

**后台安装包下载不可用**：官方镜像与源码构建镜像都应内置安装包。查看后台“安装包与版本”，确认版本、架构和校验值；升级前先确认镜像来自本仓库。

**HTTPS 打不开**：检查证书是否有效并覆盖访问域名、域名解析是否指向服务器、实际端口是否已放行。健康检查只检测 HTTP 服务，不能代替证书与公网连通性检查。

**拉取镜像提示 denied**：确认地址为 `ghcr.io/yehao9589/remotegate`。新 GHCR 包首次发布默认可能是私有，需仓库所有者在包设置中改为 public，之后可匿名拉取；也可使用上述源码构建方式。

参考：[Docker host 网络](https://docs.docker.com/engine/network/drivers/host/)、[GitHub 容器镜像仓库](https://docs.github.com/en/packages/working-with-packages/working-with-the-container-registry)。

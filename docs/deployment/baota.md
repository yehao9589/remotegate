# 方式一：宝塔界面部署

[返回部署指南](../../DEPLOYMENT.md)

本篇适用于使用宝塔 Docker 管理容器，并通过宝塔域名反代访问后台的服务器。全程在宝塔和浏览器操作，不需要在自己电脑运行 PowerShell 或 SSH 命令。

流程：**粘贴编排 → 启动容器 → 配置宝塔域名与反代 → 打开安装页 → 创建管理员**。

**版本要求：v0.1.3 或更新版本。** 同一后台域名的默认 HTTP/HTTPS 入口均可安装和登录；只需填写 `CONSOLE_HOST`，不用额外填写 `PUBLIC_URL`。已有 v0.1.2 容器需要重新拉取镜像并重新部署。

如果暂时没有域名或反代，请选择[首次访问方案 B](access.md#方案-b没有反代使用-ssh-隧道)，不要照抄本篇的域名配置。

## 1. 准备

- Linux 服务器，宝塔已安装 Docker 管理功能。
- 一个指向服务器 IP 的后台域名。以下使用 `gate.example.com` 举例，请替换为自己的域名。
- 宝塔站点使用有效的 HTTPS 证书，公网可访问站点的 443 端口。

后台只需要这一个域名；设备访问域名可以在后台上线后再配置。不需要数据库。

## 2. 填写容器编排

宝塔 → Docker → 容器编排 → 添加容器编排：

| 字段 | 填写 |
| --- | --- |
| 编排名称 | `RemoteGate` |
| 来源 | 编辑 |
| compose 内容 | 下方 YAML |
| .env 内容 | 下方环境配置 |
| 同时存为模板 | 可不勾选 |

### compose 内容

```yaml
services:
  remotegate:
    image: ${REMOTE_GATE_IMAGE:-ghcr.io/yehao9589/remotegate:stable}
    restart: unless-stopped
    network_mode: host
    environment:
      CONSOLE_HOST: ${CONSOLE_HOST:-}
      PUBLIC_URL: ${PUBLIC_URL:-}
      LISTEN_ADDR: 127.0.0.1:18088
      HTTPS_LISTEN_ADDR: "${HTTPS_LISTEN_ADDR:-}"
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

### .env 内容

```dotenv
REMOTE_GATE_IMAGE=ghcr.io/yehao9589/remotegate:stable
REMOTE_GATE_DATA_DIR=/www/RemoteGate/data
CONSOLE_HOST=gate.example.com
PUBLIC_URL=
HTTPS_LISTEN_ADDR=
```

主要修改 `CONSOLE_HOST` 为自己的后台域名，使用默认 80/443 端口时 `PUBLIC_URL` 留空：

| 配置 | 含义 |
| --- | --- |
| `CONSOLE_HOST` | 后台域名，只写域名，不写协议、端口或后台路径 |
| `PUBLIC_URL` | 默认留空；使用自定义外部端口时填完整地址，不写 `/admin` 等路径 |
| `REMOTE_GATE_DATA_DIR` | 服务器数据目录，更新和重建时保持不变 |
| `REMOTE_GATE_IMAGE` | 默认 `stable`，跟随最新通过检查的构建 |
| `HTTPS_LISTEN_ADDR` | 首次按本篇留空，由宝塔提供后台 HTTPS；在后台保存过内置入口设置后，以保存的 `https.json` 为准 |

比如 HTTPS 使用非标准 8443 端口，`PUBLIC_URL` 填 `https://gate.example.com:8443`。该设置只识别外部地址，不会给宝塔申请证书。

同一域名从 HTTP 切换为 HTTPS 不用修改配置。已有 `PUBLIC_URL=https://gate.example.com` 也可原样保留，实际通过 `http://gate.example.com` 访问时不会因协议差异被拒绝。自定义外部端口或后台域名变化后，需要修改配置并重新部署容器。

如果已有站点暂时只支持 HTTP，可以使用 `http://gate.example.com/install`。兼容逻辑不会申请站点证书；建议先在宝塔配置有效的 HTTPS 证书，再填写管理员密码。

本编排使用 host 网络，不另加 `ports`。HTTP 只监听服务器的 `127.0.0.1:18088`，不用向公网开放 18088。这里也不需要开放 RemoteGate 的 8443 端口。

## 3. 启动并确认版本

点击“确定”，等待镜像拉取并启动。容器列表应显示运行中，启动日志应包含服务端版本与 `127.0.0.1:18088`。

从旧编排更新时，compose 和 `.env` 都使用 `stable`；`.env` 中旧的固定标签会覆盖 compose 的默认值。保存后重新拉取镜像并重新部署，单纯重启不会升级。

## 4. 配置宝塔站点反代

在宝塔网站管理中创建或选择 `gate.example.com` 站点，为这个站点配置 HTTPS 证书，然后添加反向代理：

| 项目 | 填写 |
| --- | --- |
| 代理路径 | `/` |
| 目标 URL | `http://127.0.0.1:18088` |
| 发送域名 | 可保留宝塔默认值，后台通过 `CONSOLE_HOST` 或 `PUBLIC_URL` 识别外部域名 |
| WebSocket | 保留 Upgrade、Connection 与 HTTP/1.1 转发配置，设备连接会使用它 |

本篇的默认反代兼容从 v0.1.3 起支持只填写 `CONSOLE_HOST`，且只信任服务器本机回环地址连接。它适用于本篇的 Linux host 网络；其他主机或 Docker bridge 的代理不在这一信任范围内。

## 5. 打开安装页

**直接在浏览器打开：**

```text
https://gate.example.com/install
```

管理员账号默认 `admin`，填写密码、确认密码及后台入口，例如 `/admin`。创建后会进入：

```text
https://gate.example.com/admin
```

如果自定义为 `/my-panel`，以后就使用对应地址。安装完成后 `/install` 关闭，旧安装的原账号与入口会保留。

**已经能通过宝塔域名打开后台，就不需要 SSH 隧道。** SSH 是没有可用反代时的另一条首次访问路径。

## 6. 接下来做什么

- [安装路由器插件与添加映射](router.md)：插件从后台下载，或执行一键安装命令。
- [选择映射域名的 HTTPS 入口](access.md#设备映射的-https-入口)：后台能访问不代表所有映射域名都已接入。
- [宝塔更新与备份](maintenance.md#宝塔界面更新)。

后台和映射都可直接使用宝塔的 443。给 RemoteGate 对应站点绑定主域名和泛域名，使用 DNS 验证申请并启用证书，证书续期由宝塔负责。RemoteGate 页面只检测实际公网证书。完整操作见[宝塔泛域名证书与 HTTPS 检测](external-https.md)。

映射反代必须保留原始 Host；仅申请证书或仅添加泛域名 DNS，都不能代替站点域名绑定和反代。无需迁移其他网站的监听，也不需要主机助手或宝塔 API。

## 常见问题

**请求来源不匹配**：确认实际运行版本至少为 v0.1.3，compose 中传入了 `CONSOLE_HOST`，值与浏览器域名一致；自定义外部端口需用 `PUBLIC_URL` 指定并保持一致。修改环境配置后重新创建容器。不要在配置中加入后台路径。

同一域名的默认 HTTP/HTTPS 协议差异已自动兼容，无需修改反代的“发送域名”。如果同时填写了 `PUBLIC_URL`，它优先于 `CONSOLE_HOST`，请检查是否残留了另一个域名或旧端口。

**域名打不开**：先检查站点 DNS、宝塔 HTTPS 证书和反代目标。若站点已存在，还要检查代理路径是否覆盖 `/`。

**看不到后台入口字段**：从 v0.1.1 开始才有该字段；旧镜像需重新拉取部署。已经完成安装时，应访问保存的后台路径，不再访问 `/install`。

**多个映射域名打不开**：`PUBLIC_URL` 只指定管理后台地址。映射入口必须保留访问域名的 Host 才能区分各条映射，详见[映射 HTTPS 入口](access.md#设备映射的-https-入口)。

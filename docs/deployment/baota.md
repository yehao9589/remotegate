# 宝塔界面安装

[返回部署指南](../../DEPLOYMENT.md)

**在宝塔安装一个容器，用 RemoteGate 对应站点提供 HTTPS，再打开网页创建管理员。** 全程在宝塔和浏览器操作。

示例后台域名为 `gate.example.com`，请换成自己的域名，并提前解析到服务器公网 IP。不需要数据库。

**证书暂由宝塔申请和续期。** 现有反代已接管公网 HTTPS 入口，RemoteGate 再独立监听同一端口、管理证书可能产生冲突，或使新证书无法在公网生效。后续若恢复后台申请证书，会考虑调整或拆分后台监听端口并适配证书部署；目前按下方步骤使用宝塔方案。详见[当前方案与后续计划](external-https.md#当前方案与后续计划)。

## 1. 添加容器编排

进入“宝塔 → Docker → 容器编排 → 添加”，名称填写 `RemoteGate`，选择编辑内容，分别粘贴下面两段。

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

**首次安装通常只需改 `CONSOLE_HOST` 为自己的后台域名。** `HTTPS_LISTEN_ADDR` 保持空值，HTTPS 由宝塔提供，不与现有网站争用 443。

| 配置 | 怎么填 |
| --- | --- |
| `CONSOLE_HOST` | 后台域名，不带协议和 `/admin` |
| `REMOTE_GATE_DATA_DIR` | 数据保存目录；更新时保持原路径 |
| `PUBLIC_URL` | 默认留空；自定义公网端口时填完整地址，不带后台路径 |

点击确定，等待镜像下载和容器启动。**完成标志：** 容器列表显示运行中。

## 2. 设置对应站点的 HTTPS 和反代

在宝塔创建或选择 `gate.example.com` 站点，启用有效的 HTTPS 证书，再添加反向代理：

| 项目 | 填写 |
| --- | --- |
| 代理路径 | `/` |
| 目标 URL | `http://127.0.0.1:18088` |
| 公网 HTTPS 端口 | `443` |
| WebSocket | 保留 Upgrade、Connection 和 HTTP/1.1 转发配置 |

为了让多个映射域名都能正常访问，将 **RemoteGate 对应站点**反代中已有的 Host 行替换为下方内容，并设置协议头，不重复添加相同配置：

```nginx
proxy_set_header Host $http_host;
proxy_set_header X-Forwarded-Proto $scheme;
```

这里只配置 RemoteGate 对应站点，其他网站保持原样。18088 只供服务器本机反代使用，不用对公网开放，也不用额外开放 8443。

## 3. 打开网页创建管理员

浏览器打开：

```text
https://gate.example.com/install
```

填写管理员账号（默认 `admin`）、密码和后台路径（默认 `/admin`）。**安装后收藏最终地址**，例如：

```text
https://gate.example.com/admin
```

以后使用这个地址登录。安装完成后，`/install` 不再创建账号。已有实例继续保留原账号和入口。

## 4. 接入路由器和访问域名

- 在后台添加设备，按[路由器接入教程](router.md)安装插件并确认在线。
- 按[泛域名与 HTTPS 教程](external-https.md)完成站点绑定、泛域名证书和映射配置。
- 添加路由器或 NAS 的访问地址，检测后打开。

**完成标志：** 后台能登录，设备在线，映射地址能打开目标网站。

## 常见问题

| 问题 | 检查方式 |
| --- | --- |
| 请求来源不匹配 | 确认后台域名填对，`PUBLIC_URL` 没残留旧域名或旧端口；修改后重新部署 |
| 域名打不开 | 检查 DNS、站点证书和反代目标，代理路径应为 `/` |
| `unknown host` | 检查映射域名是否存在，反代是否保留原始 Host |
| 更新后版本没变 | 重新拉取镜像并重新部署；只重启容器不会升级 |

更新时保留原数据目录和 `.env`，见[宝塔更新与备份](maintenance.md#宝塔界面更新)。没有可用反代时，可先用[SSH 临时访问](access.md#方案-b没有反代使用-ssh-隧道)初始化。

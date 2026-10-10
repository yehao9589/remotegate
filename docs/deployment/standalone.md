# 默认方式：RemoteGate 独立提供 HTTPS

> 历史 v0.1.5 配置说明：原有内置入口和证书继续保留兼容。新版后台改为只检测公网 HTTPS，不再提供本文中的网页申请或入口迁移按钮。已有宝塔站点的新部署请使用[宝塔证书与检测](external-https.md)，无需迁移其他网站的 443。

[返回部署指南](../../DEPLOYMENT.md)

后台、路由器连接和映射均由 RemoteGate 提供 HTTPS，不需要宝塔网站、Nginx、Caddy 或数据库。即使用宝塔的 Docker 界面安装，也只需创建容器编排，不需要在“网站”中创建站点。

流程只有一条：**填写首次后台证书配置 → 创建容器 → HTTPS 安装管理员 → 在后台申请泛域名并添加映射。**

> 本篇适用于 v0.1.5 及以上版本。使用 `stable` 时请先拉取新版镜像；旧镜像不支持 `BOOTSTRAP_DOMAIN` 首次证书配置，只修改 `.env` 不会增加新功能。

## 1. 准备域名

本篇使用通用示例 `gate.example.com`。把它的 A / AAAA 解析指向自己的服务器，准备能修改该域名 TXT 记录的 DNS 凭据。

服务器的公网 TCP 443 必须可用并已放行。RemoteGate 直接监听它，日常访问不用加端口。若已有其他服务占用同一 IP 的 443，系统会明确报端口冲突，不会停掉那个服务或删除它的证书；先按自己的部署安排释放入口，再启动独立模式。

### 已有网站：保留原配置，使用独立端口

如果 Nginx 已占用 443，又希望保留所有现有网站配置，可在后台 HTTPS 接入中使用另一个可用端口，例如 `:18443`。系统直接使用自身证书，不需要导入 Nginx；放行公网 TCP 18443 后，映射实际地址为 `https://router.gate.example.com:18443/`。将映射端口同步为实际监听端口，并检查公网 HTTPS。

v0.1.5 可在“高级：自定义监听地址”手动填写；v0.1.6 的检测页面不再提供内置监听配置按钮，旧 API 保留端口检查与可用备选地址。检查只验证服务所在网络的端口绑定，不会修改配置，也不代表 DNS、防火墙或公网连接已通过。

使用标准 443 地址需要一个由该 HTTPS 服务接收的公网入口。托管穿透服务的 443 在服务商的云端，自建部署的入口则由自己的服务器提供；申请证书本身不会改变占用端口的程序。独立端口方案保留现有 Nginx 443，不承诺省略端口。

## 2. 创建容器编排

在宝塔 Docker 或其他支持 Compose 的面板中，新建编排名 `remotegate`，粘贴下面内容。也可以把两段内容分别保存为服务器上的 `docker-compose.yml`、`.env`，通过 Linux Docker Compose 启动。

### compose 内容

```yaml
services:
  remotegate:
    image: ${REMOTE_GATE_IMAGE:-ghcr.io/yehao9589/remotegate:stable}
    restart: unless-stopped
    network_mode: host
    environment:
      CONSOLE_HOST: ${BOOTSTRAP_DOMAIN}
      LISTEN_ADDR: 127.0.0.1:18088
      HTTPS_LISTEN_ADDR: ":443"
      BOOTSTRAP_DOMAIN: ${BOOTSTRAP_DOMAIN}
      BOOTSTRAP_EMAIL: ${BOOTSTRAP_EMAIL}
      BOOTSTRAP_DNS_PROVIDER: ${BOOTSTRAP_DNS_PROVIDER}
      BOOTSTRAP_DNS_ACCESS_KEY: ${BOOTSTRAP_DNS_ACCESS_KEY}
      BOOTSTRAP_DNS_SECRET_KEY: ${BOOTSTRAP_DNS_SECRET_KEY:-}
      BOOTSTRAP_ACME_TERMS_ACCEPTED: "${BOOTSTRAP_ACME_TERMS_ACCEPTED:-false}"
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
BOOTSTRAP_DOMAIN=gate.example.com
BOOTSTRAP_EMAIL=you@example.com
BOOTSTRAP_DNS_PROVIDER=alidns
BOOTSTRAP_DNS_ACCESS_KEY=填写你的AccessKey_ID
BOOTSTRAP_DNS_SECRET_KEY=填写你的AccessKey_Secret
BOOTSTRAP_ACME_TERMS_ACCEPTED=false
```

域名、邮箱、DNS 平台和凭据换成自己的值，数据路径按自己的服务器设置。阅读 [Let's Encrypt 服务条款](https://letsencrypt.org/repository/) 后，将最后一项设置为 `true` 才会申请；系统不会替你默认同意。

| DNS 平台 | `BOOTSTRAP_DNS_PROVIDER` | `BOOTSTRAP_DNS_ACCESS_KEY` | `BOOTSTRAP_DNS_SECRET_KEY` |
| --- | --- | --- | --- |
| 阿里云 DNS | `alidns` | AccessKey ID | AccessKey Secret |
| 腾讯云 DNSPod | `dnspod` | DNSPod API 的 `ID,Token` 组合 | 留空 |
| Cloudflare | `cloudflare` | 目标区域的 DNS API Token | 可选 Zone API Token，通常留空 |

使用负责 DNS 解析的平台，不是只看域名购买平台。DNS 凭据保存于私有数据目录用于续期，普通后台状态接口不返回凭据；不要将 `.env` 或数据目录提交到公开仓库或放入网站目录。

host 网络不填写 `ports`。18088 只绑定本机，用于健康检查和排障，不需要对公网开放。

## 3. 打开 HTTPS 安装页

启动容器后先查看日志。DNS 验证可能需要几分钟；第一次证书尚未签发时，HTTPS 暂时不能握手，这是等待首次证书的阶段。

签发成功后打开：

```text
https://gate.example.com/install
```

创建管理员（账号默认 `admin`，密码自行设置），选择后台路径，例如 `/admin`。完成后访问 `https://gate.example.com/admin`，首次安装入口关闭。

首次只申请 `gate.example.com` 的后台证书，自动续期开启。没有在启动配置申请泛域名，以免把后续映射管理挤进首次安装。

初始化失败时：检查日志、DNS 授权和服务条款开关，修正 `.env` 后重新创建容器。重复尝试至少间隔一分钟；更换环境变量需要重新创建，普通重启不会读取面板里尚未应用的环境修改。系统不会用自签名证书冒充成功，不需要忽略浏览器证书警告。

## 4. 在后台申请泛域名与添加映射

1. 在“域名与证书”打开 `gate.example.com` 的证书配置，勾选主域名和泛域名 `*.gate.example.com`。首次保存的 DNS 凭据可继续使用。
2. 保存并申请。申请成功后系统自动加载新证书，后台和一级子域名均可使用，无需重启、导出或导入其他软件。
3. 在 DNS 平台添加子域名或泛解析到服务器；申请证书只创建 TXT 验证记录，不替你添加网站 A / AAAA 记录。
4. 安装路由器插件，服务器地址填 `https://gate.example.com`，不带后台路径。设备在线后添加映射，例如 `router.gate.example.com` → `http://127.0.0.1:80`。
5. 新映射默认 HTTPS / 443，实际地址显示为 `https://router.gate.example.com/`。检测公网 HTTPS 和设备内网服务后访问。

也可以添加其他主域名并申请独立证书，系统会按访问域名选择匹配的证书。泛域名仅覆盖下一层，不覆盖域名后缀自身或更深一层。

## 5. 后续维护与其他选择

- ACME 续期后新连接自动使用新证书。续期失败继续使用尚未过期的旧证书，并保留任务记录。
- 创建管理员后，`BOOTSTRAP_*` 不再修改网页中的证书配置；后续证书、DNS 授权与申请范围在后台操作。若改变后台访问域名，还需更新 compose 的 `CONSOLE_HOST`（本篇由 `BOOTSTRAP_DOMAIN` 传入），但启动配置不会替已安装系统重新申请证书。
- 备份整个数据目录，包括管理员、域名、证书、DNS 授权与 `https.json`，不要删数据重新安装。[更新与备份](maintenance.md)
- 已有其他来源的证书，可以上传证书链与匹配私钥让系统直接使用，证书不必在宝塔部署。
- 若自愿让前置代理提供后台 HTTPS，仍支持[外部代理方式](access.md#方案-a已有宝塔nginx-反代)，但它不是系统运行的依赖。
- 不希望在 `.env` 提供首次 DNS 凭据时，可使用[本机 / SSH 隧道](access.md#方案-b没有反代使用-ssh-隧道)初始化，再完全通过网页申请第一张证书。这是另一条初始化方式，不必两条都做。

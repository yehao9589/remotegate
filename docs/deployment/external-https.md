# 宝塔泛域名证书与 HTTPS 检测

[返回部署指南](../../DEPLOYMENT.md)

证书的申请、部署和续期在宝塔完成。RemoteGate 后台只检测实际公网入口，不需要主机助手或宝塔 API。检测页面从 v0.1.6 开始提供，旧版本需要拉取新版镜像并重新创建容器。

本篇使用 `gate.example.com` 举例，请替换成自己的域名。只操作 RemoteGate 对应的站点，保留其他网站的端口和配置。

## 1. 配置 DNS

在负责 DNS 解析的平台添加：

| 主机记录 | 类型 | 记录值 | 用途 |
| --- | --- | --- | --- |
| `gate` | A | 服务器公网 IPv4 | 后台域名 `gate.example.com` |
| `*.gate` | A | 同一个服务器公网 IPv4 | `router.gate.example.com` 等子域名 |

DNS 平台若管理的是 `gate.example.com` 这个独立区域，主机记录分别使用 `@` 与 `*`。使用 IPv6 时按实际网络添加 AAAA。已有具体子域名记录优先于泛解析，需检查它是否指向同一入口。

## 2. 绑定 RemoteGate 站点域名

宝塔 → 网站 → RemoteGate 对应站点 → 域名管理，绑定：

```text
gate.example.com
*.gate.example.com
```

域名绑定决定 Nginx 将请求交给哪个站点；DNS 泛解析不能代替这一步。若某个子域名已绑定到其他独立站点，该站点可能优先处理请求，需在实际接收请求的站点配置证书和反代。

## 3. 在宝塔申请并启用证书

在该站点的 SSL 页面：

1. 选择 Let's Encrypt 免费证书。
2. 选择 **DNS 验证**，泛域名不能用文件验证。
3. 申请范围包含 `gate.example.com` 和 `*.gate.example.com`，保留后台主域名覆盖。
4. 在宝塔配置实际 DNS 平台的授权，完成申请后保存并启用。
5. 确认宝塔的自动续期设置及 DNS 授权持续有效。手动添加 TXT 的验证方式需要按宝塔提示处理后续续期。

这可以是一张同时包含主域名与泛域名的证书，不需要给每个一级子域名单独申请。`*.gate.example.com` 仅覆盖 `router.gate.example.com` 这一层，不覆盖 `gate.example.com` 本身或 `a.router.gate.example.com`。

宝塔版本不同，按钮位置可能不同。参考官方[SSL 域名与 DNS 接口管理](https://docs.bt.cn/user-guide/ssl/domain/)与[网站 SSL 部署](https://docs.bt.cn/user-guide/site/php/site-config/ssl)。

## 4. 配置这个站点的反代

| 配置 | 值 |
| --- | --- |
| 代理路径 | `/` |
| 目标 URL | `http://127.0.0.1:18088` |
| 公网 HTTPS 端口 | `443` |

为区分不同映射，反代必须保留访问者使用的域名。把 **RemoteGate 这个站点**反代中原来的 Host 行替换为下面的值，勿重复添加：

```nginx
proxy_set_header Host $http_host;
proxy_set_header X-Forwarded-Proto $scheme;
```

保留原有的 WebSocket 配置：Upgrade、Connection 与 `proxy_http_version 1.1`。后台登录能兼容发送域名为 `127.0.0.1` 的默认配置，但子域名映射需要原始 Host 才能选择对应转发规则。

无需迁移其他站点的 443，无需开放 18088 或 8443 到公网。

## 5. 添加映射并检测

1. 在 RemoteGate 添加 `gate.example.com` 域名后缀。
2. 在设备下添加映射，例如 `router.gate.example.com`，公网协议 HTTPS，端口 443。
3. 进入“域名与 HTTPS”，点击该域名的“HTTPS 检测”。
4. 分别检测主域名与每个具体映射地址。主域名成功不会自动将所有子域名标成成功。
5. 检测通过后打开 `https://router.gate.example.com/`，标准 443 不需要写在地址里。

检测读取 DNS 与实际返回的证书，验证信任链、域名和有效期，显示颁发机构、覆盖域名与到期时间。最近 40 个地址的最新结果随数据目录保存，超过 24 小时提示复检，临近到期显示提醒。

检测不证明设备或内网服务可用：设备需要在线，内网目标需要在设备工作台单独检测。

## 常见问题

| 现象 | 处理 |
| --- | --- |
| 主域名正常，子域名证书错误 | 检查泛域名绑定、当前启用证书的覆盖范围、具体子域名独立站点和 DNS |
| 返回别的网站证书 | 请求可能落到默认站点或另一站点；检查 RemoteGate 的域名绑定 |
| 证书正常但网站打不开 | 检查反代目标和 Host、映射是否启用、设备在线及内网目标可用 |
| 显示上次检测或待复检 | 点击具体地址的检测按钮更新；记录是检测时的快照 |
| 以前在 RemoteGate 申请过证书 | 旧证书文件保留；外部入口实际使用宝塔当前部署的证书。外部管理域名不再由 RemoteGate 自动续期 |

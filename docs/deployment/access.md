# 首次访问、后台入口与 HTTPS

[返回部署指南](../../DEPLOYMENT.md)

容器安装方式和访问方式是两件事。**下方 A、B 选择一条完成首次安装即可，不需要都做。**

| 选择 | 适用场景 | 首次打开什么 | HTTPS 证书由谁管理 |
| --- | --- | --- | --- |
| A：已有反代 | 已有宝塔/Nginx HTTPS 站点 | 站点域名的 `/install` | 宝塔/Nginx |
| B：SSH 隧道 | 暂无可用反代，准备使用内置 HTTPS | 自己电脑的 `127.0.0.1:18090/install` | 后续在 RemoteGate 配置 |

## 方案 A：已有宝塔/Nginx 反代

1. 反代目标指向服务器本机 `http://127.0.0.1:18088`，公网使用站点已有的 HTTPS。
2. 使用 v0.1.3 或更新版本，compose 的 `environment` 传入 `CONSOLE_HOST`，`.env` 填写 `CONSOLE_HOST=gate.fanke.xyz`。默认外部 80/443 端口下 `PUBLIC_URL` 留空；自定义端口时填写完整外部地址，例如 `PUBLIC_URL=https://gate.fanke.xyz:8443`。仅使用反代时关闭 RemoteGate 内置 HTTPS。
3. 容器重新部署后，直接打开 `https://gate.fanke.xyz/install` 创建管理员。
4. 完成后使用保存的后台路径，例如 `https://gate.fanke.xyz/admin`。

这条方案不用在自己的电脑执行 PowerShell 或 SSH。完整可复制配置见[宝塔教程](baota.md)。如果已有站点已能访问后台，沿用实际访问地址即可；同一域名默认 HTTP/HTTPS 切换不用改配置。自定义外部端口或域名变化时修改相应设置并重新部署。

反代兼容仅信任回环地址连接，不自动信任 `X-Forwarded-Host` 等转发头。不申请证书、不启用 HTTPS，设置中不包含 `/admin` 等后台路径。HTTP 登录不会因为 `PUBLIC_URL` 中写了 HTTPS 而错误生成 Secure Cookie；HTTP 和 HTTPS 会话使用不同 Cookie 名，避免切换时被浏览器阻止覆盖。

## 方案 B：没有反代，使用 SSH 隧道

这条方案只用于没有可用域名反代时的首次访问。容器已经启动，但 RemoteGate 尚无证书，因此它的内置 HTTPS 暂时不能握手。

### B1. 保持初始化配置

- `CONSOLE_HOST`、`PUBLIC_URL` 首次留空。
- HTTP 监听 `127.0.0.1:18088`，不对公网开放。
- 内置 HTTPS 可以设置 `HTTPS_LISTEN_ADDR=:443`；443 被占用时换成 `:8443`。

### B2. 在自己的电脑建立隧道

在**自己电脑的 PowerShell 或终端**执行，不是宝塔服务器终端：

```sh
ssh -N -L 18090:127.0.0.1:18088 root@服务器公网IP
```

将服务器 IP 和 SSH 用户替换为实际值。非 22 端口可添加 `-p 实际SSH端口`。连接成功后命令会一直等待，保持这个窗口打开。

浏览器打开：

```text
http://127.0.0.1:18090/install
```

这个地址通过 SSH 加密隧道访问云服务器，不是电脑上的其他本地测试后台。如果 18090 已被占用，只替换命令中第一个端口，并相应调整浏览器端口。

### B3. 创建账号与申请内置证书

1. 创建管理员，保存后台入口；经隧道访问的后台地址例如 `http://127.0.0.1:18090/admin`。
2. 在 DNS 中把后台域名 `gate.fanke.xyz` 指向服务器公网 IP。
3. 在后台“域名与证书”添加 `fanke.xyz`，选择实际负责 DNS 解析的平台。域名购买平台与 DNS 平台可以不同。
4. 在域名下添加证书：使用免费 Let's Encrypt 自动申请并填写 DNS 凭据，或上传已有证书与私钥；确认服务条款后提交。证书需要覆盖后台域名及要使用的映射域名。
5. 证书安装成功后，内置 HTTPS 会加载证书；放行其实际公网端口，再访问 `https://gate.fanke.xyz/admin`。用 8443 时访问 `https://gate.fanke.xyz:8443/admin`。
6. 公网 HTTPS 已验证可用后，再将 `CONSOLE_HOST` 设置为 `gate.fanke.xyz`，重新部署；正常访问 HTTPS 后可关闭 SSH 窗口。直接使用内置 HTTPS 不需要 `PUBLIC_URL`。

本方案的 DNS 凭据按实际服务商填写：阿里云为 AccessKey ID/Secret，DNSPod 为 DNSPod API ID/Token，Cloudflare 为可编辑目标区域 DNS 的 API Token。DNS A 记录需要自行创建；证书签发不会替你建立服务的公网 A 记录。

## 管理员账号与自定义入口

这部分两种访问方案共用：

- 账号默认填写 `admin`，首次安装可以设置。
- 密码至少 8 个字符，确认密码须一致。
- 后台入口默认 `/admin`，可改为 `/my-panel` 等合法路径。
- 安装成功后 `/install` 关闭，根路径不再代替新后台入口，请收藏最终地址。
- 后台“账号与入口”可验证当前密码后修改入口或密码，修改会退出全部会话。
- 旧安装保留原账号与入口；不要为重新进入安装页而删除数据。

路由器填写的服务器地址不含后台路径。例如后台为 `https://gate.fanke.xyz/my-panel`，路由器仍填 `https://gate.fanke.xyz`。

## 设备映射的 HTTPS 入口

管理后台能访问，只代表后台域名接通了。`ceshi01.fanke.xyz` 等映射域名还需 DNS 和实际入口。选择一条方式：

### 使用 RemoteGate 内置 HTTPS

- 在后台添加映射域名对应的主域，并配置覆盖子域名的证书。
- 设置 `HTTPS_LISTEN_ADDR=:8443` 等空闲端口，重新部署，放行该端口。
- 将映射的公网协议/端口填为 HTTPS/8443，访问 `https://ceshi01.fanke.xyz:8443/`。
- 原宝塔后台继续使用 `https://gate.fanke.xyz/admin`，它的 443 证书与 RemoteGate 的 8443 证书分别由各自入口管理。

### 使用宝塔/Nginx HTTPS

- 映射域名指向服务器 IP，并在前置站点配置覆盖这些域名的证书。
- 反代到 `127.0.0.1:18088`，**映射域名必须保留原始 Host**，否则 RemoteGate 无法区分不同映射。
- 对映射域名的反代，将已有 Host 行替换为 `proxy_set_header Host $http_host;`，不要重复添加；HTTPS 入口同时传递 `proxy_set_header X-Forwarded-Proto $scheme;`。
- 映射公网协议与端口按前置入口填写，例如 HTTPS/443。

`PUBLIC_URL` 只指定后台的一个外部地址，不能恢复被代理丢掉的多个映射域名。RemoteGate 的证书申请不会自动修改宝塔的证书文件。

## 访问问题排查

| 现象 | 检查 |
| --- | --- |
| 请求来源不匹配 | 版本至少 v0.1.3；compose 确实传入 `CONSOLE_HOST` 或 `PUBLIC_URL`；域名及自定义端口与访问一致；代理连接来自回环地址；改配置后重新创建容器。同域名默认 HTTP/HTTPS 均可使用 |
| 进入 `/install` 返回 404 | 若已安装，改用保存的后台入口；从 `/admin` 修改入口后也要用新地址 |
| 内置 HTTPS 不能握手 | 确认证书已安装、覆盖访问域名、内置监听已开启；健康检查只证明 HTTP 进程可用 |
| 只有后台能访问，映射不能访问 | 检查映射域名 DNS、入口端口、证书覆盖和 Host 转发；设备应在线 |
| 配置 `CONSOLE_HOST` 后 SSH 本地地址返回 unknown host | 初始化应在设置专用后台域名前完成；后续排障如需隧道，临时用 hosts 将该后台域名指向电脑回环地址，并带本地隧道端口访问，结束后移除临时记录 |

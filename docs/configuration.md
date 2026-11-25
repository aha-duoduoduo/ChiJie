# 配置说明

## 配置文件

- `configs/gateway.yaml`：服务器端口、TLS、认证密钥、Admin 鉴权和日志
- `configs/nodes.yaml`：节点池配置
- `configs/fingerprints.yaml`：TLS 指纹库

实际配置文件已加入 `.gitignore`，仓库提供 `*.example` 模板。首次部署：

```bash
cp configs/gateway.yaml.example configs/gateway.yaml
cp configs/nodes.yaml.example configs/nodes.yaml
cp configs/fingerprints.yaml.example configs/fingerprints.yaml
# 编辑 gateway.yaml，至少要：
#   - 设置 admin.password（或保持 listen=127.0.0.1 并留空 password）
#   - 设置 admin.jwt_secret（长度 ≥ 16，禁止占位字符串）
```

启动会拒绝以下不安全配置：
- `admin.jwt_secret` 为空、长度 < 16，或为已知占位字符串。
- `admin.password` 为空且 `admin.listen` 监听非 127.0.0.1/localhost。

## 安全相关配置项

```yaml
server:
  # 是否允许代理和中继拨向私网/回环地址。默认 false 防 SSRF。
  allow_private_targets: false

admin:
  # 登录失败速率限制：login_window 内累计 login_max_failures 次失败后锁定 login_lockout。
  login_max_failures: 5
  login_window: "60s"
  login_lockout: "5m"
  # 默认只使用连接来源 IP；仅为实际受信且清理转发头的反向代理配置 CIDR。
  trusted_proxies: []

health_check:
  interval: "30s"
  timeout: "5s"
  url: "https://www.google.com/generate_204"
  max_fail: 3

proxy:
  # 单次 /proxy 通过某个出口访问目标站点时等待响应头的超时。
  response_header_timeout: "3s"
  # 单次 /proxy 所有尝试与重试等待共用的总超时。
  total_timeout: "30s"
  # 单次 /proxy 出口执行失败时最多尝试的可用静态/订阅节点数量。
  max_attempts: 5
  # follow_redirects=true 时单次请求最多跟随的 HTTP redirect 次数。
  max_redirects: 5
  # 可用节点全部失败后继续尝试同地区同类型模板节点。
  template_fallback_after_attempts: true
  domain_retries: []
```

## 资源上限与网络行为

`/proxy` 单次请求 body 上限 10 MB，上游响应 body 上限 32 MB；Admin API JSON body 上限 1 MB。

订阅拉取只允许 `http` / `https`，默认拒绝私网/回环/保留地址；受信任的订阅池可单独设置 `allow_private_host: true`，允许直接填写本地 IP，或使用解析到本地地址的域名。单次订阅响应 body 上限 4 MB。受控直连拨号直接使用已校验 IP，保留原始 Host/SNI，阻止校验与连接之间的 DNS 重绑定。

出口节点拨号、订阅地址校验和 sing-box DNSRouter 使用内置公共 DNS resolver，默认查询 `1.1.1.1:53` 和 `8.8.8.8:53`，避免 Docker 内置 `127.0.0.11` 解析失败影响节点可用性。

Admin 登录限速默认使用 `RemoteAddr`。只有直接连接方命中 `admin.trusted_proxies` 时才读取转发 IP 头；`X-Forwarded-For` 从右向左解析至首个非受信地址。受信代理必须覆盖或清理客户端提供的转发头。空密码管理页面只接受 localhost/回环 Host，管理 API 拒绝跨站 Origin 和 Fetch Metadata。

`/tunnel` 每条消息上限 128 KiB，未认证首帧最多并行等待 64 个连接。WebSocket 默认拒绝跨站浏览器升级（无 Origin 放行；有 Origin 必须同源），不影响 Cloudflare Workers / Go / Node.js / Python 等服务端客户端。

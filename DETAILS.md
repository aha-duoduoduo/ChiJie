# Chijie 目录结构说明

```
chijie/
├── .github/
│   └── workflows/
│       └── container.yml        # GitHub Actions：测试并构建 Linux AMD64 镜像，发布到 GHCR
├── cmd/
│   └── gateway/
│       ├── main.go              # 入口：加载/校验配置、初始化各模块、启动并优雅关闭 HTTP 与 Admin 服务
│       └── main_test.go         # 入口集成测试
├── internal/
│   ├── buildinfo/
│   │   └── buildinfo.go         # 构建时提交号和时间；源码构建回退到 Go VCS 元数据
│   ├── server/
│   │   ├── server.go            # HTTP Server：生命周期、/proxy 请求协议和流量记录
│   │   ├── proxy_request.go     # 目标请求、候选路由、节点错误分类、HTTP transport 复用
│   │   ├── proxy_retry.go       # 域名与连接错误重试、总预算、Retry-After 和节点切换
│   │   ├── retry_policy.go      # 域名规则校验、精确匹配和安全方法范围
│   │   ├── egress.go            # 出口解析、受控直连与远端网关客户端
│   │   ├── proxy_settings.go    # 运行时代理配置、校验及快照
│   │   ├── sessions.go          # 活跃会话登记、取消、关闭和等待
│   │   ├── session_traffic.go   # TCP/UDP 共用字节计数与会话日志
│   │   ├── tunnel.go            # WebSocket 隧道：Origin 校验、首帧协议、上游 WebSocket 握手、raw TCP 转发、SSRF 防护
│   │   ├── relay_http.go        # WSS TCP/UDP 中继入口
│   │   ├── relay_route.go       # 中继出口选择、粘性和跨网关转发
│   │   ├── socks.go             # SOCKS5 CONNECT、账号认证及监听器
│   │   ├── socks_udp.go         # SOCKS5 UDP ASSOCIATE 与数据报转发
│   │   ├── socks_accounts.go    # SOCKS 账号和 bcrypt 哈希持久化
│   │   ├── tun.go               # Linux TUN 整机 TCP/UDP 入口
│   │   ├── guarded_dialer.go    # 直连出口的 SSRF 守卫包装
│   │   └── auth.go              # 认证：Bearer JWT 和持久访问密钥验证
│   ├── auth/
│   │   ├── claims.go            # 共享的 JWT Claims 结构（admin / proxy 标志）
│   │   ├── storage_key.go       # 独立存储密钥与旧格式迁移
│   │   └── access_keys.go       # 可列出、复制、注销的加密访问密钥
│   ├── netguard/
│   │   └── netguard.go          # 私网/回环/保留段 IP 黑名单与受控拨号
│   ├── util/
│   │   ├── strings.go           # 跨模块共享的字符串工具：FirstNonEmpty / ContainsString / RemoveString / ParseInt / SplitList
│   │   └── logger.go            # 日志分级：Debugf / Infof / Warnf / Errorf，受 log.level 控制
│   ├── pool/
│   │   ├── manager.go           # 节点池配置定义、加载及来源构建
│   │   ├── selection.go         # 出口候选、策略和地区组选择
│   │   ├── templates.go         # 模板、远端网关和兜底选择
│   │   ├── node_state.go        # 节点状态、地区及元数据匹配
│   │   ├── config_snapshot.go   # 配置深拷贝，隔离运行状态修改
│   │   ├── lifecycle.go         # 旧池资源回收和有界模板拨号器缓存
│   │   ├── subscription_runtime.go # 订阅刷新与可取消定时任务
│   │   ├── status.go            # 节点池运行状态：订阅最近拉取结果、Admin 状态 DTO、节点及地区组状态快照
│   │   ├── node_metadata.go     # 节点元数据：标签、家宽/高端标识、别名和 server:port 稳定键
│   │   ├── subscription.go      # 订阅解析：多格式与多地址解析、池级本地地址许可、重定向/拨号校验、URL 与响应大小限制
│   │   ├── subscription_cache.go # 订阅成功结果缓存：URL 哈希索引、0600 原子写入、重启失败恢复
│   │   └── health.go            # 健康检查：按池配置后台探测节点连通性、延迟和模板即时测试
│   ├── dialer/
│   │   ├── dialer.go            # 统一 Dialer 接口定义、Node 结构、工厂方法
│   │   ├── lifecycle.go         # 出口资源所有权、连接引用及 HTTP transport 缓存
│   │   ├── endpoint_error.go    # 代理端点连接错误类型
│   │   ├── direct.go            # 直连 Dialer
│   │   ├── socks5.go            # SOCKS5 代理 Dialer
│   │   ├── http_proxy.go        # HTTP Proxy Dialer（CONNECT 隧道）
│   │   ├── singbox.go           # Shadowsocks / VMess / VLESS / Trojan / Hysteria2 出站适配
│   │   ├── vless_xhttp.go       # VLESS XHTTP 专用拨号器：packet-up、stream-up、stream-one、auto
│   │   └── resolver.go           # 出口节点域名使用的公共 DNS 拨号器
│   ├── fingerprint/
│   │   ├── fingerprint.go       # 指纹库管理：YAML 加载、预设/JA3/字符串配置解析、BuildSpec
│   │   ├── http2_transport.go   # HTTP/2 指纹 transport：uTLS 握手、HTTP/2 首帧、响应 body 上限
│   │   ├── http2_exchange.go    # 并行收发、双层流量窗口、HPACK 和 trailers
│   │   └── transport.go         # Transport 包装：替换 DialTLSContext 注入 uTLS 指纹，提供独立 TLS 拨号入口
│   ├── traffic/
│   │   ├── store.go             # 请求记录、有效指标、折叠展示、活跃隧道计数
│   │   ├── grouping.go          # 失败请求合并与 URL Query 规范化规则
│   │   ├── success_folding.go   # Host + Path 的 200 折叠与统计排除规则
│   │   ├── persistence.go       # 每日 JSONL 持久化、启动恢复和按保留天数清理
│   │   ├── persistence_test.go  # Traffic 重启恢复、文件权限和过期日期清理测试
│   │   └── store_test.go        # 流量统计测试
│   └── admin/
│       ├── api.go               # 管理服务初始化与路由注册
│       ├── auth_api.go          # 登录、JWT 和访问密钥接口
│       ├── request_security.go  # 受信代理 IP、Host 和跨站请求检查
│       ├── node_pools_api.go    # 节点池增删改
│       ├── nodes_api.go         # 静态节点和启停接口
│       ├── node_config.go       # 配置验证、保存和订阅更新注册
│       ├── subscription_api.go  # 订阅刷新和元数据接口
│       ├── system_api.go        # 重载、统计、运行参数和配置导出
│       ├── domain_retry_api.go  # Traffic 单域名规则更新，保留其他代理设置
│       ├── connectivity_api.go  # 节点和模板连通性探测
│       ├── fingerprints_api.go  # 指纹配置接口
│       ├── fingerprint_probe.go # 受控 TLS 探测及结果解析
│       ├── socks_api.go         # SOCKS 账号管理 API
│       ├── traffic_api.go       # Traffic API：日志查询、保留设置、失败规范化规则和 200 折叠规则持久化
│       ├── login_limiter.go     # 按 IP 维度的登录失败速率限制
│       ├── yaml_io.go           # 通用 YAML 加载/原子写入（0600 权限）
│       └── dist/                # 前端构建产物（embed 到二进制）
├── web/
│   ├── src/
│   │   ├── app.jsx              # 前端入口：登录、后端数据加载、页面切换、统一动作分发
│   │   ├── api.jsx              # Admin API 客户端：JWT、请求错误、字段转换、节点/订阅/TLS/流量接口
│   │   ├── data.jsx             # 地区名称、ISO-2 自动旗帜和地区组函数
│   │   ├── ui.jsx               # 共享 UI：弹窗、抽屉、请求详情、开关、分段控件、图表、Toast
│   │   ├── domain-retry-rules.jsx # Traffic 与 System 共用域名重试编辑器
│   │   ├── icons.jsx            # 控制台图标
│   │   ├── page-overview.jsx    # Overview：运行状态、节点在线数、成功率、地区组、最近成功和错误请求及详情抽屉
│   │   ├── page-egress.jsx      # Egress：地区组、节点表、节点启停、测试、静态节点新增/删除
│   │   ├── page-subscriptions.jsx # Subscriptions：订阅新增、刷新、最近拉取结果、启停、本地地址许可、元数据、reject_regex 保存和窄屏布局
│   │   ├── page-templates.jsx   # Templates：模板池新增、编辑、启停、删除、优先级、覆盖范围和按地区测试
│   │   ├── page-tls.jsx         # TLS Profiles：指纹新增、删除、测试
│   │   ├── page-traffic.jsx     # Traffic：请求日志、流量序列、详情抽屉、CSV 导出
│   │   ├── page-socks.jsx       # SOCKS 账号、出口策略和连接信息
│   │   └── page-system.jsx      # System：运行统计、配置重载、日志级别、请求日志保留天数和访问密钥管理
│   ├── scripts/
│   │   └── build.mjs            # esbuild：内容哈希脚本、HTML 引用更新及静态资源复制
│   ├── uploads/                 # 原型参考图和截图素材
│   ├── favicon.ico              # 浏览器 favicon，内含 16/32/48 PNG 图标
│   ├── favicon-16x16.png        # 小尺寸浏览器 PNG 图标
│   ├── favicon-32x32.png        # 常规浏览器 PNG 图标
│   ├── apple-touch-icon.png      # iOS / Safari 添加到主屏幕图标
│   ├── icon-48.png              # favicon.ico 的 48px 源图
│   ├── icon-192.png             # PWA / Android 192px PNG 图标
│   ├── icon-512.png             # PWA / Android 512px PNG 图标
│   ├── eslint.config.js        # 未定义变量、未使用变量和 JSX 引用检查
│   ├── package-lock.json       # 前端依赖锁定
│   ├── package.json             # npm run build 入口
│   ├── index.html               # 静态 HTML、样式和脚本加载顺序
│   └── dist/                    # npm run build 产物
├── configs/
│   ├── .runtime/                # 运行数据：订阅、Traffic、SOCKS 账号和访问密钥（不提交 Git）
│   ├── gateway.yaml             # 主配置：端口、TLS、认证密钥、Admin 鉴权、日志
│   ├── gateway.docker.yaml.example # Docker 场景主配置模板
│   ├── nodes.yaml               # 节点池配置
│   ├── nodes.yaml.example       # 节点池配置模板
│   ├── fingerprints.yaml        # TLS 指纹库
│   └── fingerprints.yaml.example # TLS 指纹库空模板
├── docs/
│   ├── admin-frontend.md          # Admin 前端接入、构建方式和接口边界
│   ├── admin-api.md               # Admin API 鉴权、端点和流量日志指标
│   ├── configuration.md           # 配置文件、安全配置项和资源上限
│   ├── docker-deployment.md       # Docker 构建、Compose 部署和端口模型
│   ├── egress-protocols.md        # 出口协议、订阅格式和订阅刷新缓存
│   ├── images/                    # README 截图（演示数据）
│   ├── parameter-driven-egress.md # 参数驱动出口模型
│   ├── premium-egress.md          # 高端出口节点配置和请求语义
│   ├── relay-socks.md             # WSS 中继、SOCKS5、Linux TUN 和访问密钥
│   ├── proxy-client-usage.md      # 外部服务接入 Proxy API 的独立使用文档
│   ├── subscription-routing.md    # 订阅节点、地区组和模板节点说明
│   ├── template-fallback.md       # 模板 fallback、远端 Chijie 和优先级规则
│   ├── tls-fingerprints.md        # TLS 指纹来源、extra_fp 兼容和测试接口语义
│   └── traffic-url-grouping.md    # Traffic URL 参数忽略和失败合并规则
├── build.sh                     # 构建脚本：前端 build + 复制 dist + Go 编译
├── Dockerfile                   # 多阶段容器构建：前端 build + Go build + 运行镜像
├── docker-compose.yml           # Docker 部署：Proxy API 暴露，Admin 仅绑定宿主机本机地址
├── docker-compose.prod.yml      # 镜像部署：服务器只拉取镜像运行
├── docker-compose.tun.yml       # Linux host 网络 TUN 部署模式
├── pkg/relay/                   # 可供受控客户端直接使用的 WSS 中继协议包
├── docker-compose.amd64.yml     # ARM 机器构建 X86 镜像时使用的 Compose 覆盖文件
├── .env.example                 # Docker 镜像名、宿主机端口、配置目录和时区模板
├── .dockerignore                # Docker 构建上下文排除规则
├── go.mod
├── go.sum
├── README.md
├── TODO.md
└── DETAILS.md                   # 本文件
```

## 模块职责

### server（请求入口）

接收 `POST /proxy` 请求，完成认证、JSON 解包、`egress` 参数解析、出口候选排序、TLS 指纹包装、目标请求执行、按请求跟随 redirect、上游 `Location` / `Set-Cookie` 响应头转发、出口失败重试、失败节点即时离线、模板延后兜底、响应大小限制和流量记录。

`ProxyRequest` 当前字段：

- `url`：目标 URL。
- `method`：目标请求方法，缺省为 `GET`。
- `headers`：目标请求 Header，包括调用方显式传入的 `Cookie`。
- `payload`：目标请求 Body。
- `follow_redirects`：是否自动跟随 HTTP redirect，缺省为 `false`。
- `egress`：出口参数，包括 `region`、`any`、`max_latency_ms`、`strategy`、`residential`、`premium`、`tls_fingerprint`。

WebSocket 隧道 `/tunnel` 使用同一套 `egress` 参数。连接升级后读取首帧 JSON，通过首帧 `authorization` 或握手 `Authorization` Header 完成认证。`ws://` / `wss://` 目标会执行上游 WebSocket 握手并使用首帧 `headers` 与 `payload`；`http://` / `https://` 目标保持 raw TCP 转发。`wss://` 上游握手支持通过当前出口应用请求级 TLS 指纹。

### pool（节点池）

管理多个节点池，支持四种来源：

- `direct`：直连出口。
- `static`：手动配置的固定节点。
- `template`：按地区动态生成代理节点或转发到远端 Chijie。
- `subscription`：从订阅地址自动拉取节点，支持 Clash YAML、Base64 URI 列表和纯 URI 列表。

核心选择入口：

- `SelectEgress(region, strategy, residential)`：按请求参数选择出口，保留给普通/家宽旧调用。
- `SelectEgressFor(region, strategy, selector)`：按请求参数、家宽类别和高端偏好选择出口。
- `SelectAnyEgress(strategy, residential, maxLatency)`：不指定地区时选择任意非直连静态/订阅出口。
- `SelectAnyEgressFor(strategy, selector, maxLatency)`：不指定地区时按家宽类别和高端偏好选择任意非直连静态/订阅出口。
- `NormalizeRegionCode(value)`：标准化二字母地区码。
- `NormalizeStrategy(value)`：标准化选择策略。
- `EgressGroup(region, residential)`：生成地区组代码，例如 `US`、`US-RES`。
- `EgressGroupFor(region, selector)`：生成地区组代码；`premium` 不改变组名。
- `AnyEgressGroupFor(selector)`：生成地区无关组代码；`premium` 不改变组名。

选择规则：

- 普通请求使用该家宽类别下的全部节点和模板，包含高端节点和高端模板。
- 家宽请求只使用家宽节点和家宽模板。
- 高端请求把高端节点和高端模板排在前面；高端不可用或尝试失败时回到原有候选和 fallback。
- 静态节点和订阅节点优先，单次 `/proxy` 默认最多尝试 5 个可用节点。
- 模板节点按 `priority` 降序尝试，同优先级按池名排序。
- 地区无关请求只使用非 `direct` 的静态节点和订阅节点，不使用直连或模板节点。
- 显式地区请求开启 `proxy.template_fallback_after_attempts` 时，可用节点尝试失败后继续使用同类型模板节点；地区组无可用节点时也使用同类型模板节点。
- 模板节点默认支持任意二字母地区码。
- 多个候选出口按 `random`、`round-robin` 或 `least-latency` 选择。

订阅池附加能力：

- 多订阅地址，使用换行、英文逗号或 `|` 分隔。
- 自动地区识别，也支持 `node_regions` 手动修正。
- 支持 `node_aliases`、`node_tags`、`node_server_aliases`、`node_server_tags`、`node_server_regions`、`region_group_names` 和 `reject_regex`。
- 拉取失败记录为池级错误，不阻断其他节点池加载；运行时已有旧节点时继续保留上一次成功节点。
- 成功拉取写入 `.runtime/subscriptions.json`；进程重启后的首次拉取失败时按 URL 哈希恢复缓存节点。
- 订阅拉取只允许 `http` / `https` 公网目标，单次响应 body 上限 4 MB。
- `update_interval` 支持分钟、小时、天；留空表示只手动刷新。
- `enabled: false` 的订阅池在启动、配置重载、手动刷新和定时更新阶段均不发起订阅请求；重新启用后再执行实时拉取。
- `try_offline` 允许某地区只有一个离线订阅节点时继续尝试该节点。
- 后台健康检查记录 `Alive`、`Latency` 和连续失败次数，默认参数来自 `gateway.yaml` 的 `health_check`，每个池仍可用 `health_check.interval`、`timeout`、`url`、`max_fail` 覆盖。

### dialer（出口拨号）

统一的 Dialer 接口，根据节点类型创建具体连接方式：

- `direct`：直连。
- `socks5`：SOCKS5 代理。
- `http_proxy` / `http`：HTTP CONNECT 代理。
- `ss` / `shadowsocks`：Shadowsocks。
- `vmess`：VMess。
- `vless`：VLESS。
- `trojan`：Trojan。
- `hysteria2` / `hy2`：Hysteria2。
- `anytls`：AnyTLS。
- `tuic`：TUIC。

Reality、AnyTLS / TUIC 代理侧 uTLS 指纹依赖 `with_utls` 构建标签，`build.sh` 默认使用 `go build -tags with_utls`。

### fingerprint（TLS 指纹）

负责 TLS 指纹配置加载和请求级指纹字符串解析。

支持来源：

- 指纹库中的命名配置。
- 内置预设：Chrome / Firefox / Safari / iOS / Edge / 360 / QQ / Random。
- JA3 字符串。
- JA4 raw 列表。
- Akamai raw 字符串。
- TLS / HTTP2 详细 raw 字段。
- 检测站 JSON 导入：`tls.ja3`、`tls.ja4_r`、`http2.akamai_fingerprint`、`http2.sent_frames`。
- YAML 或 JSON 格式的 `FingerprintConfig` 字符串。
- JA3 的 `extra` 参数，以及 curl_cffi 风格的 `extra_fp` 参数。

JA3/JA4/Akamai 都按 raw 输入保存，测试结果只展示远端返回的真实观测字段。HTTP/2 指纹请求会写入 Akamai raw 中的 SETTINGS、WINDOW_UPDATE 和伪头顺序，并限制响应 body。`region` 为空的直连请求同样可以应用 TLS 指纹。详细字段和测试接口语义见 `docs/tls-fingerprints.md`。

### traffic（流量记录）

记录 HTTP 请求和 WebSocket 隧道的运行结果：

- 请求类型、方法、URL、目标标识。
- 地区、地区组、策略、家宽标识、高端标识。
- 出口池、出口节点、来源类型、是否模板。
- TLS 指纹、状态码、耗时、字节数和错误文本。
- 原始 trace、合并展示 trace、分钟级有效请求数、成功率、成功请求平均延迟 / P95、活跃隧道数、持久化设置和折叠规则。

原始 trace 由 `persistence.go` 按本地日期写入 `.runtime/traffic/traffic-YYYY-MM-DD.jsonl`，默认保留 7 天，启动时恢复最近 1000 条。失败请求按 `kind + url/target + egress_group` 合并为有效错误，不把 header、payload、出口节点或策略纳入合并键。`traffic.failure_grouping.url_normalization.rules` 支持在生成合并键前删除易变 query 参数。

`traffic.success_folding.rules` 匹配 Host + Path；命中的 200 响应按 `kind + host/path + egress_group` 折叠展示，保留原始 trace，但从有效请求、成功率、延迟、字节数和分钟序列排除。Admin 请求详情可生成失败 Query 规范化规则或成功 Path 折叠规则；完整说明见 `docs/traffic-url-grouping.md`。

### admin（管理 API）

独立端口管理接口，默认 `127.0.0.1:9090`。

主要能力：

- JWT 鉴权：密码登录获取 token，未配置密码时不启用。
- 节点池 CRUD：添加、更新、删除节点池。
- static 节点 CRUD：编辑和删除静态节点。
- subscription 节点元数据：地区修正、别名、标签。
- 订阅刷新：手动刷新指定订阅池。
- 节点启停：更新运行时状态，并持久化到 `nodes.yaml`。
- 节点连通性测试：返回代理连接阶段、HTTP 状态、出口 IP、实际国家码、IP 类型、ASN/ISP 信息和错误文本。
- 模板地区连通性测试：按模板池、地区和可选测试 URL 生成临时节点后即时探测，并返回代理连接阶段、HTTP 状态、出口 IP、实际国家码、IP 类型、ASN/ISP 信息和错误文本。
- TLS 指纹 CRUD 和真实 HTTPS 目标测试。
- `POST /api/reload`：重载 `nodes.yaml` 和 `fingerprints.yaml`。
- `GET /api/stats`：返回运行时长、节点池数量、指纹数量和流量指标。
- `GET /api/traffic`：返回原始请求记录、合并展示记录、时间序列和聚合指标。
- `PUT /api/system/logging`：修改运行时日志级别，并写回 `gateway.yaml`。
- `GET /api/system/health-check` / `PUT /api/system/health-check`：读取或保存全局健康检查默认参数，并写回 `gateway.yaml`。
- `GET /api/system/proxy` / `PUT /api/system/proxy`：读取或保存 `/proxy` 响应头等待超时、完整请求总超时、重试次数和模板兜底设置，并写回 `gateway.yaml`。
- `PUT /api/system/proxy/domain-retries`：新增或修改单域名重试规则，Traffic 与 System 共用运行时设置，详见 [按域名重试](docs/domain-retries.md)。
- `GET /api/config/export`：导出当前配置目录下的 YAML 配置快照。

规则管理 API 已从后端移除。当前 `web/` 静态原型已接入 Admin API，构建产物复制到 `internal/admin/dist/` 后会随 Go 二进制嵌入。

## 安全模型

- **配置文件治理**：`configs/gateway.yaml` 与 `configs/nodes.yaml` 由 `.gitignore` 排除，仓库只保留 `*.example` 模板。启动时校验 `admin.jwt_secret` 非空且非占位、长度 ≥ 16；`admin.password` 为空时仅允许 admin 监听 127.0.0.1/localhost。
- **SSRF 防护**：`internal/netguard` 维护私网/回环/CGNAT/保留段黑名单（IPv4/IPv6 同时覆盖）。`/proxy`、`/tunnel`、`/api/fingerprints/test` 在解析目标 host 后强制走黑名单校验，直连出口在拨号阶段二次校验防止 DNS rebinding。`gateway.yaml` 的 `server.allow_private_targets: true` 可关闭代理目标防护（默认关闭）。订阅拉取独立使用池级 `allow_private_host`，默认拒绝本地地址；开启后同时允许直接 IP、解析到本地地址的域名及其重定向目标。
- **登录暴力破解防护**：`admin/login_limiter.go` 按客户端 IP 累计失败次数，`login_window` 内达到 `login_max_failures` 后锁定 `login_lockout`，登录成功立即清零。Cloudflare 部署优先使用 `CF-Connecting-IP` / `True-Client-IP`。密码比较使用 `crypto/subtle.ConstantTimeCompare`。
- **WebSocket Origin 校验**：`/tunnel` 在升级阶段校验 `Origin`：无 Origin（CF Workers / Go / Node.js / curl 等服务端客户端）放行；有 Origin 时必须与请求 `Host` 同源，否则拒绝。该策略阻挡浏览器跨站发起的 CSWSH。
- **请求与响应大小限制**：`/proxy` 请求 body 上限 10 MB，上游响应 body 上限 32 MB；Admin API 请求 body 上限 1 MB（`http.MaxBytesReader`）；订阅响应 body 上限 4 MB。
- **CORS 收紧**：`/proxy` 不再无差别返回 `Access-Control-Allow-Origin: *`，仅在请求 Origin 与 Host 同源时回显该 Origin。
- **配置文件持久化权限**：`nodes.yaml` 与 `fingerprints.yaml` 由后端写入时使用 0600 权限，避免同机其他用户读取上游凭据。

## 日志分级

`log.level` 配置项现在生效。`internal/util/logger` 提供原子性的级别控制：

- `debug`：输出每次代理 / 隧道请求的细节（出口选择、耗时、字节数）
- `info`（默认）：服务启动、节点池加载、订阅刷新等运行信息
- `warn`：可恢复的运行警告（出口解析失败、目标拒绝、订阅 403 等）
- `error`：仅输出错误

通过 `gateway.yaml` 的 `log.level` 设置；空值或未识别值回落为 `info`。

## 优雅关闭

进程收到 SIGINT/SIGTERM 时按以下顺序在 5 秒超时内完成：

1. `server.Shutdown(ctx)` — 等待 `/proxy` 与 `/tunnel` 在途请求结束。
2. `admin.Shutdown(ctx)` — 等待 Admin API 在途请求结束。
3. `healthChecker.Stop()` — 取消健康检查协程。
4. `poolMgr.StopSubscriptionUpdater()` — 取消订阅自动更新协程并 wait 全部退出。

`StartSubscriptionUpdater` 重复调用前会先停掉旧协程，避免 reload 时旧 ticker 泄漏。

## 构建流程

```bash
# 1. 前端构建（静态复制 index.html 和 src）
cd web && npm run build

# 2. 复制产物到 internal/admin/dist（embed 需要同目录或子目录）
cp -r web/dist internal/admin/dist

# 3. Go 编译（embed 自动打包 dist；with_utls 用于 Reality / uTLS 指纹）
go build -tags with_utls -o chijie ./cmd/gateway/

# 或使用 build.sh 一键完成
./build.sh
```

## 请求流程

参数模型详见 `docs/parameter-driven-egress.md`。订阅节点、地区组和模板节点详见 `docs/subscription-routing.md`。高端出口详见 `docs/premium-egress.md`。

```
# HTTP 代理
POST /proxy
  → auth
  → 解包 JSON
  → resolveEgress()
  → pool.SelectEgress()
  → [HTTP/1.1 WrapTransportWithDialContext 或 HTTP/2 指纹 transport]
  → 可选跟随 redirect（受 proxy.max_redirects 限制）
  → http.Client.Do()
  → 返回目标响应

# WebSocket 隧道
WS /tunnel
  → auth
  → 升级 WS
  → 读首帧 JSON
  → resolveEgress()
  → pool.SelectEgress()
  → DialContext
  → 双向转发
```

2026-09-30 的模块拆分、资源所有权和回归证据见 [审计修复记录](docs/audit-fixes-2026-09-30.md)。

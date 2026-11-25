# 审计修复记录

日期：2026-09-30。修复基准：`ab0116afd87905757517966f96692084e8412dfd`，对应 2026-09-26 审计的 F01–F20。

## 修复与证据

| 编号 | 修复结果 | 验证位置或方式 |
| --- | --- | --- |
| F01 | `netguard.Guard` 解析并连接已校验 IP，受控直连、订阅和 TLS 探测共用；TCP relay 也传递固定 IP | `server/regression_test.go` 的 DNS 重绑定测试、`netguard` 测试 |
| F02 | `/tunnel` 升级后立即限制消息为 128 KiB，认证前并发上限 64，超额返回 429 | 超大首帧、未认证并发、退出后容量释放测试 |
| F03 | 默认按直接连接 IP 限速；显式受信代理 CIDR 才能提供转发头 | 伪造 CF 头限速回归、可信代理与 XFF 顺序测试 |
| F04 | 空密码管理模式限制 Host 为 localhost/回环地址；API 检查 Origin 和 Fetch Metadata | 跨站创建 SOCKS 账号被拒绝、恶意 Host/Origin 测试 |
| F05 | 在重试前校验请求头；只有代理端点连接错误影响共享节点健康 | 非法 Header 不使节点离线；端点连接错误仍使节点离线 |
| F06 | HTTP/2 上传与读取控制帧并行，遵守连接/流窗口及对端 SETTINGS，下载补充窗口 | 真实本地 `http2.Server`，2.4 MB 上传、1.2 MB 下载及小窗口测试 |
| F07 | Go/Docker 工具链升级到 1.26.8；更新 x/crypto、x/net、x/text、x/mod、compress 等修复版本；发布前增加验证任务 | Linux AMD64、`with_utls` 漏洞扫描和交叉编译；详见下表 |
| F08 | SOCKS5 使用 ContextDialer；HTTP CONNECT 握手支持取消和截止时间，保留缓冲数据 | 本地不回复握手的代理取消测试 |
| F09 | 区分最终响应头、informational headers 与 trailers，保持 HPACK 解码状态 | 404 + body + trailers 保留状态、响应头和尾部字段 |
| F10 | 指纹库加读写锁并返回独立配置；节点池状态在锁内深拷贝 | 指纹重载、节点启停与 JSON 快照并发竞争测试 |
| F11 | relay `Stream.Write` 自动分段，每条消息不超过 128 KiB | 单次 256 KiB 写入真实 echo 回归 |
| F12 | 粘性缓存硬上限 10,000，按最近使用顺序淘汰；key 长度上限 256 字节 | 插入超过上限后仍为 10,000 |
| F13 | relay、SOCKS、TUN 使用共用会话与字节计数，关闭时保存最终统计 | relay 和 SOCKS TCP/UDP 实际收发、活跃计数及关闭后字节断言；TUN 共用转发层 |
| F14 | duration 必须为正数，拒绝非法、溢出、NaN/Inf 天数 | API 和 duration 边界测试 |
| F15 | 拨号器持有连接引用、关闭底层出站资源；池替换后保留正在使用的连接；有界模板/transport 缓存 | 活跃连接跨资源退休继续收发；连续 HTTP 请求复用同一 TCP 连接；既有池重载测试 |
| F16 | 管理 API 复用拨号器协议校验，接受 AnyTLS/TUIC | 管理端协议校验测试 |
| F17 | 新增、修改节点池共用保存/加载/注册定时更新流程；停止更新会取消网络请求 | 本地订阅 HTTP 服务验证新增后自动刷新及取消挂起请求 |
| F18 | 存储密钥与 JWT 签名密钥分离；满额时回收已过期/注销记录 | 旧文件迁移、错误旧密钥不写入迁移文件、JWT 轮换、0600 权限和容量测试 |
| F19 | 注册 WebSocket、SOCKS、TUN 会话；退出时取消、关闭并等待转发协程 | 活跃 relay/SOCKS 关闭测试、最终流量统计；正常终止测试进程 |
| F20 | TLS 页面默认 `untested`，实际结果显示 `passed`/`failed` 和时间；删除无行为编辑按钮 | 内置浏览器创建临时指纹，真实访问 TLS BrowserLeaks，HTTP 200 后状态更新 |

## 冗余与职责拆分

删除无调用的 `pickEgressChoice`、`AnyEgressGroup`、`ProxySettingsConfigFromSettings`、`isUnspecifiedSOCKSAddress`、`WrapTransport`、Datagram 多余循环，以及前端模拟节点/指纹/流量数据和无调用的 API 包装。`pkg/relay.Open` 是对外协议入口，继续保留。

按职责拆分 `admin/api.go`、`pool/manager.go` 和 `server/server.go`，将管理端认证、节点、订阅、系统、TLS 探测，以及出口选择、配置和会话管理分开。节点启停配置和 HTTP transport 默认值改为共用实现。HTTP/2 帧交换与 TLS/指纹首帧配置分开。

前端改用 ES 模块与本地 React/esbuild 构建，移除 `window` 组件注册、CDN React 和运行时 Babel。ESLint 检查未定义、未使用变量及 JSX 引用；锁文件用于 `npm ci`。浏览器回归发现的 System `Toggle` 导入遗漏已修正。

## 验证结果

| 检查 | 结果 |
| --- | --- |
| `go test -race -tags with_utls ./... -count=1 -timeout 180s` | 通过；最后修改的池、管理及入口模块另行通过竞争测试 |
| `go vet -tags with_utls ./...` | 通过 |
| `staticcheck v0.8.1 -tags with_utls ./...` | 通过 |
| Linux AMD64 / `with_utls` / Go 1.26.8 govulncheck | 可达符号 0，导入包 0；模块级仅剩未使用的 `x/crypto/openpgp` 无修复版本告警 GO-2026-5932 |
| `npm run build --prefix web` | ESLint 与 esbuild 通过，React 页面打包约 298 KiB |
| `npm audit`（安装依赖时） | 0 项漏洞 |
| `GOOS=linux GOARCH=amd64 go build -tags with_utls ./cmd/gateway` | 通过 |
| 内置浏览器 | Overview、Egress、Subscriptions、Templates、SOCKS、TLS、Traffic、System 页面加载；指纹创建及真实 HTTPS 探测通过，截图检查弹窗布局 |

CI 的 Go 检查显式覆盖 `cmd`、`internal`、`pkg`，避免扫描前端 npm 依赖附带的 Go 示例。验证使用临时配置、测试凭据与回环服务；TLS 页面探测使用公开 HTTPS 接口。没有部署运行环境变更。Linux TUN 的实际路由、DNS、IPv6 和重启恢复仍需目标 Linux 主机验证。本机 Docker 版本接口返回 500，本轮未完成完整镜像构建；前端构建和 Linux 二进制交叉编译已通过。

## 升级说明

1. 旧访问密钥存储第一次升级时保留原 `admin.jwt_secret`，启动会验证旧密文并生成 `configs/.runtime/access-keys.json.key`。这次启动成功后可以轮换 JWT 签名密钥。
2. 备份、恢复须同时包含 `access-keys.json` 和 `access-keys.json.key`，后者权限为 `0600`。不要在首次迁移前替换旧 JWT 签名密钥。
3. 使用反向代理进行 Admin 登录限速时，在 `admin.trusted_proxies` 填写实际受信代理的 CIDR，并由代理覆盖客户端提交的转发头。默认空列表只使用直接连接 IP。
4. 空密码本地管理模式通过 localhost 或回环 IP 访问；订阅 `update_interval` 必须为正数，留空表示手动刷新。
5. TLS 测试状态属于当前页面会话。重新进入页面或修改配置后显示未测试，需重新执行探测。

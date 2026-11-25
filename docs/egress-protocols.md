# 出口协议与订阅格式

## 出口协议

静态节点和订阅节点当前支持：

- `direct`
- `socks5`
- `http_proxy` / `http`
- `ss` / `shadowsocks`
- `vmess`
- `vless`
- `trojan`
- `hysteria2` / `hy2`
- `anytls`
- `tuic`

`vmess` / `vless` / `trojan` 支持常见 V2Ray 传输参数：TCP、WebSocket、gRPC、HTTP/H2、HTTPUpgrade、QUIC。`vless` 支持 TLS、Reality 和 `xhttp` / `splithttp` 的 `packet-up`、`stream-up`、`stream-one`、`auto` 模式；`auto` 使用 Xray 当前规则，默认选择 `packet-up`，Reality 选择 `stream-one`。`hysteria2` 支持 Shadowrocket 常见的 `mport=16001-17000` 端口跳跃写法；`anytls` / `tuic` 使用 sing-box outbound 并默认启用 TLS。Clash YAML 的 `fingerprint` 会作为证书 SHA-256 pinning 指纹保留，不再当成 uTLS 指纹传给 sing-box；订阅中的未知 uTLS 指纹值会被忽略，避免正常节点被跳过。当前 sing-box 版本不原生支持 `xhttp` / `splithttp`，`vless+xhttp` 由 Chijie 的专用拨号器接入；其他协议的 `xhttp` 节点会被跳过。订阅中的 XHTTP `downloadSettings` 字段会被保留，但当前拨号器继续使用主节点地址承载下载，以兼容不要求独立下载地址的节点。Reality 和订阅中的 `fp/client-fingerprint` 需要使用 `-tags with_utls` 构建，`./build.sh` 已默认启用。

## 订阅格式

订阅导入支持 Clash YAML、Base64 URI 列表、未 Base64 包装的纯 URI 列表。URI 列表支持 `ss`、`vmess`、`vless`、`trojan`、`hysteria2` / `hy2`、`anytls` 和 `tuic`。Shadowsocks URI 支持 `ss://userinfo@host:port?plugin=...` 和 SIP002 常见的 `ss://userinfo@host:port/?plugin=...` 写法；`simple-obfs` 插件名会规范化为 sing-box 可识别的 `obfs-local`。订阅拉取使用 `clash-verge/v2.0.0` User-Agent，以兼容会按客户端 UA 返回不同节点集合的订阅服务。单个订阅池可填写多个订阅地址，使用换行、英文逗号或 `|` 分隔；部分订阅地址失败时，已成功解析的节点仍会进入该池。订阅地址必须使用 `http` / `https`；默认只允许公网目标，池级 `allow_private_host: true` 可允许私网、回环和保留地址。响应体超过 4 MB 时会拒绝解析。

## 订阅刷新与缓存

订阅池的 `update_interval` 支持 `30m`、`12h`、`3d`，必须为正数；留空表示只手动刷新。通过管理 API 新增订阅会立即注册自动刷新。每次成功拉取都会把解析后的节点原子写入配置目录下的 `.runtime/subscriptions.json`；自动刷新、配置重载或容器重启后的首次拉取失败时，系统保留或恢复上一次成功节点，并继续展示本次失败状态。Admin Subscriptions 页面以灰色时间表示成功、红色时间表示失败，详细语义见 [subscription-routing.md](subscription-routing.md)。

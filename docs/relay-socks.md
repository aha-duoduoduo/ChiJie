# 通用 TCP/UDP 中继

更新：2026-09-30

`/proxy` 继续处理单次 HTTP 请求；`/relay/v1` 承载原始 TCP 流或固定目标的 UDP 数据报。需要浏览器流量、Bot 或其他程序流量时，客户端使用 `pkg/relay` 原生接入，或者通过同一镜像提供的 SOCKS5 入口接入。服务之间的传输使用 WSS。

## WSS 协议

客户端对 `wss://<网关>/relay/v1` 发起 WebSocket Upgrade，握手携带 `Authorization: Bearer <proxy_token>`。每个 WebSocket 对应一个 TCP 连接或一个固定目标的 UDP 会话。关闭 WebSocket 即关闭会话。

首个客户端消息为 JSON 文本帧：

```json
{"version":1,"network":"tcp","address":"example.com:443","egress":{"region":"US","strategy":"least-latency"},"affinity_key":"service-a","affinity_ttl":"1h"}
```

`network` 为 `tcp` 或 `udp`。服务端先返回 `{"status":"connected","pool":"...","node":"..."}`；失败时返回 `{"status":"error","error":"..."}` 并关闭连接。连接成功后的二进制帧：TCP 按顺序组成字节流，UDP 一帧对应一条数据报，协议大小上限 65507 字节，实际可发送大小还受出口系统及网络 MTU 限制。每条 WebSocket 消息上限 128 KiB，`Stream.Write` 自动分段较大的 TCP 写入。客户端不需要实现帧处理时可直接使用 `pkg/relay.DialTCP` 或 `pkg/relay.DialUDP`。

服务端每 25 秒发送 WebSocket Ping，客户端需要正常处理控制帧；`pkg/relay` 内的连接读取方法会调用 WebSocket 读循环并自动回复 Pong。

`egress` 常用写法：

| 目标 | 参数 |
| --- | --- |
| 当前网关直连出口 | `{"direct":true}` |
| 指定地区与策略 | `{"region":"US","strategy":"least-latency"}` |
| 任意可用地区 | `{"any":true,"strategy":"random"}` |
| 固定节点 | `{"pool":"pool-name","node":"node-name"}` |
| 指定远端网关直连出口 | `{"gateway":"gateway-pool","direct":true}` |
| 指定远端网关地区出口 | `{"gateway":"gateway-pool","region":"JP"}` |

`premium`、`residential` 和 `max_latency_ms` 沿用现有出口选择语义。`affinity_key` 与 `affinity_ttl` 让同一调用方的新连接在有效期内优先使用上次选中的节点；连接建立后不会中途切换节点。粘性键最长 256 字节，服务端最多缓存 10,000 条记录，满额时淘汰最久未使用记录。远端网关使用已有的 `template_type: chijie` 模板池，转发时递增跳数，最多 3 跳；地区节点不可用时按现有模板兜底规则选择远端网关。远端直连使用远端机器的出口 IP。

## SOCKS5 入口

Admin 的 **SOCKS accounts** 页面创建账号，每个账号保存出口、地区、策略和粘性时间。用户名选定策略，密码用于认证；一个监听端口同时提供 TCP CONNECT 与 UDP ASSOCIATE。密码只在创建或修改时返回，服务端仅保存 bcrypt 哈希。没有账号或账号被停用时，SOCKS 登录会失败。账号记录位于 `configs/.runtime/socks-accounts.json`。

默认 Docker Compose 将容器的 TCP/UDP `1080` 映射到宿主机 `127.0.0.1:11080`。需要远端客户端连接时，设置 `.env` 的 `CHIJIE_SOCKS_HOST` 和防火墙规则，并在 `socks.advertise_address` 填写客户端实际使用的 `host:port`。公网 SOCKS5 入口本身是明文协议；在客户端机器部署同一镜像作为本地转接器，设置 `socks.upstream_url` 和 `socks.upstream_token`，客户端到本地转接器使用 SOCKS5，转接器到远端网关使用 WSS。

生成的连接地址使用 `socks5h://`，支持该写法的客户端会将域名交给 SOCKS 服务端解析。需要客户端本地解析域名时可将地址前缀改为 `socks5://`；两者的 SOCKS5 线缆协议相同。

SOCKS UDP ASSOCIATE 的 TCP 控制连接必须保持打开。客户端需要发送 UDP 数据报到回复的地址及同一数值端口。一个源 IP 同时只能有一个未声明 UDP 源端口的会话；客户端声明各自的 UDP 源端口可并行使用。

## Linux 整机流量入口

同一镜像可选启用 `tun.enabled`，将宿主机的 TCP/UDP 流量送到远端 WSS 网关。使用独立的 `docker-compose.tun.yml`，它采用 host 网络、`/dev/net/tun` 和 `NET_ADMIN`；`socks.enabled` 可设为 `false`。示例配置仅用于展示字段，令牌和实际地址保存在部署环境的 `configs/gateway.yaml`，不提交仓库：

```yaml
server:
  listen: "127.0.0.1:8080"
admin:
  listen: "127.0.0.1:9090"
  jwt_secret: "" # 启动前填写独立随机值
socks:
  enabled: false
  upstream_url: "wss://relay.example.com/relay/v1"
  upstream_token: "<proxy_token>"
tun:
  enabled: true
  egress:
    direct: true
  bypass_cidrs: []
```

启动时先解析远端 WSS 主机名，再将所选上游 IP 排除出 TUN 路由，并固定 WSS 拨号地址，避免回环。`bypass_cidrs` 可额外保留不经过 TUN 的目标网段。TUN 模式处理 TCP/UDP；ICMP 和其他 IP 协议不在本版中继范围内。UDP 运行在 WSS 的 TCP 连接上，丢包时可能出现队头阻塞。TUN 模式只支持 Linux，需在实际目标主机验证路由、DNS 和重启恢复。

Linux 内核关闭 IPv6 的主机可设置 `tun.ipv6_enabled: false`。默认启用 IPv4 和 IPv6 捕获。

## 访问密钥

System 页面可以生成带有效期或永不过期的访问密钥，查看、复制和注销已保存的密钥。新密钥以 `chijie_pk_v1_` 开头，服务端用哈希校验、AES-GCM 加密保存可复制的原文，文件位于 `configs/.runtime/access-keys.json`，权限为 `0600`。加密材料保存在同目录的 `access-keys.json.key`，权限为 `0600`，备份和恢复必须同时包含这两个文件。新建存储使用独立随机密钥，之后轮换 `admin.jwt_secret` 不影响已有访问密钥。

升级旧存储时，第一次启动必须保留原 `admin.jwt_secret`，程序校验旧密文后自动生成 `.key` 文件；完成这次启动后再轮换 JWT 密钥。达到 1000 条记录时，新建操作会回收最早的已注销或过期记录，有效密钥仍受 1000 条上限限制。

旧版签发的无状态 JWT 仍按原有签名与过期时间验证。旧 JWT 未保存原文，无法列出或单独注销；它们到期后自然失效。新密钥可以单独注销，不影响其他密钥。

WSS 是标准 TLS 上的 WebSocket 升级，可以从 TLS/HTTP 行为识别为 WebSocket；本协议没有 SOCKS 握手特征，但不能保证不被网络设备检测或阻断。

## 统计与退出

relay、SOCKS 和 TUN 共用会话计数与流量记录，`kind` 分别为 `relay`、`socks`、`tun`，在连接结束时保存请求与响应字节数。服务退出会取消打开中的连接、关闭已建立会话并等待转发协程结束。节点池重载停止创建旧出口连接，已建立连接保留至结束后再释放底层资源。

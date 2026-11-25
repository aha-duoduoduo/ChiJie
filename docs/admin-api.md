# Admin API

默认监听 `127.0.0.1:9090`，由 `gateway.yaml` 的 `admin.listen` 配置。

## 鉴权配置

```yaml
admin:
  listen: "127.0.0.1:9090"
  password: "your_password"
  jwt_secret: "replace-with-random-32-byte-secret"
  jwt_expire: "24h"
```

登录后获取 JWT token，后续请求携带 `Authorization: Bearer <token>` Header。`password` 为空时不启用 Admin 鉴权。

Proxy API 调用使用后台生成的访问密钥，不能访问 Admin API。新密钥保存在 `configs/.runtime/access-keys.json`，可单独注销；独立加密密钥保存在 `access-keys.json.key`，应与数据文件一起备份。旧存储首次升级须保留原 `admin.jwt_secret`，自动迁移成功后可轮换 JWT 签名密钥。旧版 Proxy JWT 在自身过期前继续有效。

## API 端点

### 认证

| 方法 | 端点 | 说明 |
|------|------|------|
| POST | /api/auth/login | 密码登录，返回 JWT token |
| GET / POST | /api/auth/proxy-token | 查看或生成只用于代理接口的访问密钥 |
| DELETE | /api/auth/proxy-token/{id} | 注销指定访问密钥 |

### 节点管理

| 方法 | 端点 | 说明 |
|------|------|------|
| GET | /api/nodes | 查看节点池、节点状态和地区组状态 |
| POST | /api/nodes | 添加新节点池 |
| PUT | /api/nodes/pool | 更新节点池配置 |
| DELETE | /api/nodes/pool?name=xxx | 删除节点池配置 |
| PUT | /api/nodes/node | 更新 static 节点池中的单个节点 |
| DELETE | /api/nodes/node?pool=xxx&node=xxx | 删除 static 节点池中的单个节点 |
| PUT | /api/nodes/subscription/node | 更新 subscription 节点地区修正、别名和标签 |
| POST | /api/nodes/refresh?pool=xxx | 手动刷新指定订阅池 |
| POST | /api/nodes/test | 测试节点连通性，返回出口 IP、国家码和 IP 类型信息 |
| POST | /api/nodes/template/test | 按模板池、地区和可选测试 URL 测试连通性；普通代理模板生成临时节点，Chijie 模板请求远端 `/proxy` |
| POST | /api/nodes/enabled | 启用或禁用节点，并持久化到 `nodes.yaml` |

### 流量日志

| 方法 | 端点 | 说明 |
|------|------|------|
| GET | /api/traffic?limit=100 | 查看请求记录、合并展示记录、分钟级流量序列和聚合指标；内存最多保留最近 1000 条 |
| POST | /api/traffic/grouping-rules | 保存失败 URL 规范化规则，并立即用于 Traffic 合并展示 |
| POST | /api/traffic/success-folding-rules | 保存 200 成功请求的 Host + Path 折叠规则 |
| GET / PUT | /api/traffic/settings | 查看或更新 Traffic 日志持久化与保留天数 |

Traffic 原始 trace 按本地日期写入配置目录下的 `.runtime/traffic/traffic-YYYY-MM-DD.jsonl`，默认保留 7 天，可通过 Admin System 页面的 Logging 设置或 `traffic.persistence.retention_days` 设置为 1–3650 天。文件按天自然轮换，启动时加载最近 1000 条到内存；Docker 默认挂载整个 `/config`，因此容器重启或重建不会清空这些记录。

Traffic 指标使用有效请求口径：普通成功请求逐条计入；失败请求按 `kind + url/target + egress_group` 合并计入，避免同一个目标故障被调用方重复重试后放大错误率；命中 `success_folding` 的 200 请求保留在日志中并折叠为一行，但不计入有效请求、成功率、延迟和分钟序列。原始请求数仍通过 `raw_requests` / `raw_failures` 返回给 Admin 页面展示。

失败请求的 URL 可按规则先做规范化再参与合并。Admin 请求详情里的 `Ignore URL params` 可从真实错误 URL 生成规则：Host 和 Path 每一段可点击切换为 `*`，Query 参数可勾选加入 `drop_keys`。规则会保存到 `gateway.yaml`，完整说明见 [traffic-url-grouping.md](traffic-url-grouping.md)：

```yaml
traffic:
  persistence:
    enabled: true
    retention_days: 7
  failure_grouping:
    enabled: true
    url_normalization:
      enabled: true
      rules:
        - name: "hls-signed-query"
          match:
            host_pattern: "*.cdn.example.com"
            path_pattern: "/media/*/segments/*/playlist.m3u8"
          query:
            drop_keys:
              - vendtime
              - vhash
            sort: true
  success_folding:
    enabled: true
    rules:
      - name: "frequent-search"
        match:
          host_pattern: "api.example.com"
          path_pattern: "/v1/search"
```

### 指纹管理

| 方法 | 端点 | 说明 |
|------|------|------|
| GET | /api/fingerprints | 查看所有 TLS 指纹 |
| POST | /api/fingerprints | 添加新指纹，支持 raw JA3/JA4/Akamai 和检测 JSON 导入 |
| DELETE | /api/fingerprints/:name | 删除指定指纹 |
| POST | /api/fingerprints/test | 对 HTTPS 目标发起真实 TLS/HTTP2 指纹测试 |

### 系统管理

| 方法 | 端点 | 说明 |
|------|------|------|
| POST | /api/reload | 热重载节点池和 TLS 指纹配置 |
| GET | /api/stats | 基础统计、节点池数量、指纹数量和流量指标 |
| PUT | /api/system/logging | 修改当前日志级别并写入 `gateway.yaml` |
| GET / PUT | /api/system/health-check | 查看或修改全局健康检查默认参数，并写入 `gateway.yaml` |
| GET / PUT | /api/system/proxy | 查看或修改 `/proxy` 响应头等待超时、完整请求总超时、重试与模板兜底设置，并写入 `gateway.yaml` |
| GET | /api/config/export | 导出当前 YAML 配置快照 |

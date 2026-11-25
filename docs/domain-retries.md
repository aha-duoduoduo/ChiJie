# 按域名重试 HTTP 请求

Traffic → 点击请求 → **Domain retry**，自动填写当前域名；已有规则显示 **Edit domain retry**。设置重试次数、原节点或其他节点、需要重试的状态码，然后保存。System → **Proxy settings → Domain retries** 显示并管理全部规则。保存后立即生效，写入 `gateway.yaml`，重启后保留。

Overview 的近期请求详情也提供相同的域名重试入口。

新安装和升级默认没有域名规则。规则只作用于 `POST /proxy` 执行的目标 **GET / HEAD** 请求，不作用于 SOCKS、WSS 中继和隧道。

## 规则行为

- **Domain**：精确匹配目标 hostname；不包含协议、端口、路径或通配符。域名大小写、末尾圆点和国际化域名会归一化。跨域重定向后的 HTTP 错误不触发原域名规则。
- **Max retries**：首次请求之后的额外次数，范围 0–20。连接错误与选中的 HTTP 错误共用此预算，覆盖该域名的全局 `max_attempts`；最多执行 `1 + max_retries` 次。
- **Use another node**：沿用原地区、家宽、高端与其他筛选条件和候选顺序；每个候选节点最多使用一次，没有候选就停止。模板是否参与仍遵循原模板兜底设置。直连没有其他节点可切换。
- **Use the same node**：在最初选中的出口重试。
- **Retry HTTP statuses**：可选择 408、425、429、500、502、503、504。默认勾选 429、500、502、503、504；404、401、403 等不允许加入。
- 换节点时立即重试，不等待原节点的 `Retry-After`。原节点重试使用 250 ms 至 2 s 的指数退避，同样忽略 `Retry-After`。等待和所有尝试共用 `proxy.total_timeout`，不足以等待时返回当前响应。
- 重试耗尽后返回最后一次结果。HTTP 错误保留状态码、响应体和 `Retry-After`；最后一次是连接错误时返回网关错误。429 / 5xx 不会将节点标记为全局离线。

响应头 `X-Chijie-Attempts` 表示本次网关执行的尝试次数，`X-Chijie-Retry-Outcome` 表示重试结果。Traffic 详情显示 **Attempts**、**Retry result** 和最终出口，区分重试成功、次数用尽、候选耗尽、总超时、请求取消、不适用的状态码或没有匹配规则。历史记录缺失的次数显示 Not recorded；历史记录无法恢复当时的停止原因。表格的 Count 表示合并的请求记录数量，不是单次请求的尝试次数。

## 配置与 API

以下仅为手动配置示例，不会自动启用：

```yaml
proxy:
  domain_retries:
    - domain: api.example.com
      enabled: true
      max_retries: 3
      retry_other_nodes: true
      status_codes: [429, 500, 502, 503, 504]
```

`GET /api/system/proxy` 返回 `domain_retries`；`PUT /api/system/proxy` 保存整个代理设置。Traffic 使用 `PUT /api/system/proxy/domain-retries`，请求体为单条规则，按域名新增或替换，保留其他规则及代理设置。两种写入均校验后原子保存，并使用相同的管理员认证。最多支持 100 条规则；重复域名、非法状态码或越界次数会返回 400，不修改原配置。

## 验证

自动化测试覆盖真实本地 HTTP 响应的节点切换、同节点重试、预算、候选耗尽、域名和方法边界、Retry-After、取消及配置持久化。界面集成使用本地运行的网关和两个受控 HTTP 代理节点，验证 Traffic 创建、System 编辑及 429 → 200、404 原样返回；这些测试不依赖目标网站即时触发限流。

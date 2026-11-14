# 架构与请求链路

[返回 README](../README.md)

## 1. 两层网关

| 层 | 组件 | 解决的问题 |
|---|---|---|
| 南北向 | Edge Gateway | 外部流量入口：认证、RBAC、限流、超时、重试、可观测性、看板 |
| 东西向 | Control Plane + Sidecar | 服务间流量：服务发现、mTLS、金丝雀、故障注入、重试、熔断 |

Edge 自己也是网格客户端：它从控制面拿到服务实例和证书，直接以 `edge-gateway` 身份 mTLS 调用目标 sidecar，不需要额外的 ingress sidecar。

## 2. 进程与端口

| 进程 | 监听 | 说明 |
|---|---|---|
| Edge Gateway | `:8080` | 对外入口 |
| Control Plane | `:18000` | 注册中心、配置、CA、面板 |
| user-service v1 | app `:9101` | 应用 |
| user-service v1 sidecar | inbound `:19101`，outbound `:15001`，admin `:19201` | 服务旁代理 |
| user-service v2 | app `:9102` | 金丝雀版本 |
| user-service v2 sidecar | inbound `:19102`，outbound `:15002`，admin `:19202` | 服务旁代理 |
| order-service | app `:9103` | 应用 |
| order-service sidecar | inbound `:19103`，outbound `:15003`，admin `:19203` | 服务旁代理 |

## 3. 南北向请求链路

以 `GET /api/users/1` 为例：

```text
client
  │  Authorization: Bearer <JWT>
  ▼
Edge Gateway
  1. request ID / CORS / 访问日志 / 指标
  2. 路由匹配：/api/users/ → route users
  3. JWT 校验 + RBAC（admin/viewer）
  4. 令牌桶限流（route + user/IP）
  5. 剥离 /api → /users/1
  6. 外层 timeout（route.upstream.timeout）
  7. mesh router：
       - 匹配 user-service 的 VirtualService
       - 选择子集（默认 90/10，或 x-canary: true → v2）
       - 从快照过滤 HEALTHY 实例
       - 负载均衡（least_request）
       - mTLS 发往目标 sidecar inbound
  ▼
user-service sidecar inbound
  8. 校验客户端证书链与 SPIFFE 身份
  9. 写入 X-Mesh-Source / X-Forwarded-Client-Cert
 10. 反向代理到 127.0.0.1:9101
  ▼
user-service app
```

响应沿原路返回；Edge 记录状态码、耗时、route、user、upstream。

## 4. 东西向请求链路

以 `GET /orders/1` 为例：

```text
order-service app
  │  mesh sdk: http://user-service/users/1
  │  Transport.Proxy = 127.0.0.1:15003
  ▼
order-service sidecar outbound
  1. 从绝对 URL 中取目标服务 user-service
  2. 匹配 VirtualService（canary / faulty / default）
  3. 故障注入（如有）
  4. 选择子集权重，过滤 HEALTHY 实例
  5. 负载均衡 + 重试（每次重试重新选实例）
  6. mTLS 发往 user-service sidecar inbound
  ▼
user-service sidecar inbound → user-service app
```

应用代码不需要知道实例地址、证书或路由规则，只写服务名。

## 5. 配置分发

控制面把以下内容打包成一个 `Snapshot`：

- `Services`：服务实例（地址、标签、健康状态）；
- `VirtualServices`：路由规则；
- `DestinationRules`：子集、LB、连接池、离群摘除、mTLS 模式；
- `TrustBundlePEM`：根证书。

分发流程：

1. 数据面启动时 `GET /v1/config` 拿完整快照；
2. 连接 `GET /v1/watch?version=N`（SSE）；
3. 控制面在注册表或配置变化时递增 `Version`，推送新快照；
4. 客户端断线后先重新拉一次快照，再带新版本重连，避免漏事件；
5. 15 秒心跳注释保持连接。

这相当于一个“简化版 xDS”：不是 gRPC 增量协议，而是版本化的全量快照。

## 6. mTLS 与身份

- 控制面根 CA 使用 ECDSA P-256，持久化在 `.run/data/ca/`。
- sidecar 启动时向控制面申请叶子证书，TTL 默认 10 分钟。
- 叶子证书同时包含：
  - `DNS SAN = 服务名`，用于标准 TLS 主机名校验；
  - `URI SAN = spiffe://cluster.local/ns/<ns>/sa/<service>`，用于身份校验。
- 出站：标准 `RootCAs + ServerName` 校验链和主机名，`VerifyConnection` 再核对目标 SPIFFE 身份。
- 入站：`tls.RequestClientCert`，握手后手动校验证书链和 URI SAN。
- `PERMISSIVE` 允许没有客户端证书（身份记为 `anonymous`）；`STRICT` 直接拒绝。
- 证书在剩余 1/3 TTL 时自动轮换。

## 7. 弹性和流量治理

| 能力 | 位置 | 说明 |
|---|---|---|
| 主动健康检查 | 控制面（网格）/ Edge（静态上游） | 连续失败标记 UNHEALTHY，恢复后重新加入 |
| 负载均衡 | `internal/upstream` | round_robin / random / least_request |
| 重试 | `internal/upstream` + mesh router | 每次重试重新选实例，支持 5xx / reset / connect |
| 超时 | Edge 路由 / VirtualService | 外层 timeout + 每次尝试 timeout |
| 离群摘除 | `internal/upstream.Cluster` | 连续 5xx 达到阈值后摘除一段时间 |
| 连接池 | DestinationRule | 最大连接数、最大排队请求数 |
| 金丝雀 | VirtualService | 子集权重 + header 精确匹配 |
| 故障注入 | VirtualService | abort / delay + 百分比 |
| 限流 | Edge | 按路由 + 用户/IP 的令牌桶 |

## 8. 可观测性

- **Prometheus 文本**：`/metrics`，手写 registry，不依赖 client_golang。
- **JSON 快照**：Edge `/api/stats` 给看板用，包含最近请求、P95、RPS、上游健康。
- **SSE 日志**：`logbuf` 环形缓冲 + `/api/logs/stream`，看板实时显示。
- **请求 ID**：`X-Request-ID` 从 Edge 生成/透传，经过 mesh 到应用。
- **身份头**：sidecar inbound 用校验证书得到的身份覆盖 `X-Mesh-Source`，防止伪造。

## 9. 生命周期

所有进程都使用 `signal.NotifyContext` + `http.Server.Shutdown`：

- Edge：停止配置轮询、健康检查、SSE 客户端；
- Sidecar：先向控制面摘除实例，等待 200ms，再关闭入站/出站/管理监听；
- Control Plane：停止健康检查、TTL 清理和 watch 客户端。

Windows 下 devstack 无法向子进程发送 Ctrl+C，会用 `Kill`；单独运行二进制时仍可验证优雅退出。

## 10. 与 Istio 的差异

| 方面 | 本实现 | Istio |
|---|---|---|
| 注册中心 | 进程内注册表 + 心跳 | Kubernetes Service/Endpoint |
| 数据面 | Go sidecar | Envoy |
| 配置协议 | SSE 全量快照 | gRPC xDS（ADS） |
| 流量拦截 | 显式 HTTP forward proxy | iptables/ebpf 透明拦截 |
| 配置资源 | VirtualService / DestinationRule 子集 | 完整 Istio CRD |
| 证书分发 | 明文 HTTP 接口（localhost 演示） | SDS over mTLS |
| 可观测性 | 手写指标 + SSE 日志 | Prometheus + 分布式追踪 |

这些差异是为了让整个系统能在一台机器上直接运行和阅读，而不是复刻 Istio。

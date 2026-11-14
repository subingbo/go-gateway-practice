# Go Gateway Reference

这是 `go-gateway-practice` 的完整参考版，独立放在 `reference/` 目录，方便和根目录的练习版并排对照。

它包含两层网关：

1. **边缘网关（南北向）**：配置化路由、JWT + refresh/吊销 + RBAC、令牌桶限流、重试、超时、主动健康检查、负载均衡、Prometheus 指标、SSE 日志、优雅退出、内置看板。
2. **集群内网关层（东西向，类 Istio）**：控制面 + 每个服务实例旁的 sidecar。控制面负责服务注册/健康检查、VirtualService / DestinationRule 下发和 SPIFFE 证书签发；sidecar 负责 mTLS、金丝雀权重、故障注入、重试、熔断和指标。

> 目标是把 `docs/limitations.md` 里的 8 个练习方向做成一个可运行、可观察、可对照的完整实现，而不是生产级网关。

## 快速开始

要求：Go 1.26、curl；运行 e2e 脚本还需要 Python 3。Windows 下使用 Git Bash。

```bash
cd reference
go run ./cmd/devstack
```

devstack 会：

- 构建 `controlplane`、`sidecar`、`user-service`、`order-service`、`edge` 五个二进制到 `.run/bin`；
- 拉起控制面、3 个 sidecar、3 个应用实例和边缘网关；
- 为 `admin` / `viewer` 生成随机密码并打印在终端；
- 在 `http://127.0.0.1:8080/` 提供边缘网关看板，在 `http://127.0.0.1:18000/` 提供控制面看板。

也可以提前设置账号密码：

```bash
GATEWAY_ADMIN_PASSWORD=你的密码 GATEWAY_VIEWER_PASSWORD=查看者密码 go run ./cmd/devstack
```

停止：在 devstack 终端按 `Ctrl+C`。脚本或 CI 可以调用：

```bash
curl -X POST http://127.0.0.1:18099/shutdown
```

### 常用参数

| 参数 | 说明 |
|---|---|
| `--chaos` | 让 user-service v2 对所有 `/users/*` 返回 500，用于演示重试与离群摘除 |
| `--run-for 60s` | 运行指定时间后自动停止，适合脚本 |
| `--skip-build` | 复用 `.run/bin` 里已有的二进制 |
| `--no-edge` | 只启动控制面和网格，不启动边缘网关 |
| `--control-addr` | devstack 本地控制端点，默认 `127.0.0.1:18099` |

## 端口与进程

| 组件 | 地址 |
|---|---|
| Edge Gateway | `127.0.0.1:8080` |
| Control Plane | `127.0.0.1:18000` |
| user-service v1 | app `9101`，sidecar inbound `19101`，outbound `15001`，admin `19201` |
| user-service v2 | app `9102`，sidecar inbound `19102`，outbound `15002`，admin `19202` |
| order-service | app `9103`，sidecar inbound `19103`，outbound `15003`，admin `19203` |
| devstack 控制端点 | `127.0.0.1:18099` |

## 架构

```text
                     浏览器 / curl
                          │
                 ┌────────▼─────────┐
                 │ Edge Gateway     │  :8080
                 │ JWT/RBAC/限流     │
                 │ 重试/超时/指标    │
                 │ 看板/SSE 日志     │
                 └────────┬─────────┘
                          │ mesh client（发现 + mTLS + LB + 熔断）
                          ▼
     ┌──────────────────────────────────────────────┐
     │ Control Plane :18000                         │
     │ registry + 主动健康检查                       │
     │ VirtualService / DestinationRule              │
     │ CA（SPIFFE）+ SSE watch + 面板                │
     └───────▲──────────────────────────▲───────────┘
             │ SSE 配置/实例推送          │ SSE
     ┌───────┴────────┐        ┌────────┴─────────┐
     │ user-service v1│        │ order-service    │
     │ app   :9101    │        │ app   :9103      │
     │ sidecar        │        │ sidecar          │
     │ 19101 / 15001  │        │ 19103 / 15003    │
     └───────▲────────┘        └────────┬─────────┘
             │ mTLS                     │ mesh SDK（HTTP forward proxy）
     ┌───────┴────────┐                  │
     │ user-service v2│◀─────────────────┘
     │ app   :9102    │
     │ sidecar        │
     │ 19102 / 15002  │
     └────────────────┘
```

- **南北向**：客户端 → Edge（认证/限流/重试/超时）→ mesh client（发现 + mTLS + LB）→ 目标 sidecar inbound → 应用。
- **东西向**：order-service 用 mesh SDK 把请求发给本机 sidecar outbound → 按 VirtualService 选择子集和实例 → mTLS → user-service sidecar inbound → 应用。

更细的请求链路见 [docs/architecture.md](docs/architecture.md)。

## 目录结构

```text
reference/
├── edge/                       边缘网关（路由、认证、限流、代理、指标、看板）
├── mesh/
│   ├── controlplane/           控制面：注册中心、配置、CA、SSE、面板
│   ├── sidecar/                sidecar：入站 mTLS、出站代理、注册心跳
│   ├── dataplane/              edge 与 sidecar 共用的网格路由/LB/重试/故障注入
│   ├── configclient/           SSE 配置订阅 + 断线补偿
│   ├── certs/                  证书获取与轮换
│   ├── transport/              mTLS client/server 与 SPIFFE 校验
│   └── sdk/                    业务服务访问网格的轻量客户端
├── internal/
│   ├── config/                 边缘网关 YAML 配置与热重载
│   ├── meshapi/                Snapshot / VirtualService / DestinationRule 类型
│   ├── meshconfig/             网格配置多文档解析与校验
│   ├── route/                  最长前缀 + method/header 匹配
│   ├── upstream/               负载均衡、主动健康检查、离群摘除、重试
│   ├── metrics/                手写 Prometheus 指标
│   ├── httpx/                  请求 ID、SSE、状态码记录
│   ├── logbuf/                 slog JSON + 环形缓冲 + SSE 订阅
│   ├── jwtutil/                access/refresh、轮换、jti 吊销
│   └── tlsutil/                SPIFFE URI 与证书链校验
├── apps/                       示例 user-service / order-service
├── cmd/
│   ├── devstack/               一键构建/启动/停止
│   └── loadgen/                并发与 RPS 压测
├── configs/
│   ├── gateway.yaml            边缘网关配置
│   └── mesh/                   VirtualService / DestinationRule
├── docs/                       架构、边缘网关、网格、演示剧本
└── scripts/e2e.sh              端到端验收脚本
```

## 和练习版的对照

| 练习方向（根目录 `docs/limitations.md`） | reference 实现 |
|---|---|
| 1. 上游/超时/认证抽成 YAML | `configs/gateway.yaml` + `internal/config`，按 mtime 热重载 |
| 2. `http.Server.Shutdown` + 信号 | 每个进程的 `main.go`，`signal.NotifyContext` |
| 3. refresh token / 吊销 / RBAC | `internal/jwtutil` + `edge/auth.go`：refresh 轮换、jti denylist、roles |
| 4. 令牌桶 / body limit / 重试 | `edge/ratelimit.go` + `internal/upstream`，429、8 MiB 限制、按次重选实例 |
| 5. Prometheus 指标 | `internal/metrics`，`/metrics` 文本 + `/api/stats` JSON |
| 6. 请求 ID / 结构化日志 / 链路 | `internal/httpx` + `logbuf` + slog JSON，`X-Request-ID` 全链路传播 |
| 7. 主动健康检查 + 负载均衡 | 控制面健康检查 + `internal/upstream`（RR/Random/LeastReq、离群摘除） |
| 8. SSE 推送服务端日志 | `logbuf` + `/api/logs/stream` + 看板日志页 |
| 新增：集群内网关层 | 控制面 + sidecar + mTLS + VirtualService/DestinationRule + 金丝雀/故障注入/熔断 |

和练习版相比，reference 把“代理、认证、指标、看板”都拆成了独立包，并增加了服务注册、配置分发、SPIFFE 身份和数据面策略。练习版仍然更适合按提交历史一步步读；reference 更适合整体对照和查漏补缺。

## 建议阅读顺序

1. `internal/config/config.go`：先看配置模型和默认值。
2. `edge/gateway.go`：看边缘网关的请求处理顺序。
3. `internal/upstream/`：负载均衡、健康检查、重试、离群摘除。
4. `mesh/controlplane/`：注册中心、配置分发、CA。
5. `mesh/dataplane/router.go`：VirtualService 匹配、故障注入、重试、mTLS 发送。
6. `mesh/sidecar/`：入站/出站代理、注册心跳、证书轮换。
7. `mesh/transport/` 和 `internal/tlsutil/`：SPIFFE 身份校验。
8. `edge/dashboard/index.html`：看板如何消费这些接口。

## 验证

```bash
cd reference
go build ./...
go vet ./...
go test ./...
bash scripts/e2e.sh
```

`scripts/e2e.sh` 会启动 devstack，覆盖登录/401/403、南北向代理、金丝雀、东西向调用、504 超时、故障注入、直连 sidecar、mTLS、指标、SSE 日志、refresh 轮换和登出吊销，最后自动清理进程。

## 文档

- [架构与请求链路](docs/architecture.md)
- [边缘网关](docs/edge.md)
- [集群内网关层](docs/mesh.md)
- [演示剧本](docs/demos.md)

## 边界

这是练习/演示项目，不是生产网关：

- 控制面 API 无鉴权，CA 私钥和证书通过 localhost 明文接口分发；
- 演示账号密码由 devstack 生成，生产环境应使用密钥管理系统；
- 不包含 Kubernetes、Docker、Envoy、gRPC xDS；
- 网格入站始终使用 TLS，PERMISSIVE 表示“允许没有客户端证书”，不实现 Istio 的明文/mTLS 同端口嗅探；
- 指标保存在进程内存，重启清零；没有分布式追踪和持久化；
- 只支持 HTTP 上游，没有 WebSocket、HTTP/2 和边缘 TLS。

这些取舍在 [docs/mesh.md](docs/mesh.md) 和 [docs/edge.md](docs/edge.md) 中都有说明。

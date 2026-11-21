# Go Gateway Practice

用 Go 标准库手写一个 API 网关的练习项目：反向代理、JWT 登录认证、日志与超时中间件，以及一个用来观察请求流转的 Vue 3 测试看板。

## 功能

- **反向代理**：基于 `net/http/httputil.ReverseProxy`，按路径把请求转发到用户服务或订单服务
- **JWT 认证**：`POST /auth/login` 校验 bcrypt 密码并签发 HS256 Token，API 统一经过认证中间件
- **中间件链**：请求日志、2 秒超时、JWT 认证、指标采集
- **超时演示**：订单服务的慢接口会触发网关 504，并取消对上游的请求
- **服务发现与负载均衡**：从 Nacos 周期同步实例，支持动态扩缩容和线程安全轮询
- **故障保护**：主动健康检查、滑动窗口被动摘除，以及 GET/HEAD 跨实例重试一次
- **入口限流**：按客户端 IP 维护独立令牌桶，超限返回 429
- **密钥管理**：优先读环境变量，其次读本地文件，最后自动生成 32 字节随机密钥
- **测试看板**：登录、发请求、查看响应、延迟趋势、状态分布和请求历史

## 技术栈

| 层 | 使用 |
|---|---|
| 语言 | Go 1.26 |
| HTTP | `net/http`、`net/http/httputil` |
| 认证 | `github.com/golang-jwt/jwt/v5`、`golang.org/x/crypto/bcrypt` |
| 前端 | Vue 3 + Vite（无 UI 库、无图表库） |

## 目录结构

```text
gateway/          网关主体：路由、代理、认证、中间件、指标
  web/            Vue 3 测试看板源码与构建产物
backend-user/     用户服务，监听 :9090
backend-order/    订单服务，监听 :9091（含 5 秒慢接口）
backend/          最早的练习服务，同样监听 :9090，已被 backend-user 取代
data/             运行时生成的 JWT 密钥
```

`backend/` 与 `backend-user/` 都占用 9090，二选一启动。

## 快速开始

### 1. 一次性设置本地开发环境变量

在 PowerShell 中执行一次以下命令，把配置保存到 Windows 当前用户环境变量：

```powershell
[Environment]::SetEnvironmentVariable("NACOS_ADDR", "127.0.0.1:8848", "User")
[Environment]::SetEnvironmentVariable("ORDER_SERVICE_PORT", "9091", "User")
[Environment]::SetEnvironmentVariable("ORDER_SERVICE_IP", "127.0.0.1", "User")
[Environment]::SetEnvironmentVariable("USER_SERVICE_PORT", "9090", "User")
[Environment]::SetEnvironmentVariable("USER_SERVICE_IP", "127.0.0.1", "User")
[Environment]::SetEnvironmentVariable("GATEWAY_ADMIN_USERNAME", "admin", "User")
[Environment]::SetEnvironmentVariable("GATEWAY_ADMIN_PASSWORD", "admin123", "User")
[Environment]::SetEnvironmentVariable("GATEWAY_USER_USERNAME", "user", "User")
[Environment]::SetEnvironmentVariable("GATEWAY_USER_PASSWORD", "user123", "User")
[Environment]::SetEnvironmentVariable("GATEWAY_RATE_LIMIT_RPS", "20", "User")
[Environment]::SetEnvironmentVariable("GATEWAY_RATE_LIMIT_BURST", "40", "User")
```

设置后关闭并重新打开终端。以上密码仅用于本机学习环境，不要用于生产环境或提交真实凭据。

管理员账号为 `admin`，可以查询、添加和删除上游；普通用户账号为 `user`，只能查询上游。

限流默认按每个客户端 IP 每秒补充 20 个令牌、最多积攒 40 个令牌。超过容量时返回 `429 Too Many Requests`；`/health` 和静态看板资源不参与限流。

`JWT_SECRET` 可选，值是至少 32 字节随机数据的 Base64。不设置时会自动读取或生成 `data/jwt.secret`。

### 2. 启动三个服务

在 **仓库根目录** 分别打开三个终端：

```powershell
go run ./backend-user
go run ./backend-order
go run ./gateway
```

网关监听 `:8080`，用户服务 `:9090`，订单服务 `:9091`。必须在仓库根目录启动网关，`data/jwt.secret` 是相对工作目录的路径。

### 3. 打开看板

浏览器访问 <http://127.0.0.1:8080/>，用刚才设置的用户名和密码登录。

看板开发模式：

```powershell
cd gateway/web
npm install
npm run dev
```

Vite 在 `:5173` 提供页面，并把 `/api`、`/auth`、`/health` 代理到 `:8080`。

生产构建（构建产物会被 `go:embed` 打进网关，构建后需要重启网关）：

```powershell
cd gateway/web
npm run build
cd ../..
go run ./gateway
```

如果忘记构建，网关仍能启动，`/` 会返回一页构建提示。

## 接口一览

| 方法 | 路径 | 认证 | 说明 |
|---|---|---|---|
| POST | `/auth/login` | 否 | 登录并获取 JWT |
| GET | `/health` | 否 | 网关健康检查 |
| GET | `/api/whoami` | 是 | 返回 Context 中的当前用户 |
| GET | `/api/users/{id}` | 是 | 代理到用户服务，剥离 `/api` 前缀 |
| GET | `/api/orders/slow` | 是 | 代理到订单服务，2 秒后返回 504 |
| GET | `/api/routes` | 是 | 返回网关路由与上游配置 |
| GET | `/api/stats` | 是 | 返回进程内请求指标 |

完整示例见 [docs/api.md](docs/api.md)。

## 文档

- [架构与请求流程](docs/architecture.md)
- [接口说明](docs/api.md)
- [前端看板](docs/frontend.md)
- [已知限制与练习方向](docs/limitations.md)

## 已知限制

内存用户、进程内指标、单机限流和有限的幂等请求重试仍属于学习版实现。完整清单见 [docs/limitations.md](docs/limitations.md)。

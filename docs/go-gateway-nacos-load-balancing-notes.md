# Go Gateway：Nacos 服务发现、负载均衡与故障摘除学习笔记

> 这份笔记记录当前项目已经完成的实现、关键代码思路、并发安全设计，以及下一阶段准备实现的内容。
> 面试时要区分“现在已经有的能力”和“计划继续做的能力”，不要把尚未实现的请求重试说成已经完成。

## 1. 项目当前解决了什么问题

这个项目实现了一个 Go 网关，核心链路是：

```text
客户端
  |
  v
Gateway
  |-- JWT 鉴权
  |-- 路由匹配
  |-- 从内存中的实例列表做轮询
  |-- 主动健康检查
  |-- 真实请求失败统计与临时摘除
  |
  +--> user-service  127.0.0.1:9090
  |                 127.0.0.1:9092
  |
  +--> order-service 127.0.0.1:9091

Nacos
  |-- 保存服务实例
  |-- Gateway 定期查询实例列表
  +-- 后端启动时注册、正常停机时注销
```

当前已经完成：

- User Service 和 Order Service 启动时注册临时 Nacos 实例。
- 后端正常停机时主动从 Nacos 注销实例。
- Gateway 启动时从 Nacos获取实例列表。
- Gateway 每 5 秒刷新一次实例列表，而不是每个业务请求都查询 Nacos。
- Nacos 不可用时支持启动兜底地址和运行期“最后一次成功列表”。
- 实例扩容、缩容后，Gateway 可以动态更新内存中的负载均衡列表。
- Gateway 使用线程安全的轮询算法选择可用实例。
- 每个实例有主动健康状态和被动摘除状态。
- 真实请求在 10 秒窗口内连续出现 3 次传输失败时，实例被临时摘除。
- 被摘除实例经过 1 次成功的 `/health` 检查后恢复。
- 摘除和恢复日志由独立 Goroutine 异步输出。

当前还没有完成：

- 分布式限流；当前只有单个 Gateway 进程内的按 IP 令牌桶。
- POST 等非幂等请求的安全重试；当前只自动重试 GET/HEAD。
- 熔断、半开等完整熔断器状态机。

---

## 2. 本地环境变量与启动方式

### 2.1 持久化环境变量

PowerShell 中可以把常用配置写入当前 Windows 用户的持久环境变量：

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
```

设置后要重新打开终端，因为已经打开的 PowerShell 不会自动读取新的用户环境变量。

开发演示密码可以这样保存；真实生产环境不应该把明文密码直接写进仓库，通常应由 Secret 管理系统或部署平台注入。

### 2.2 启动三个程序

```powershell
cd D:\code\go-gateway-practice
go run ./backend-user
```

```powershell
cd D:\code\go-gateway-practice
go run ./backend-order
```

```powershell
cd D:\code\go-gateway-practice
go run ./gateway
```

### 2.3 临时启动第二个 User Service 实例

持久环境变量只能给出默认端口。如果要在同一台机器上再启动一个副本，它必须使用不同端口：

```powershell
cd D:\code\go-gateway-practice
$env:USER_SERVICE_PORT="9092"
go run ./backend-user
```

这里临时覆盖端口是合理的，因为 9092 只属于这个额外进程；如果两个实例都监听 9090，第二个进程会因为端口被占用而启动失败。

---

## 3. 后端服务如何注册到 Nacos

后端启动的大致步骤是：

```text
读取环境变量
  -> 创建 Nacos 客户端
  -> Register(serviceName, ip, port)
  -> 启动 HTTP Server
  -> 等待 SIGINT / SIGTERM
  -> HTTP Server 优雅停机
  -> Deregister(serviceName, ip, port)
  -> 关闭 Nacos 客户端
```

两个服务使用的逻辑服务名分别是：

```text
user-service
order-service
```

Nacos 中的一条实例记录至少由下面的信息区分：

```text
服务名 + 分组 + 集群 + IP + 端口
```

因此下面两台进程可以同时属于 `user-service`：

```text
user-service -> 127.0.0.1:9090
user-service -> 127.0.0.1:9092
```

### 3.1 为什么注册为临时实例

当前注册使用临时实例，也就是 `Ephemeral: true`。

临时实例适合应用进程：

- 进程正常停机时，代码主动调用注销方法。
- 进程崩溃、断电或被强制结束，来不及主动注销时，Nacos 也能根据心跳超时自动清理实例。
- 不会让已经死亡的应用长期留在服务列表中。

### 3.2 `Deregister` 接口为什么看不到具体删除代码

项目中有一个接口：

```go
type serviceRegistry interface {
    Register(serviceName string, ip string, port uint64) error
    Deregister(serviceName string, ip string, port uint64) error
    Close()
}
```

它只描述“具备什么能力”，不包含具体实现。真正的实现位于 Nacos 注册器类型中，最终会调用 Nacos Go SDK 的注销实例 API。

这是一种面向接口编程：

- 业务启动代码只依赖 `serviceRegistry`。
- 生产环境传入真正的 Nacos 实现。
- 单元测试传入假的实现，检查有没有调用注册和注销，不需要真的连接 Nacos。

所以“假连接”只存在于测试替身中；正常运行 `go run` 时连接的是真实 Nacos。

---

## 4. Gateway 如何发现服务

### 4.1 不是每次业务请求都访问 Nacos

Gateway 的处理方式是：

```text
Gateway 启动
  -> 查询 Nacos
  -> 把结果放入内存负载均衡器

后台刷新 Goroutine
  -> 每 5 秒查询一次 Nacos
  -> 更新内存负载均衡器

每次业务请求
  -> 只读取内存列表
  -> 不直接查询 Nacos
```

如果每次业务请求都查询 Nacos，会带来几个问题：

- 请求延迟增加。
- Nacos 压力会随着业务流量增长。
- Nacos 短暂抖动会直接影响所有业务请求。
- Gateway 无法利用本地缓存继续提供服务。

所以服务发现属于控制面，业务转发属于数据面，两者应该解耦。

### 4.2 启动失败和运行期失败采用不同策略

启动时还没有任何成功的 Nacos 结果：

```text
Nacos 查询成功 -> 使用查询到的实例
Nacos 查询失败 -> 使用 FallbackTargets
```

Gateway 已经运行，并且以前成功查询过 Nacos：

```text
本次刷新成功 -> ReplaceTargets 更新实例列表
本次刷新失败 -> 保留最后一次成功列表
```

运行期查询失败时不应该突然切回静态兜底地址，因为兜底地址可能已经过时。保留最后一次成功结果通常更稳定。

后台刷新仍会继续运行，Nacos 恢复后下一轮查询就能重新同步。

### 4.3 Nacos 返回哪些实例

Gateway 查询时要求健康实例。Nacos SDK 还会综合实例的：

- `Healthy`
- `Enabled`
- `Weight`

例如管理员把实例设为 disabled 后，该实例不应该继续进入可用列表。这部分筛选由 Nacos 服务端和 SDK 的选择逻辑共同完成，不是 Gateway 自己遍历所有实例后只判断一个字段。

---

## 5. `proxyRoute` 每个字段的作用

当前路由结构是：

```go
type proxyRoute struct {
    Name            string
    ServiceName     string
    Path            string
    FallbackTargets []string
    Prefix          string
}
```

| 字段 | 含义 |
|---|---|
| `Name` | 管理页面和日志显示的名称 |
| `ServiceName` | 在 Nacos 中查询的逻辑服务名 |
| `Path` | Gateway 对外匹配的路径前缀 |
| `FallbackTargets` | Gateway 启动时 Nacos 不可用所使用的静态兜底地址 |
| `Prefix` | 转发前需要从请求路径删除的前缀 |

`FallbackTargets` 不是保存 Nacos 查询结果的地方。

动态查询结果保存在 `RoundRobinBalancer.targets` 中。这样静态配置与运行期状态不会混在一起：

```text
FallbackTargets              = 配置
RoundRobinBalancer.targets   = 运行时状态
```

---

## 6. 动态扩容、轮询和缩容如何实现

### 6.1 动态扩容

原列表：

```text
127.0.0.1:9090
```

启动 9092 并注册 Nacos 后：

```text
Nacos: [9090, 9092]
  -> Gateway 下一轮刷新
  -> ReplaceTargets([9090, 9092])
  -> 后续请求开始在两台实例之间轮询
```

不需要重启 Gateway。

### 6.2 动态缩容

停止 9092 后：

```text
9092 正常注销，或心跳超时被 Nacos 清理
  -> Nacos: [9090]
  -> Gateway 下一轮刷新
  -> ReplaceTargets([9090])
  -> 后续请求只选择 9090
```

### 6.3 为什么最多可能有约 5 秒的发现延迟

Gateway 当前每 5 秒刷新一次。实例刚刚变化时，Gateway 内存里可能仍是上一版列表，直到下一轮刷新。

这 5 秒内不是直接进入兜底地址，而是继续使用最后一次成功列表。主动健康检查和真实请求失败统计负责缩短坏实例继续接收流量的时间。

---

## 7. `upstream` 是什么

一个 `upstream` 代表 Gateway 内存中的一个后端实例：

```go
type upstream struct {
    target  *url.URL
    healthy atomic.Bool
    checked bool
    passive passiveFailureTracker
}
```

它不仅保存地址，还保存这个实例的运行状态：

- `target`：实例地址。
- `healthy`：主动 `/health` 检查结果。
- `checked`：是否已经完成过主动检查。
- `passive`：真实请求失败窗口和被动摘除状态。

### 7.1 `&upstream{target: target}` 是什么

```go
item := &upstream{target: target}
```

这会创建一个新的 `upstream` 结构体，并返回指向它的指针，类型是：

```go
*upstream
```

### 7.2 从 map 中拿到的也是同一个对象指针

```go
existing := existingByAddress["http://127.0.0.1:9090"]
```

如果 map 类型是：

```go
map[string]*upstream
```

那么 `existing` 的类型就是 `*upstream`。它不是复制出一个全新的实例状态，而是拿到原对象的指针。

---

## 8. `ReplaceTargets` 为什么要复用已有对象

核心逻辑可以概括为：

```go
for _, target := range parsedTargets {
    if existing := existingByAddress[target.String()]; existing != nil {
        nextTargets = append(nextTargets, existing)
        continue
    }

    nextTargets = append(nextTargets, newUpstream(target))
}
```

这里的“不重复创建”不是说列表中永远不可能出现重复字符串，而是：

> 地址没有变化时，复用原来的 `*upstream` 对象，不重新创建一个丢失状态的新对象。

例如刷新前：

```text
9090 -> healthy=true，未摘除
9092 -> healthy=false，已记录失败
```

Nacos 下一次仍返回 `[9090, 9092]`。如果全部重新创建，两台实例的健康状态和失败记录都会被重置。复用对象后，这些运行状态可以保留。

`continue` 的作用是：已经找到旧对象并追加到新列表后，直接进入下一轮循环，不再执行下面的新建代码。

刷新后的行为是：

- 旧地址仍存在：复用旧 `upstream`。
- 出现新地址：创建新 `upstream`。
- 旧地址消失：不放入 `nextTargets`，自然完成缩容。

---

## 9. 轮询选实例的算法

核心计算是：

```go
start := (counter.Add(1) - 1) % uint64(len(targets))

for offset := 0; offset < len(targets); offset++ {
    index := (int(start) + offset) % len(targets)
    candidate := targets[index]

    if !candidate.healthy.Load() {
        continue
    }
    if candidate.passive.isEjected() {
        continue
    }
    return candidate.target
}
```

假设实例列表为：

```text
下标 0 -> A
下标 1 -> B
下标 2 -> C
```

正常情况下，`start` 依次为：

```text
0, 1, 2, 0, 1, 2 ...
```

如果本轮从 B 开始，但 B 不可用：

```text
B -> 不可用，跳过
C -> 可用，返回 C
```

如果从 C 开始，而 C 不可用：

```text
C -> 不可用
A -> 通过取模回到开头，A 可用，返回 A
```

### 9.1 `offset` 会不会一直增长

不会。`offset` 是 `Next()` 方法内部的局部变量：

```go
for offset := 0; offset < len(targets); offset++
```

每次请求调用 `Next()` 时，它都会重新从 0 开始，并且最多检查当前列表长度次。

真正持续增长的是原子计数器 `counter`。它通过取模映射到有效下标。即使 `uint64` 最终溢出，也会按照无符号整数规则回绕，不会导致数组越界。

### 9.2 如果起点是第二台，但第一台正常怎么办

轮询不是每次都优先第一台。起点是第二台且第二台可用，就返回第二台；这是负载均衡的目的。

只有起点实例不可用时，`offset` 才向后扫描其他实例，并通过取模绕回第一台。

### 9.3 所有实例都不可用

`Next()` 最多扫描一整圈，如果没有任何满足条件的实例，就返回 `nil`，Gateway 返回：

```text
503 Service Unavailable
```

---

## 10. 主动健康检查与被动摘除

这两个状态解决的问题不同。

### 10.1 主动健康检查

Gateway 定期访问：

```text
http://实例地址/health
```

当前周期约 5 秒，单次检查超时约 1 秒。

主动检查用于回答：

> Gateway 主动探测时，这台实例是否健康？

结果保存在：

```go
healthy atomic.Bool
```

### 10.2 被动失败统计

真实业务请求可能比下一次健康检查更早发现实例已经失效。例如内存中仍然是：

```text
真实状态：进程已经死亡
Gateway 内存：healthy=true
```

这时反向代理产生连接失败或超时，Gateway 会记录一次传输失败。

当前规则是：

```text
10 秒滑动窗口内累计到 3 次连续传输失败
  -> ejected=true
  -> 暂时不再给该实例分配新流量
  -> 输出一次 WARN 日志
```

实例被摘除后，下一次成功的 `/health` 探测会：

```text
清空失败记录
  -> ejected=false
  -> 恢复流量
  -> 输出一次 INFO 日志
```

当前恢复阈值是 1 次，不是 2 次，因此正常情况下不需要额外等待两个健康检查周期。

### 10.3 最终可选条件

一台实例能被轮询选中，必须同时满足：

```text
主动健康状态 healthy == true
并且
被动摘除状态 ejected == false
```

也就是：

```text
available = healthy && !ejected
```

### 10.4 为什么真实请求失败不直接修改 `healthy`

- `healthy` 表示主动健康检查得到的结论。
- 一次真实请求失败可能是短暂网络抖动，不能立刻判死实例。
- 主动检查和业务请求由不同 Goroutine 并发执行，混写同一个语义状态容易让规则不清晰。
- 分开后，一个字段描述探测结论，一个字段描述流量保护策略。

这不是因为原子变量不能并发修改，而是为了让两个状态的职责清楚。

---

## 11. 哪些错误会进入失败窗口

会记录失败的典型情况：

- TCP 连接被拒绝。
- 后端连接中断。
- 代理请求超时。
- 其他无法获得 HTTP 响应的传输错误。

不会记录为实例失败：

- 客户端主动取消请求。
- 后端正常返回 HTTP 4xx。
- 后端正常返回 HTTP 5xx。

4xx/5xx 说明 Gateway 已经成功连接到后端并收到了完整 HTTP 响应。它可能是业务错误，但不是连接级故障。因此任何收到的 HTTP 响应都会打断当前“连续传输失败”序列。

---

## 12. 滑动窗口队列如何清理超时记录

每台实例的失败时间保存在：

```go
failureTimes []time.Time
```

收到一次新失败时，先计算窗口起点：

```go
cutoff := now.Add(-failureWindow)
```

然后原地过滤旧切片：

```go
kept := failureTimes[:0]

for _, failureTime := range failureTimes {
    if failureTime.Before(cutoff) {
        continue
    }
    kept = append(kept, failureTime)
}

failureTimes = kept
failureTimes = append(failureTimes, now)
```

例如窗口为 10 秒，现在是第 20 秒：

```text
原记录：[5秒, 12秒, 17秒]
cutoff：10秒

5秒  < 10秒 -> 丢弃
12秒 >= 10秒 -> 保留
17秒 >= 10秒 -> 保留
追加当前 20秒

新记录：[12秒, 17秒, 20秒]
```

这就是滑动窗口：窗口会随着当前时间向前移动，不是固定的整点分桶。

### 12.1 如何去掉“多出来”的记录

达到阈值后实例立即进入 `ejected=true`。被摘除期间不再接受新的业务流量，`recordFailure` 也不会继续追加记录。因此当前队列最多只需要容纳阈值数量，也就是 3 条。

恢复成功时会执行：

```go
failureTimes = failureTimes[:0]
```

正常业务请求成功时，如果实例尚未被摘除，也会清空失败序列，因为当前规则统计的是连续传输失败。

### 12.2 超时记录为什么不是后台定时删除

当前采用惰性清理：下一次失败到来时再删除窗口外记录。

优点是：

- 不需要为每台实例额外启动清理定时器。
- 没有新失败时，旧记录即使暂时留在内存里也不会影响选实例。
- 每台实例最多只有很少几条时间记录，内存成本固定且很小。

---

## 13. Slice 的有效长度存在哪里

Go 的 slice 可以从概念上理解为一个小的描述头：

```go
type sliceHeader struct {
    Data *T
    Len  int
    Cap  int
}
```

实际长度由 slice 自己的 `Len` 保存，不是单独放在业务结构体中的另一个字段。

例如：

```go
failureTimes := make([]time.Time, 0, 3)
```

此时：

```text
len = 0
cap = 3
```

执行：

```go
kept := failureTimes[:0]
```

`kept` 与 `failureTimes` 共享同一个底层数组，但是 `kept` 的有效长度是 0。后面每次：

```go
kept = append(kept, failureTime)
```

都会返回一个更新了 `Len` 的 slice 头。

最后：

```go
t.failureTimes = kept
```

就是把包含新 `Len` 的 slice 头保存回结构体字段。后续：

```go
len(t.failureTimes)
```

读到的就是过滤后的有效长度。

这种写法复用原底层数组，避免每次失败都额外分配新切片。

---

## 14. 为什么还需要单独的 `ejected`

不能只通过 `len(failureTimes)` 判断实例当前是否被摘除，原因有三个。

### 原因一：历史证据和当前状态不是一回事

```text
failureTimes = 为什么触发摘除的历史记录
ejected      = 当前是否允许接收流量
```

实例到达阈值后，即使 10 秒过去，也不能仅仅因为记录“过期”就自动恢复。它必须真的通过健康检查，证明自己已经恢复。

### 原因二：高频读取需要更轻量

每次请求都会调用 `Next()`。如果通过 slice 长度判断，必须持有失败跟踪器的互斥锁，才能避免与追加和清空 slice 并发冲突。

现在只需要：

```go
ejected.Load()
```

这是原子读取，不需要在每次选实例时竞争互斥锁。

### 原因三：状态转换更清晰

只有达到阈值的一刻执行：

```text
false -> true，记录一次 WARN
```

只有恢复探测成功的一刻执行：

```text
true -> false，记录一次 INFO
```

这样不会对同一故障重复输出阈值告警。

---

## 15. 线程安全体现在哪里

Gateway 同时存在业务请求、Nacos 刷新、健康检查、日志等多个 Goroutine，所以不同共享数据使用了不同同步手段。

### 15.1 实例列表：`sync.RWMutex`

```go
mu      sync.RWMutex
targets []*upstream
```

- Nacos 刷新替换列表时加写锁。
- 请求读取列表时加读锁，并生成快照。
- 避免读列表和换列表同时发生造成数据竞争。

### 15.2 轮询计数：`atomic.Uint64`

```go
counter atomic.Uint64
```

多个请求可以并发获取不同轮询序号，不需要为一个整数使用大锁。

### 15.3 健康和摘除状态：原子变量

```go
healthy atomic.Bool
ejected atomic.Bool
```

这些字段读取频率高、状态简单，使用原子变量可以避免高频锁竞争。

### 15.4 失败时间队列：`sync.Mutex`

```go
mu           sync.Mutex
failureTimes []time.Time
```

slice 的追加、过滤、清空不是单条原子操作，必须放进同一个临界区，不能只靠原子变量。

### 15.5 异步日志队列：独立互斥锁和条件变量

日志队列可能被业务请求 Goroutine 和健康检查 Goroutine 同时写入，因此有自己的锁。后台日志 Goroutine通过条件变量等待新事件。

---

## 16. `Context` 和 `WaitGroup` 各自做什么

它们不是同一种工具。

### `Context`：发出停止通知

```text
cancel()
  -> ctx.Done() 被关闭
  -> 后台 Goroutine 收到“该退出了”的信号
```

### `WaitGroup`：等待真正退出

```text
启动 Goroutine 前 Add(1)
Goroutine 退出时 Done()
主流程调用 Wait()
```

完整关闭流程是：

```text
先 cancel 通知后台任务停止
  -> 各 Goroutine 收尾并 Done
  -> Wait 等它们全部退出
  -> 再关闭它们依赖的客户端和日志器
```

为什么要等：如果不等健康检查和服务发现 Goroutine 就直接关闭 Nacos 客户端或日志器，后台任务可能仍在访问已经关闭的资源，造成错误、数据竞争或丢日志。

`WaitGroup` 本身不会杀死 Goroutine；如果 Goroutine 没有监听 `ctx.Done()` 或其他退出信号，`Wait()` 可能永远等下去。

---

## 17. 异步告警日志如何工作

状态发生转换时，业务 Goroutine 不直接执行日志 I/O，而是创建事件：

```text
达到失败阈值 -> upstreamEventEjected
健康探测恢复 -> upstreamEventRecovered
```

处理流程：

```text
业务请求 / 健康检查 Goroutine
  -> 把事件追加到内存队列
  -> 唤醒日志 Goroutine
  -> 自己继续工作

日志 Goroutine
  -> 从队列取出事件
  -> log.Printf
```

日志示例：

```text
[WARN] upstream passively ejected target=http://127.0.0.1:9092 failures=3 window=10s
[INFO] upstream recovered target=http://127.0.0.1:9092 successful_probes=1
```

关闭 Gateway 时，日志器禁止新事件入队，唤醒后台 Goroutine，写完已经入队的事件后再退出。

当前队列没有固定容量上限，这是为了保证少量的摘除/恢复状态日志不因队列满而丢失。代价是如果日志输出长时间永久阻塞，队列理论上可能增长；不过这里只记录低频状态转换，不记录每个请求。

---

## 18. 实例刚失效时会发生什么

假设有两台实例：

```text
A = 9090，正常
B = 9092，刚刚失效
```

但 Gateway 还没刷新到最新 Nacos 列表，内存状态暂时是：

```text
A healthy=true
B healthy=true
```

可能发生：

1. 某个请求轮询到 B。
2. 代理连接 B 失败，B 的失败窗口增加一条记录。
3. 如果是 GET/HEAD，并且请求仍有剩余总时间，Gateway 排除 B 后选择 A 重试一次。
4. A 成功时，客户端直接获得 A 的响应；A 也失败时，本次请求返回 502，整体超时则返回 504。
5. 没达到摘除阈值之前，后续新请求仍有可能再次选到 B。
6. 10 秒内达到 3 次失败后，B 被动摘除。
7. 之后的请求会跳过 B，选择 A。
8. Nacos 下一轮刷新也会把 B 从实例列表中删除。

这里有一个必须说明的当前边界：

> 只有 GET/HEAD 会自动换一台实例重试一次。POST 等请求仍然直接返回本次错误，避免重复下单或重复写入。

---

## 19. 兜底地址也失败时怎么办

兜底地址只解决 Gateway 启动时暂时无法查询 Nacos 的问题，不保证地址本身一定可用。

如果兜底列表中：

- 还有其他健康地址：轮询会跳过不可用地址并选择剩余地址。
- 所有地址都不可用：`Next()` 返回 `nil`，Gateway 返回 503。
- 某个刚失效地址还没被识别：可能先出现 502/504，达到阈值或健康检查失败后才会被跳过。

即使使用兜底地址启动，后台 Nacos 刷新仍会继续。Nacos 恢复后，动态实例列表会替换兜底列表。

---

## 20. 当前请求重试如何工作

请求重试不是简单地再次调用 `Next()`。当前实现采用以下边界：

- 只自动重试 GET/HEAD。
- 最多重试 1 次，也就是一次客户端请求最多尝试 2 台实例。
- `NextExcluding` 明确排除第一次失败的地址。
- 两次尝试共享原请求 Context 和总超时时间。
- 只有无法获得 HTTP 响应的传输错误才触发重试。
- 正常收到的 4xx/5xx 不触发连接级重试。
- 请求体存在时，只有可以通过 `GetBody` 安全重建才允许重试。

关键链路是：

```text
第一次 RoundTrip 失败
  -> 把失败计入第一台实例
  -> NextExcluding(第一台)
  -> 克隆请求并替换目标地址
  -> 使用同一个 Context 发起第二次 RoundTrip
  -> 第二次成功则返回响应
  -> 第二次失败则把错误归属到第二台实例并返回 502/504
```

POST 如果以后需要重试，仍应配合幂等键和服务端去重，不能直接照搬 GET 的规则。

限流位于入口处：每个客户端 IP 有独立令牌桶，默认每秒补充 20 个令牌、最多积攒 40 个。`/api/*` 与 `/auth/*` 消耗令牌，超限返回 429；健康检查和静态资源绕过限流。长时间不活跃的 IP 会被惰性清理，避免内存 map 无限增长。

---

## 21. 手动上游管理接口的边界

项目里还有查看、添加、删除内存上游地址的管理接口。这些操作修改的是当前 Gateway 进程内的列表，不会替代 Nacos 成为实例的最终数据源。

如果某条路由由 Nacos 管理，下一轮服务发现刷新可能覆盖手动修改结果。因此当前可以把它理解为学习和调试接口，而不是一套独立、持久化的服务注册中心。

---

## 22. 关键文件索引

| 文件 | 作用 |
|---|---|
| `backend-user/main.go` | User Service、Nacos 注册/注销、优雅停机 |
| `backend-order/main.go` | Order Service、Nacos 注册/注销、优雅停机 |
| `internal/discovery/nacos.go` | Nacos 客户端、注册、注销、查询实例 |
| `gateway/routes.go` | 路由、服务名、兜底地址 |
| `gateway/nacos_targets.go` | 启动查询与后台定时刷新 |
| `gateway/balancer.go` | 实例对象、轮询、健康检查、动态替换列表 |
| `gateway/passive_health.go` | 滑动失败窗口、被动摘除与恢复 |
| `gateway/async_upstream_logger.go` | 摘除/恢复事件异步日志 |
| `gateway/main.go` | 反向代理、成功/失败上报、程序组装与关闭 |
| `gateway/upstreams_http.go` | 内存上游管理接口 |

---

## 23. 验证命令

格式化代码：

```powershell
gofmt -w backend-order/*.go backend-user/*.go gateway/*.go internal/discovery/*.go
```

运行全部测试：

```powershell
go test ./... -count=1
```

使用竞态检测运行 Gateway 测试：

```powershell
go test -race ./gateway -count=1
```

构建全部包：

```powershell
go build ./...
```

静态检查：

```powershell
go vet ./...
```

这一阶段最近一次验证结果是：

```text
go test ./... -count=1          通过
go test -race ./gateway -count=1 通过
go build ./...                  通过
go vet ./...                    通过
```

---

## 24. 面试时可以怎么讲

### 24.1 一分钟项目说明

> 我用 Go 实现了一个支持 JWT 鉴权和反向代理的 API Gateway。后端服务启动时会以临时实例注册到 Nacos，正常停机时主动注销，异常退出则由 Nacos 根据心跳清理。Gateway 定期把实例同步到内存负载均衡器，业务请求不直接访问注册中心。负载均衡使用线程安全的轮询算法，并通过主动探测和 10 秒滑动窗口被动摘除故障实例。对于 GET/HEAD 传输失败，网关会排除原实例并跨实例重试一次；入口使用按客户端 IP 隔离的令牌桶限流，超限返回 429。

### 24.2 简历描述参考

> 基于 Go 实现 API Gateway 服务发现与流量治理：接入 Nacos 临时实例注册、优雅注销和周期性实例同步，支持动态扩缩容；实现并发安全轮询、主动健康检查、滑动窗口被动摘除、GET/HEAD 跨实例有限重试及按客户端 IP 隔离的令牌桶限流。

### 24.3 可能被追问的问题

**为什么业务请求不直接查询 Nacos？**

为了降低延迟和注册中心压力，并在 Nacos 短暂不可用时继续使用本地最后一次成功列表。

**为什么更新列表时复用 `upstream` 指针？**

为了保留相同实例已有的主动健康状态、失败窗口和摘除状态；只有新地址才创建新对象。

**为什么同时有主动健康检查和被动摘除？**

主动检查周期性发现问题，被动统计能利用真实流量更快识别健康检查间隔内的故障，两者互补。

**为什么 4xx/5xx 不算传输失败？**

因为已经成功获得 HTTP 响应，说明网络连接和后端 HTTP 服务可达；它属于业务结果，不属于连接级失败。

**为什么用 `atomic` 和 `Mutex` 两种同步方式？**

简单且高频读取的布尔值和计数器适合原子操作；slice 过滤、追加和清空是复合操作，需要互斥锁保护。

**当前设计还有什么不足？**

当前重试只覆盖 GET/HEAD，且最多一次；限流状态只在单个进程内，部署多副本网关时配额不共享；发现刷新采用定时轮询，仍有秒级延迟；异步事件队列没有容量上限。

---

## 25. 当前进度清单

- [x] Order Service 注册 Nacos 临时实例
- [x] Order Service 优雅停机并注销
- [x] User Service 注册 Nacos 临时实例
- [x] User Service 优雅停机并注销
- [x] 常用端口、Nacos 地址和演示账号持久化为用户环境变量
- [x] Gateway 启动时查询 Nacos
- [x] 启动查询失败时使用静态兜底地址
- [x] 后台每 5 秒刷新实例列表
- [x] 运行期刷新失败时保留最后成功列表
- [x] 动态扩容和缩容
- [x] 线程安全轮询与不可用实例跳过
- [x] 主动 `/health` 健康检查
- [x] 10 秒窗口内 3 次连续传输失败后被动摘除
- [x] 1 次健康探测成功后恢复
- [x] 摘除和恢复事件异步记录日志
- [x] 单元测试、竞态检测、构建与静态检查
- [x] GET/HEAD 传输失败后排除原实例并重试另一实例一次
- [x] 按客户端 IP 的进程内令牌桶限流
- [ ] 根据重试与限流结果继续评估熔断机制

---

## 26. 最重要的整体认识

这个网关目前不是依赖 Nacos 完成每次请求转发，而是：

```text
Nacos 负责告诉 Gateway“有哪些实例”
Gateway 把实例放进自己的内存列表
Gateway 自己完成轮询、健康判断和请求转发
```

Nacos 列表、主动探测和真实请求反馈分别提供三类信息：

```text
Nacos          -> 注册中心认为实例是否存在、是否可选
主动健康检查    -> Gateway 定期探测实例是否健康
真实请求反馈    -> 实际流量是否出现连接失败或超时
```

把三者结合起来，才能同时处理动态扩缩容、注册中心短暂不可用、实例突然宕机以及并发请求下的安全选址。

# Nexus — 微服务基座框架

## 概述

Nexus 是一个面向本地多进程微服务架构的通用基座框架，负责进程编排、服务注册发现、和高性能 IPC 通信。框架与业务逻辑完全解耦，可在不同项目间复用。

## 核心目标

1. **进程管理**：声明式配置，统一管理所有组件的启动/停止/重启/健康检查
2. **服务注册中心**：组件启动后注册能力和地址，其他组件按名称/类型发现
3. **传输抽象层**：统一 API，底层自动选择 UDS（同机）或 TCP（跨机），序列化统一用 msgpack
4. **零拷贝支持**：同机大数据传输可选 memfd 文件描述符共享

## 架构

```
┌──────────────────────────────────────────────┐
│                  nexusd (守护进程)              │
│  ┌──────────┐ ┌──────────┐ ┌──────────────┐  │
│  │ 进程管理  │ │ 注册中心  │ │ 健康检查      │  │
│  └──────────┘ └──────────┘ └──────────────┘  │
│                    │                          │
│         UDS: /run/nexus/registry.sock         │
└──────────────────────────────────────────────┘
        ↕                    ↕
┌──────────┐  ┌──────────┐  ┌──────────┐
│ 组件 A   │  │ 组件 B   │  │ 组件 C   │
│ (Go bin) │  │ (Go bin) │  │ (Python) │
└──────────┘  └──────────┘  └──────────┘
     ↕ UDS msgpack    ↕ TCP msgpack
```

## 组件类型

框架对组件一视同仁，但通过配置支持不同的部署模式：

- **singleton**：单实例，只在本机运行一份（适用于 1-4, 7 类）
- **worker**：可多实例，每个实例接收不同参数/配置（适用于 5, 6 类）
  - worker 可以运行在本机，也可以运行在远端机器

## 目录结构

```
nexus/
├── cmd/
│   └── nexusd/          # 守护进程入口
│       └── main.go
├── pkg/
│   ├── config/          # 配置解析（TOML/YAML）
│   │   ├── config.go
│   │   └── types.go
│   ├── daemon/          # 进程管理 & 生命周期
│   │   ├── daemon.go
│   │   ├── process.go
│   │   └── health.go
│   ├── registry/        # 服务注册中心
│   │   ├── registry.go
│   │   ├── service.go
│   │   └── discovery.go
│   ├── transport/       # 传输抽象层
│   │   ├── transport.go # 统一接口
│   │   ├── uds.go       # Unix Domain Socket 实现
│   │   ├── tcp.go       # TCP 实现
│   │   ├── msgpack.go   # msgpack 编解码
│   │   └── memfd.go     # memfd 零拷贝（Linux only）
│   └── sdk/             # 组件集成 SDK
│       ├── client.go    # Go 组件接入库
│       └── python/      # Python 适配
│           └── nexus_sdk.py
├── nexus.toml           # 示例配置文件
├── DESIGN.md
├── AGENTS.md
└── go.mod
```

## 配置格式

```toml
[daemon]
socket = "/run/nexus/registry.sock"
log_level = "info"
health_interval = "5s"

# 单实例组件
[[service]]
name = "preprocessor"
type = "singleton"
binary = "/opt/app/preprocessor"
args = ["--config", "/etc/app/preproc.toml"]
depends_on = ["gateway"]
health_check = "tcp://localhost:9001/health"
# 注意：depends_on 和 health_check 目前为预留字段，当前版本 daemon 不会执行依赖排序或健康探针检查。

# 多实例 worker
[[service]]
name = "detector"
type = "worker"
binary = "/opt/app/detector"
instances = [
  { id = "detector-vehicle", args = ["--config", "vehicle.toml"] },
  { id = "detector-person", args = ["--config", "person.toml"] },
]
network = "dual"  # 同时监听 UDS 和 TCP，支持远端调用

# Python 容器组件
[[service]]
name = "legacy-postproc"
type = "singleton"
runtime = "docker"
image = "app/postproc:latest"
volumes = ["/opt/app/postproc:/app"]
network = "uds"
# Nexus 为容器挂载 UDS socket，容器内 Python 通过 nexus_sdk.py 连接
```

## 传输抽象层设计

### 统一接口

```go
// Transport 是所有传输实现的统一接口
type Transport interface {
    // 客户端：连接到目标服务
    Dial(ctx context.Context, target ServiceEndpoint) (Conn, error)
    // 服务端：监听并接受连接
    Listen(ctx context.Context, addr string) (Listener, error)
}

type Conn interface {
    // 发送 msgpack 编码的消息
    Send(msg *Message) error
    // 接收 msgpack 编码的消息
    Recv() (*Message, error)
    // 可选：通过 memfd 发送大数据
    SendFd(fd int, metadata []byte) error
    RecvFd() (fd int, metadata []byte, error)
    Close() error
}

type Message struct {
    Method  string            `msgpack:"method"`
    Payload []byte            `msgpack:"payload"`
    Headers map[string]string `msgpack:"headers,omitempty"`
}
```

### 自动路由

组件调用时只需提供目标服务名，传输层自动决策：
- 注册中心查询目标地址
- 同机 → UDS
- 跨机 → TCP
- 组件代码无需关心底层传输方式

### memfd 零拷贝

当 `SendFd` 可用时（同机 + Linux）：
1. 发送方 `memfd_create` 创建匿名内存文件
2. 写入图像数据
3. 通过 UDS 的 `SCM_RIGHTS` 传递 fd
4. 接收方直接 mmap 读取，零拷贝

跨机或不支持时自动 fallback 到普通 msgpack 传输。

## 服务注册中心

### 注册流程

1. 组件启动后连接 nexusd 的 UDS
2. 发送注册请求：`{ name, id, endpoints: [{type: "uds", addr: "/run/nexus/svc/xxx.sock"}, {type: "tcp", addr: "0.0.0.0:9005"}], capabilities: [...] }`
3. nexusd 记录并广播变更给所有已注册组件
4. 组件定期心跳保活

### 发现

- 按名称：`registry.Lookup("detector")` → 返回所有 detector 实例
- 按能力：`registry.LookupByCapability("detect-vehicle")` → 返回有此能力的实例
- 变更订阅：`registry.Watch("detector", callback)` → 实例上下线通知

## Go SDK 使用示例

```go
package main

import "github.com/maxesisn/nexus/pkg/sdk"

func main() {
    // 初始化 SDK，自动连接本机 nexusd
    client, _ := sdk.New(sdk.Config{
        Name: "my-service",
        Capabilities: []string{"process-image"},
    })
    defer client.Close()

    // 注册自己为服务端
    client.Handle("process", func(req *sdk.Request) (*sdk.Response, error) {
        // 处理请求
        return &sdk.Response{Payload: result}, nil
    })

    // 作为客户端调用其他服务
    resp, _ := client.Call("detector-vehicle", "detect", payload)

    // 大数据零拷贝（自动判断是否可用）
    resp, _ = client.CallWithData("detector-vehicle", "detect", imageBytes)

    client.Serve() // 阻塞
}
```

## Python SDK 使用示例

```python
import nexus_sdk

client = nexus_sdk.Client(
    name="legacy-postproc",
    socket="/run/nexus/registry.sock"
)

# 注册处理函数
@client.handler("postprocess")
def handle(req):
    result = do_postprocess(req.payload)
    return nexus_sdk.Response(payload=result)

# 调用其他服务
resp = client.call("detector-vehicle", "detect", image_data)

client.serve()
```

## Python 适配策略

Python 旧组件通过**原生 Python SDK** 接入（不使用 sidecar）：
- `nexus_sdk.py`：纯 Python 实现，通过 UDS 连接 nexusd
- 使用 `msgpack` pip 包进行序列化
- Docker 容器内通过挂载 `/run/nexus/` 目录访问 UDS
- memfd 在 Python 侧可通过 `os.memfd_create()` (Python 3.8+) 支持

## 跨机器通信

Worker 类型组件需要 `network = "dual"` 配置：

1. 本机 nexusd 启动时，worker 同时监听 UDS 和 TCP
2. 远端机器运行 nexusd，配置 `peers`：

```toml
[daemon]
socket = "/run/nexus/registry.sock"

# 对等节点（远端注册中心）
[[daemon.peers]]
addr = "192.168.1.100:7700"  # 主节点
```

3. 两个 nexusd 互相同步注册信息
4. 调用远端 worker 时，传输层自动走 TCP

## 目标内核兼容性

- 最低 Linux 4.19
- memfd_create: Linux 3.17+（满足）
- UDS SCM_RIGHTS: 所有 Linux 版本支持

## 构建

```bash
# 编译 nexusd
go build -o nexusd ./cmd/nexusd/

# 静态编译（适合分发）
CGO_ENABLED=0 go build -o nexusd ./cmd/nexusd/
```

## 开发阶段规划

### Phase 1：核心骨架
- [x] nexusd 守护进程启动/信号处理
- [x] 配置解析（TOML）
- [x] 进程管理（启动/停止/重启）
- [x] 健康检查（进程存活检测）
- [ ] `depends_on` / `health_check` 配置生效（规划中，当前未实现）

### Phase 2：传输层
- [x] UDS transport + msgpack
- [x] TCP transport + msgpack
- [x] 统一 Transport 接口
- [x] 自动路由（同机 UDS / 跨机 TCP）

### Phase 3：服务注册中心
- [x] 注册/注销/心跳
- [x] 服务发现（按名称/按能力）
- [x] 变更通知
- [ ] 多节点注册信息同步

### Phase 4：高级特性
- [x] memfd 零拷贝
- [x] Go SDK
- [ ] Python SDK
- [ ] Docker 容器组件支持

### Phase 5：CLI & 运维
- [ ] nexusctl 命令行工具（查看状态、手动操作）
- [ ] 日志聚合
- [ ] metrics 导出

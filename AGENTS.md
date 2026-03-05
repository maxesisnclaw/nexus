# AGENTS.md — Codex 行为指南

## 项目概述

你正在实现 **Nexus**，一个通用的微服务基座框架。详细设计见 `DESIGN.md`。

## 关键约束

1. **语言**：Go，`go mod` 管理依赖
2. **目标平台**：Linux only，最低内核 4.19
3. **静态编译**：`CGO_ENABLED=0` 必须能编译通过
4. **无外部依赖原则**：尽可能使用标准库。允许的例外：
   - `github.com/vmihailenco/msgpack/v5`（msgpack 编解码）
   - `github.com/BurntSushi/toml`（TOML 配置解析）
   - 其他依赖需有充分理由
5. **代码风格**：标准 Go 风格，`go vet` / `go lint` 无警告
6. **注释语言**：英文

## 开发顺序

按 DESIGN.md 的 Phase 规划推进。每个 Phase 完成后：
1. 确保 `go build ./...` 通过
2. 写基本单元测试
3. `go test ./...` 全部通过
4. `git add -A && git commit` 带有意义的 commit message

## 决策原则

遇到设计上不确定的地方：
- **优先选简单方案**，后续可以扩展
- **优先选标准库方案**，避免引入不必要的依赖
- 如果两个方案差异很大且都有道理，选更通用的那个
- 只有在真正无法判断（影响全局架构方向）时才退出询问

## 具体技术指导

### UDS
- 使用 `net.Listen("unix", path)` / `net.Dial("unix", path)`
- socket 文件放在 `/run/nexus/` 下
- 注意清理旧 socket 文件

### memfd
- 使用 `unix.MemfdCreate()` (golang.org/x/sys/unix)
- `SCM_RIGHTS` 通过 `unix.Sendmsg` / `unix.Recvmsg` 传递 fd
- 这是 Linux-specific 特性，用 build tag `//go:build linux` 隔离
- golang.org/x/sys/unix 是允许的依赖

### msgpack
- 统一使用 `github.com/vmihailenco/msgpack/v5`
- 所有 IPC 消息都走 msgpack 编解码

### 进程管理
- 使用 `os/exec` 启动子进程
- 用 `syscall.Kill` + `os.Process.Signal` 管理
- 实现优雅关闭：先 SIGTERM，超时后 SIGKILL

### 配置
- TOML 格式，使用 `github.com/BurntSushi/toml`
- 配置结构体定义在 `pkg/config/types.go`

### Docker 容器管理
- 通过 `docker` CLI 命令管理（`exec.Command("docker", ...)`）
- 不引入 Docker SDK，保持轻量

### 日志
- 使用标准库 `log/slog`
- 结构化日志，JSON 格式

## 文件组织

严格遵循 DESIGN.md 中的目录结构。新增文件时放在合理的位置。

## 测试

- 每个 package 写 `_test.go`
- 需要 Linux 特性的测试用 `//go:build linux` 标记
- 测试可以使用临时目录和临时 UDS
- transport 层测试用 localhost loopback

## 不要做的事

- 不要引入 gRPC / protobuf
- 不要引入 HTTP 框架
- 不要引入 etcd / consul / zookeeper
- 不要引入容器编排工具（K8s SDK 等）
- 不要写 Makefile，用 `go build` / `go test` 即可
- 不要在 macOS 上测试 memfd（编译隔离即可）

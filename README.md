# ai-dev-case

AI 开发案例与 Demo 项目，各示例按目录独立存放。

## 项目结构

```
.
├── bin/                           # 编译产物目录（已加入 .gitignore）
├── cmd/
│   ├── 4s-distributed-lock/       # Redis + Lua 秒杀库存（分布式锁 / 原子扣减）
│   │   └── main.go
│   ├── 4s-goroutine-optimization/  # Goroutine + Channel 并发优化（可控制最大协程数）
│   │   └── main.go
│   └── redis-reentrant-lock/       # Redis 可重入分布式锁（Lua 原子性、过期、续期、日志）
│       ├── lock.go
│       ├── main.go
│       └── main_test.go
├── Makefile
├── go.mod
├── go.sum
└── README.md
```

编译生成的二进制会输出到 `bin/`，该目录已被 `.gitignore` 忽略。

## 编译与运行

推荐使用 Makefile 管理编译（在项目根目录执行）：

```bash
make build          # 编译全部到 bin/
make clean          # 清理 bin/
make 4s-distributed-lock      # 只编译该 demo
make redis-reentrant-lock     # 只编译该 demo
make test            # 运行所有测试
make run-help        # 查看运行说明
```

或手动编译到 `bin/`：

```bash
mkdir -p bin
go build -o bin/4s-distributed-lock      ./cmd/4s-distributed-lock
go build -o bin/4s-goroutine-optimization ./cmd/4s-goroutine-optimization
go build -o bin/redis-reentrant-lock      ./cmd/redis-reentrant-lock
```

直接运行（不生成二进制）：

| Demo | 命令 | 说明 |
|------|------|------|
| 秒杀库存（Redis+Lua） | `go run ./cmd/4s-distributed-lock` | 需本地 Redis |
| Goroutine+Channel 优化 | `go run ./cmd/4s-goroutine-optimization` | 纯内存演示，无需 Redis |
| Redis 可重入分布式锁 | `go run ./cmd/redis-reentrant-lock` | Lua 原子性、可重入、过期与续期，带日志；自测：`go test ./cmd/redis-reentrant-lock/...` |

## 依赖

- Go 1.24+
- Redis（`4s-distributed-lock`、`redis-reentrant-lock` 需要）

```bash
go mod tidy
```

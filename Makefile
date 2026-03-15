# Makefile - 管理编译与常用命令

BIN_DIR := bin
GO      := go

# 各 demo 的二进制名与源码路径
CMDS := 4s-distributed-lock 4s-goroutine-optimization redis-reentrant-lock
CMD_PATHS := $(addprefix ./cmd/,$(CMDS))

.PHONY: all build clean test deps run-help
.PHONY: 4s-distributed-lock 4s-goroutine-optimization redis-reentrant-lock

# 默认：编译全部
all: build

# 编译全部到 bin/
build: $(addprefix $(BIN_DIR)/,$(CMDS))

$(BIN_DIR)/%: ./cmd/%
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $@ ./cmd/$*

# 单独编译
4s-distributed-lock:      $(BIN_DIR)/4s-distributed-lock
4s-goroutine-optimization: $(BIN_DIR)/4s-goroutine-optimization
redis-reentrant-lock:      $(BIN_DIR)/redis-reentrant-lock

# 清理编译产物
clean:
	rm -rf $(BIN_DIR)

# 运行测试
test:
	$(GO) test ./...

# 仅测试 redis-reentrant-lock（需 Redis）
test-redis-lock:
	$(GO) test -v ./cmd/redis-reentrant-lock/...

# 依赖
deps:
	$(GO) mod tidy
	$(GO) mod download

# 运行说明
run-help:
	@echo "运行已编译的二进制:"
	@echo "  ./$(BIN_DIR)/4s-distributed-lock"
	@echo "  ./$(BIN_DIR)/4s-goroutine-optimization"
	@echo "  ./$(BIN_DIR)/redis-reentrant-lock"
	@echo ""
	@echo "或直接 go run:"
	@echo "  $(GO) run ./cmd/4s-distributed-lock"
	@echo "  $(GO) run ./cmd/4s-goroutine-optimization"
	@echo "  $(GO) run ./cmd/redis-reentrant-lock"

// 使用 goroutine + channel 模式优化高并发秒杀请求，支持自主控制最大协程数。
// 文件内附：与传统互斥锁（Mutex）的对比分析。
//
// 运行: go run ./cmd/4s-goroutine-optimization
package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// ========== 请求与结果 ==========

// SeckillRequest 一次秒杀请求（可替换为真实 Redis 调用）
type SeckillRequest struct {
	UserID string
	Seq    int
}

// SeckillResult 秒杀结果
type SeckillResult struct {
	UserID string
	Seq    int
	OK     bool  // 是否抢到
	Err    error // 异常信息
}

// ========== 方式一：Goroutine + Channel（可控制最大协程数）==========

// ChannelPool 基于 channel 的协程池：通过「任务 channel + 结果 channel + 固定 worker 数」控制最大协程数
type ChannelPool struct {
	maxWorkers int
	jobCh      chan SeckillRequest
	resultCh   chan SeckillResult
	doSeckill  func(ctx context.Context, req SeckillRequest) (ok bool, err error)
}

// NewChannelPool 创建协程池，maxWorkers 为最大并发协程数，queueSize 为待处理任务队列长度
func NewChannelPool(maxWorkers, queueSize int, doSeckill func(context.Context, SeckillRequest) (bool, error)) *ChannelPool {
	return &ChannelPool{
		maxWorkers: maxWorkers,
		jobCh:      make(chan SeckillRequest, queueSize),
		resultCh:   make(chan SeckillResult, queueSize),
		doSeckill:  doSeckill,
	}
}

// Start 启动 worker 协程，消费 jobCh，将结果写入 resultCh。调用者应在发完任务后关闭 jobCh
func (p *ChannelPool) Start(ctx context.Context) {
	for i := 0; i < p.maxWorkers; i++ {
		go func(workerID int) {
			for req := range p.jobCh {
				ok, err := p.doSeckill(ctx, req)
				select {
				case p.resultCh <- SeckillResult{UserID: req.UserID, Seq: req.Seq, OK: ok, Err: err}:
				case <-ctx.Done():
					return
				}
			}
		}(i)
	}
}

// Submit 提交任务（非阻塞，队列满则根据 channel 语义阻塞或可改为 select default 返回繁忙）
func (p *ChannelPool) Submit(req SeckillRequest) {
	p.jobCh <- req
}

// SubmitBatch 批量提交并关闭任务通道，便于 Start 的 worker 自然退出
func (p *ChannelPool) SubmitBatch(reqs []SeckillRequest) {
	for _, req := range reqs {
		p.jobCh <- req
	}
	close(p.jobCh)
}

// Results 返回结果 channel，供调用方遍历收集结果
func (p *ChannelPool) Results() <-chan SeckillResult {
	return p.resultCh
}

// CloseResults 在确认所有结果已消费后关闭 resultCh（可选，也可由调用方读完即结束）
func (p *ChannelPool) CloseResults() {
	close(p.resultCh)
}

// RunWithLimit 一站式：用 maxWorkers 个协程处理 reqs，收集全部结果。可自主控制最大协程数
func RunWithLimit(ctx context.Context, reqs []SeckillRequest, maxWorkers int, doSeckill func(context.Context, SeckillRequest) (bool, error)) (success, fail int, results []SeckillResult) {
	if maxWorkers <= 0 {
		maxWorkers = 1
	}
	qSize := len(reqs) + 1
	if qSize < 2 {
		qSize = 2
	}
	pool := NewChannelPool(maxWorkers, qSize, doSeckill)
	pool.Start(ctx)
	go func() {
		for _, r := range reqs {
			pool.Submit(r)
		}
		close(pool.jobCh)
	}()

	results = make([]SeckillResult, 0, len(reqs))
	for i := 0; i < len(reqs); i++ {
		select {
		case res := <-pool.resultCh:
			results = append(results, res)
			if res.Err != nil {
				fail++
			} else if res.OK {
				success++
			} else {
				fail++
			}
		case <-ctx.Done():
			return success, fail, results
		}
	}
	return success, fail, results
}

// ========== 方式二：传统 Mutex 并发（无协程数上限）==========

// MutexStock 使用互斥锁保护的库存，模拟传统「共享变量 + 锁」方式
type MutexStock struct {
	mu     sync.Mutex
	stock  int64
	failed int64 // 统计抢购失败次数（含库存不足）
}

func NewMutexStock(initial int64) *MutexStock {
	return &MutexStock{stock: initial}
}

// TrySeckillMutex 传统方式：多个 goroutine 竞争同一把锁，扣减库存
func (m *MutexStock) TrySeckillMutex() (ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stock <= 0 {
		m.failed++
		return false
	}
	m.stock--
	return true
}

// ========== 对比用：Channel 信号量限制 goroutine 数 + 共享库存 ==========

// ChannelSemaphoreStock 用 channel 做信号量限制「同时扣减」的协程数，库存用原子变量
type ChannelSemaphoreStock struct {
	stock  atomic.Int64
	sem    chan struct{} // 有缓冲 channel 作为信号量，cap = maxConcurrent
	maxCon int
	failed atomic.Int64
}

func NewChannelSemaphoreStock(initial int64, maxConcurrent int) *ChannelSemaphoreStock {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	c := &ChannelSemaphoreStock{sem: make(chan struct{}, maxConcurrent), maxCon: maxConcurrent}
	c.stock.Store(initial)
	return c
}

// TrySeckillSemaphore 先获取信号量（控制最大并发数），再原子扣减库存
func (c *ChannelSemaphoreStock) TrySeckillSemaphore() (ok bool) {
	c.sem <- struct{}{}        // 获取信号量，超过 maxConcurrent 会阻塞
	defer func() { <-c.sem }() // 释放
	for {
		n := c.stock.Load()
		if n <= 0 {
			c.failed.Add(1)
			return false
		}
		if c.stock.CompareAndSwap(n, n-1) {
			return true
		}
	}
}

// ========== 模拟秒杀逻辑（可替换为真实 Redis 调用）==========

func mockSeckill(ctx context.Context, req SeckillRequest, stock *atomic.Int64) (ok bool, err error) {
	for {
		n := stock.Load()
		if n <= 0 {
			return false, nil
		}
		if stock.CompareAndSwap(n, n-1) {
			return true, nil
		}
		// CAS 失败，重试或退出
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}
	}
}

// ========== 与传统互斥锁的区别分析（文档）==========
//
// ## Goroutine + Channel 模式 vs 传统 Mutex
//
// ### 1. 并发数控制
// - **Mutex**：通常不限制 goroutine 数量，成百上千个 goroutine 同时去 Lock，抢到锁的只有一个，
//   其余全部阻塞。goroutine 数量可以远大于 CPU 核心数，导致大量上下文切换和内存占用。
// - **Channel（Worker Pool / 信号量）**：通过「固定 worker 数」或「带缓冲的 channel 作信号量」
//   明确限制「同时执行」的协程数（如 50、200），其余任务在 channel 里排队，避免无界并发。
//
// ### 2. 资源与可预测性
// - **Mutex**：并发请求数不可控，流量突增时 goroutine 数可能暴增，有 OOM 或调度延迟风险。
// - **Channel**：最大并发由 maxWorkers 或 sem channel 的 cap 决定，便于做容量规划和背压。
//
// ### 3. 数据流与所有权
// - **Mutex**：多 goroutine 共享同一块内存，通过加锁串行访问；容易产生「谁在何时改了什么」难以追踪。
// - **Channel**：通过「把任务发到 channel、worker 取任务并回写结果」形成单向数据流，
//   「谁负责生产、谁负责消费」清晰，符合 "Don't communicate by sharing memory; share memory by communicating"。
//
// ### 4. 适用场景简表
// - **Mutex 更合适**：临界区极小、并发度本身不高、逻辑简单（如简单计数器、配置读写）。
// - **Channel 更合适**：需要限制并发数、流水线/多阶段处理、需要优雅关闭、需要超时/取消传播。
//
// ### 5. 本示例中的对应关系
// - **MutexStock.TrySeckillMutex**：传统做法，goroutine 数无上限，仅用锁保护库存。
// - **ChannelPool / RunWithLimit**：固定 maxWorkers 个 worker，任务经 channel 分发，结果经 channel 汇总，协程数可控。
// - **ChannelSemaphoreStock**：用 channel 当信号量限制「同时扣减」的协程数，库存用原子操作（演示用；真实秒杀建议 Redis+Lua）。

func main() {
	const totalStock = 20
	const totalRequests = 100
	const maxWorkers = 10

	fmt.Println("========== 1. Goroutine + Channel 模式（限制最大协程数 =", maxWorkers, "）==========")
	stock := &atomic.Int64{}
	stock.Store(totalStock)
	ctx := context.Background()
	reqs := make([]SeckillRequest, totalRequests)
	for i := range reqs {
		reqs[i] = SeckillRequest{UserID: fmt.Sprintf("user_%d", i), Seq: i}
	}
	doSeckill := func(ctx context.Context, req SeckillRequest) (bool, error) {
		return mockSeckill(ctx, req, stock)
	}
	success, fail, _ := RunWithLimit(ctx, reqs, maxWorkers, doSeckill)
	fmt.Printf("成功: %d, 失败/不足: %d, 剩余库存(原子): %d\n", success, fail, stock.Load())

	fmt.Println("\n========== 2. 传统 Mutex 方式（无协程数上限，开 100 个 goroutine 竞争）==========")
	mStock := NewMutexStock(totalStock)
	var wg sync.WaitGroup
	var mutexSuccess int64
	for i := 0; i < totalRequests; i++ {
		wg.Add(1)
		go func(seq int) {
			defer wg.Done()
			if mStock.TrySeckillMutex() {
				atomic.AddInt64(&mutexSuccess, 1)
			}
		}(i)
	}
	wg.Wait()
	fmt.Printf("成功: %d, 失败: %d\n", mutexSuccess, totalRequests-int(mutexSuccess))

	fmt.Println("\n========== 3. Channel 信号量限制并发（最大同时扣减 =", maxWorkers, "）==========")
	cStock := NewChannelSemaphoreStock(totalStock, maxWorkers)
	var cWg sync.WaitGroup
	for i := 0; i < totalRequests; i++ {
		cWg.Add(1)
		go func() {
			defer cWg.Done()
			cStock.TrySeckillSemaphore()
		}()
	}
	cWg.Wait()
	fmt.Printf("剩余库存: %d（信号量限制同时扣减的协程数，避免无界并发）\n", cStock.stock.Load())
}

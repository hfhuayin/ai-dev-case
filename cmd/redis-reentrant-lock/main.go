// Redis 可重入分布式锁 Demo：Lua 保证原子性，支持重入、过期与续期，带日志。
//
// 运行: go run ./cmd/redis-reentrant-lock
package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

func main() {
	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,
	})
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("Redis 未连接: %v（请先启动 Redis）", err)
	}

	lockKey := "lock:demo:reentrant"
	ttl := 5 * time.Second

	fmt.Println("========== 1. 单次加锁与解锁 ==========")
	lock1 := NewReentrantLock(rdb, lockKey, ttl)
	if err := lock1.TryLock(ctx); err != nil {
		log.Printf("加锁失败: %v", err)
	} else {
		fmt.Println("加锁成功，执行业务...")
		time.Sleep(100 * time.Millisecond)
		if err := lock1.Unlock(ctx); err != nil {
			log.Printf("解锁失败: %v", err)
		} else {
			fmt.Println("解锁成功")
		}
	}

	fmt.Println("\n========== 2. 可重入：同一 holder 多次 Lock/Unlock ==========")
	holderID := "client-demo-1"
	lock2 := NewReentrantLock(rdb, lockKey, ttl).WithHolderID(holderID)
	for i := 0; i < 3; i++ {
		if err := lock2.TryLock(ctx); err != nil {
			log.Printf("重入加锁 %d 失败: %v", i+1, err)
			break
		}
		fmt.Printf("重入加锁第 %d 次成功\n", i+1)
	}
	for i := 0; i < 3; i++ {
		_ = lock2.Unlock(ctx)
		fmt.Printf("重入解锁第 %d 次\n", i+1)
	}
	fmt.Println("可重入加锁/解锁完成")

	fmt.Println("\n========== 3. 并发：多 goroutine 争抢同一把锁 ==========")
	var wg sync.WaitGroup
	successCount := 0
	var mu sync.Mutex
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			l := NewReentrantLock(rdb, lockKey, ttl)
			ctxTry, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			if err := l.Lock(ctxTry, 100*time.Millisecond); err != nil {
				return
			}
			defer l.Unlock(ctx)
			mu.Lock()
			successCount++
			mu.Unlock()
			log.Printf("goroutine %d 获得锁", id)
			time.Sleep(50 * time.Millisecond)
		}(i)
	}
	wg.Wait()
	fmt.Printf("并发加锁完成，成功获得锁的 goroutine 数: %d\n", successCount)

	fmt.Println("\n========== 4. 锁过期 ==========")
	shortTTL := 500 * time.Millisecond
	lock3 := NewReentrantLock(rdb, lockKey, shortTTL)
	if err := lock3.TryLock(ctx); err != nil {
		log.Printf("加锁失败: %v", err)
	} else {
		fmt.Println("加锁成功，等待锁过期...")
		time.Sleep(600 * time.Millisecond)
		// 此时 key 已过期，Unlock 会得到 key 不存在
		if err := lock3.Unlock(ctx); err != nil {
			fmt.Printf("解锁时发现锁已过期: %v\n", err)
		}
	}

	fmt.Println("\nDemo 结束")
}

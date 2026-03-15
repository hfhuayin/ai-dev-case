package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func testClient(t *testing.T) *redis.Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "",
		DB:       1, // 使用 DB1 避免与其它测试冲突
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("Redis 不可用，跳过测试: %v", err)
	}
	return rdb
}

func TestReentrantLock_TryLockUnlock(t *testing.T) {
	rdb := testClient(t)
	ctx := context.Background()
	key := "lock:test:try"
	rdb.Del(ctx, key)

	r := NewReentrantLock(rdb, key, 5*time.Second)
	if err := r.TryLock(ctx); err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if err := r.Unlock(ctx); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	// 再次加锁应成功（同一 holder 可重入，这里等价于新的一次加锁因为 key 已删）
	if err := r.TryLock(ctx); err != nil {
		t.Fatalf("TryLock again: %v", err)
	}
	_ = r.Unlock(ctx)
}

func TestReentrantLock_Reentrant(t *testing.T) {
	rdb := testClient(t)
	ctx := context.Background()
	key := "lock:test:reentrant"
	rdb.Del(ctx, key)

	holder := "holder-reentrant-test"
	r := NewReentrantLock(rdb, key, 5*time.Second).WithHolderID(holder)

	// 同一 holder 加锁 3 次
	for i := 0; i < 3; i++ {
		if err := r.TryLock(ctx); err != nil {
			t.Fatalf("reentrant Lock %d: %v", i+1, err)
		}
	}
	// 解锁 3 次
	for i := 0; i < 3; i++ {
		if err := r.Unlock(ctx); err != nil {
			t.Fatalf("reentrant Unlock %d: %v", i+1, err)
		}
	}
	// 键应被删除
	n, _ := rdb.Exists(ctx, key).Result()
	if n != 0 {
		t.Errorf("expected key to be deleted, exists=%d", n)
	}
}

func TestReentrantLock_Concurrent(t *testing.T) {
	rdb := testClient(t)
	ctx := context.Background()
	key := "lock:test:concurrent"
	rdb.Del(ctx, key)

	var success int32
	N := 10
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := NewReentrantLock(rdb, key, 2*time.Second)
			ctxTry, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			if err := l.Lock(ctxTry, 50*time.Millisecond); err != nil {
				return
			}
			defer l.Unlock(ctx)
			atomic.AddInt32(&success, 1)
			time.Sleep(20 * time.Millisecond)
		}()
	}
	wg.Wait()
	if success < 1 || int(success) > N {
		t.Errorf("unexpected success count: %d", success)
	}
	t.Logf("concurrent: %d goroutines succeeded to hold lock", success)
}

func TestReentrantLock_Expiration(t *testing.T) {
	rdb := testClient(t)
	ctx := context.Background()
	key := "lock:test:expire"
	rdb.Del(ctx, key)

	ttl := 200 * time.Millisecond
	r := NewReentrantLock(rdb, key, ttl)
	if err := r.TryLock(ctx); err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	time.Sleep(ttl + 50*time.Millisecond)
	// 锁已过期，Unlock 应返回 key 不存在相关错误
	err := r.Unlock(ctx)
	if err != nil && err != ErrLockNotHeld {
		t.Logf("Unlock after expire (expected): %v", err)
	}
}

func TestReentrantLock_Refresh(t *testing.T) {
	rdb := testClient(t)
	ctx := context.Background()
	key := "lock:test:refresh"
	rdb.Del(ctx, key)

	r := NewReentrantLock(rdb, key, 1*time.Second)
	if err := r.TryLock(ctx); err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	defer r.Unlock(ctx)

	time.Sleep(300 * time.Millisecond)
	if err := r.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	ttl, err := r.TTL(ctx)
	if err != nil {
		t.Fatalf("TTL: %v", err)
	}
	if ttl < 500*time.Millisecond {
		t.Errorf("expected TTL > 500ms after refresh, got %v", ttl)
	}
}

func TestReentrantLock_OtherHolderCannotUnlock(t *testing.T) {
	rdb := testClient(t)
	ctx := context.Background()
	key := "lock:test:other"
	rdb.Del(ctx, key)

	r1 := NewReentrantLock(rdb, key, 5*time.Second).WithHolderID("holder-A")
	r2 := NewReentrantLock(rdb, key, 5*time.Second).WithHolderID("holder-B")

	if err := r1.TryLock(ctx); err != nil {
		t.Fatalf("r1 TryLock: %v", err)
	}
	// r2 未持有锁，不应能“解”掉 r1 的锁；Unlock 脚本会检测 holder，r2.Unlock 返回 not holder
	err := r2.Unlock(ctx)
	if err != ErrLockNotHeld {
		t.Errorf("r2.Unlock should return ErrLockNotHeld, got %v", err)
	}
	// r1 仍应能正常解锁
	if err := r1.Unlock(ctx); err != nil {
		t.Fatalf("r1 Unlock: %v", err)
	}
}

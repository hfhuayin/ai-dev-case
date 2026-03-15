// Package main 实现基于 Redis + Lua 的可重入分布式锁，保证加锁/解锁/续期原子性。
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	// Lua: 加锁（含可重入）。KEYS[1]=lockKey, ARGV[1]=holderId, ARGV[2]=ttlMs
	// 返回: 1 成功，0 已被他人占用
	scriptLock = `
		local v = redis.call("GET", KEYS[1])
		if v == false or v == "" then
			redis.call("SET", KEYS[1], ARGV[1] .. ":1", "PX", tonumber(ARGV[2]))
			return 1
		end
		local i = string.find(v, ":")
		if not i then return 0 end
		local holder = string.sub(v, 1, i - 1)
		local count = tonumber(string.sub(v, i + 1)) or 0
		if holder == ARGV[1] then
			count = count + 1
			redis.call("SET", KEYS[1], ARGV[1] .. ":" .. count, "PX", tonumber(ARGV[2]))
			return 1
		end
		return 0
	`
	// Lua: 解锁。KEYS[1]=lockKey, ARGV[1]=holderId, ARGV[2]=ttlMs（续期用）
	// 返回: 1 成功释放，0 重入计数减一，-1 键不存在，-2 非本持有者
	scriptUnlock = `
		local v = redis.call("GET", KEYS[1])
		if v == false then return -1 end
		local i = string.find(v, ":")
		if not i then return -2 end
		local holder = string.sub(v, 1, i - 1)
		local count = tonumber(string.sub(v, i + 1)) or 0
		if holder ~= ARGV[1] then return -2 end
		count = count - 1
		if count <= 0 then
			redis.call("DEL", KEYS[1])
			return 1
		else
			redis.call("SET", KEYS[1], ARGV[1] .. ":" .. count, "PX", tonumber(ARGV[2]))
			return 0
		end
	`
	// Lua: 续期。仅当持有者匹配时 PEXPIRE。KEYS[1]=lockKey, ARGV[1]=holderId, ARGV[2]=ttlMs
	// 返回: 1 成功，0 非本持有者或键不存在
	scriptRefresh = `
		local v = redis.call("GET", KEYS[1])
		if v == false then return 0 end
		local i = string.find(v, ":")
		if not i then return 0 end
		local holder = string.sub(v, 1, i - 1)
		if holder ~= ARGV[1] then return 0 end
		redis.call("PEXPIRE", KEYS[1], tonumber(ARGV[2]))
		return 1
	`
)

// ReentrantLock Redis 可重入分布式锁，Lua 保证原子性，支持锁过期与续期
type ReentrantLock struct {
	client   *redis.Client
	lockKey  string
	holderID string
	ttl      time.Duration
	scriptLock   *redis.Script
	scriptUnlock *redis.Script
	scriptRefresh *redis.Script
}

// NewReentrantLock 创建一个可重入锁。lockKey 为 Redis 键，ttl 为锁过期时间（避免死锁）
func NewReentrantLock(client *redis.Client, lockKey string, ttl time.Duration) *ReentrantLock {
	if ttl <= 0 {
		ttl = 10 * time.Second
	}
	return &ReentrantLock{
		client:        client,
		lockKey:       lockKey,
		holderID:      uuid.New().String(),
		ttl:           ttl,
		scriptLock:    redis.NewScript(scriptLock),
		scriptUnlock:  redis.NewScript(scriptUnlock),
		scriptRefresh: redis.NewScript(scriptRefresh),
	}
}

// WithHolderID 指定持有者 ID（用于测试或固定客户端标识）
func (r *ReentrantLock) WithHolderID(id string) *ReentrantLock {
	r.holderID = id
	return r
}

// HolderID 返回当前持有者 ID
func (r *ReentrantLock) HolderID() string { return r.holderID }

// TryLock 尝试加锁一次，不阻塞；支持可重入
func (r *ReentrantLock) TryLock(ctx context.Context) error {
	return r.lock(ctx)
}

// Lock 在 ctx 超时前重试加锁，支持可重入；interval 为重试间隔，若为 0 则只尝试一次
func (r *ReentrantLock) Lock(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return r.lock(ctx)
	}
	for {
		if err := r.lock(ctx); err == nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (r *ReentrantLock) lock(ctx context.Context) error {
	ttlMs := r.ttl.Milliseconds()
	if ttlMs <= 0 {
		ttlMs = 10000
	}
	n, err := r.scriptLock.Run(ctx, r.client, []string{r.lockKey}, r.holderID, ttlMs).Int()
	if err != nil {
		log.Printf("[ReentrantLock] Lock key=%s holder=%s err=%v", r.lockKey, r.holderID, err)
		return err
	}
	if n == 0 {
		log.Printf("[ReentrantLock] Lock key=%s holder=%s failed: lock held by others", r.lockKey, r.holderID)
		return ErrLockNotAcquired
	}
	log.Printf("[ReentrantLock] Lock key=%s holder=%s ttl=%v acquired", r.lockKey, r.holderID, r.ttl)
	return nil
}

// TTL 返回锁的剩余过期时间（仅当 key 存在时有效）
func (r *ReentrantLock) TTL(ctx context.Context) (time.Duration, error) {
	d, err := r.client.PTTL(ctx, r.lockKey).Result()
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, nil // key 存在但无过期
	}
	return d, nil
}

// Unlock 释放一次持有计数；完全释放时删除 Redis 键
func (r *ReentrantLock) Unlock(ctx context.Context) error {
	ttlMs := r.ttl.Milliseconds()
	if ttlMs <= 0 {
		ttlMs = 10000
	}
	n, err := r.scriptUnlock.Run(ctx, r.client, []string{r.lockKey}, r.holderID, ttlMs).Int()
	if err != nil {
		log.Printf("[ReentrantLock] Unlock key=%s holder=%s err=%v", r.lockKey, r.holderID, err)
		return err
	}
	switch n {
	case 1:
		log.Printf("[ReentrantLock] Unlock key=%s holder=%s released", r.lockKey, r.holderID)
		return nil
	case 0:
		log.Printf("[ReentrantLock] Unlock key=%s holder=%s reentrant count decreased", r.lockKey, r.holderID)
		return nil
	case -1:
		log.Printf("[ReentrantLock] Unlock key=%s holder=%s key already gone (expired?)", r.lockKey, r.holderID)
		return ErrLockNotHeld
	case -2:
		log.Printf("[ReentrantLock] Unlock key=%s holder=%s not holder", r.lockKey, r.holderID)
		return ErrLockNotHeld
	default:
		return fmt.Errorf("unlock script returned %d", n)
	}
}

// Refresh 续期当前锁（仅当本持有者时生效），用于长任务避免中途过期
func (r *ReentrantLock) Refresh(ctx context.Context) error {
	ttlMs := r.ttl.Milliseconds()
	if ttlMs <= 0 {
		ttlMs = 10000
	}
	n, err := r.scriptRefresh.Run(ctx, r.client, []string{r.lockKey}, r.holderID, ttlMs).Int()
	if err != nil {
		log.Printf("[ReentrantLock] Refresh key=%s holder=%s err=%v", r.lockKey, r.holderID, err)
		return err
	}
	if n == 0 {
		log.Printf("[ReentrantLock] Refresh key=%s holder=%s failed: not holder or key gone", r.lockKey, r.holderID)
		return ErrLockNotHeld
	}
	log.Printf("[ReentrantLock] Refresh key=%s holder=%s ttl=%v ok", r.lockKey, r.holderID, r.ttl)
	return nil
}

var (
	ErrLockNotAcquired = fmt.Errorf("lock not acquired")
	ErrLockNotHeld     = fmt.Errorf("lock not held by this holder")
)

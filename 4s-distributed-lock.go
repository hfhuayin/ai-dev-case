// Package main 使用 Redis + Lua 脚本实现秒杀库存场景
// 通过 Lua 保证「判断库存 + 扣减」的原子性，防止超卖
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// Lua: 原子扣减库存，仅当库存充足时扣减并返回 1，否则返回 0
	// KEYS[1]: 库存 key，ARGV[1]: 扣减数量
	scriptDecrStock = `
		local key = KEYS[1]
		local decr = tonumber(ARGV[1])
		local current = tonumber(redis.call("GET", key) or "0")
		if current >= decr then
			redis.call("DECRBY", key, decr)
			return 1
		end
		return 0
	`
)

// SeckillService 秒杀库存服务（Redis + Lua）
type SeckillService struct {
	client *redis.Client
	script *redis.Script
}

// NewSeckillService 创建秒杀服务，需传入已配置的 Redis 客户端
func NewSeckillService(client *redis.Client) *SeckillService {
	return &SeckillService{
		client: client,
		script: redis.NewScript(scriptDecrStock),
	}
}

// InitStock 初始化商品库存（一般由后台在活动开始前调用）
func (s *SeckillService) InitStock(ctx context.Context, productKey string, total int64) error {
	return s.client.Set(ctx, productKey, total, 0).Err()
}

// DecrStock 原子扣减库存（Lua 保证原子性）
// productKey: 如 "seckill:stock:product_1001"
// quantity: 每次扣减数量，通常为 1
// 返回: (true, nil) 扣减成功；(false, nil) 库存不足；(false, err) 异常
func (s *SeckillService) DecrStock(ctx context.Context, productKey string, quantity int64) (ok bool, err error) {
	result, err := s.script.Run(ctx, s.client, []string{productKey}, quantity).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

// GetStock 查询当前库存（仅查询，不扣减）
func (s *SeckillService) GetStock(ctx context.Context, productKey string) (int64, error) {
	v, err := s.client.Get(ctx, productKey).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return v, err
}

// TrySeckill 一次完整的秒杀尝试：先扣减库存，成功则可继续下单等逻辑
func (s *SeckillService) TrySeckill(ctx context.Context, productKey string, userID string) (success bool, err error) {
	ok, err := s.DecrStock(ctx, productKey, 1)
	if err != nil {
		return false, fmt.Errorf("扣减库存失败: %w", err)
	}
	if !ok {
		return false, nil // 库存不足
	}
	// 这里可扩展：记录用户抢购成功（如写入 Redis Set 或 MQ），后续异步下单
	_ = userID
	return true, nil
}

// WithDistributedLock 使用 Redis 分布式锁执行 fn（可用于初始化库存等需要互斥的操作）
func (s *SeckillService) WithDistributedLock(ctx context.Context, lockKey string, ttl time.Duration, fn func() error) error {
	lockVal := fmt.Sprintf("%d", time.Now().UnixNano())
	ok, err := s.client.SetNX(ctx, lockKey, lockVal, ttl).Result()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("获取分布式锁失败: %s", lockKey)
	}
	// 简单起见未做 Lua 的「仅删除自己持有的锁」；生产建议用 Lua 判断再 DEL
	defer s.client.Del(ctx, lockKey)
	return fn()
}

func main() {
	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,
	})
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		fmt.Println("Redis 未连接，请先启动 Redis:", err)
		return
	}

	svc := NewSeckillService(rdb)
	productKey := "seckill:stock:product_1001"

	// 初始化库存
	if err := svc.InitStock(ctx, productKey, 10); err != nil {
		fmt.Println("初始化库存失败:", err)
		return
	}
	fmt.Println("库存已初始化为 10")

	// 模拟多次秒杀
	for i := 0; i < 12; i++ {
		ok, err := svc.TrySeckill(ctx, productKey, fmt.Sprintf("user_%d", i))
		if err != nil {
			fmt.Printf("第 %d 次: 异常 %v\n", i+1, err)
			continue
		}
		if ok {
			fmt.Printf("第 %d 次: 抢购成功\n", i+1)
		} else {
			fmt.Printf("第 %d 次: 库存不足\n", i+1)
		}
	}

	left, _ := svc.GetStock(ctx, productKey)
	fmt.Println("剩余库存:", left)
}

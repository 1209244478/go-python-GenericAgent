package auth

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type codeEntry struct {
	code      string
	expiresAt time.Time
}

type CodeStore struct {
	client *redis.Client
	ttl    time.Duration
	fallback *fallbackStore
}

type fallbackStore struct {
	mu    sync.RWMutex
	codes map[string]*codeEntry
}

func newFallbackStore() *fallbackStore {
	return &fallbackStore{codes: make(map[string]*codeEntry)}
}

func (f *fallbackStore) set(key, code string, ttl time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.codes[key] = &codeEntry{code: code, expiresAt: time.Now().Add(ttl)}
}

func (f *fallbackStore) get(key string) (string, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	e, ok := f.codes[key]
	if !ok || time.Now().After(e.expiresAt) {
		return "", false
	}
	return e.code, true
}

func (f *fallbackStore) del(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.codes, key)
}

func (f *fallbackStore) cleanup() {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for k, e := range f.codes {
		if now.After(e.expiresAt) {
			delete(f.codes, k)
		}
	}
}

func NewCodeStore(addr string, password string, db int) *CodeStore {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	s := &CodeStore{
		client:   rdb,
		ttl:      5 * time.Minute,
		fallback: newFallbackStore(),
	}

	go s.cleanupLoop()

	return s
}

func (s *CodeStore) Client() *redis.Client {
	return s.client
}

func (s *CodeStore) GenerateCode(email string) (string, error) {
	// 使用 crypto/rand 生成验证码，避免可预测性
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", fmt.Errorf("generate code: %w", err)
	}
	code := fmt.Sprintf("%06d", n.Int64())
	key := s.key(email)

	err = s.client.Set(context.Background(), key, code, s.ttl).Err()
	if err != nil {
		s.fallback.set(key, code, s.ttl)
	}
	return code, nil
}

func (s *CodeStore) VerifyCode(email string, code string) (bool, error) {
	key := s.key(email)
	attemptKey := s.attemptKey(email)

	attempts, _ := s.client.Get(context.Background(), attemptKey).Int()
	if attempts >= 10 {
		s.client.Expire(context.Background(), attemptKey, 5*time.Minute)
		return false, fmt.Errorf("too many attempts, please request a new code")
	}

	if fbAttempts, ok := s.fallback.get(attemptKey); ok {
		if n, err := strconv.Atoi(fbAttempts); err == nil && n >= 10 {
			return false, fmt.Errorf("too many attempts, please request a new code")
		}
	}

	stored, err := s.client.Get(context.Background(), key).Result()
	if err == nil {
		if stored != code {
			s.client.Incr(context.Background(), attemptKey)
			s.client.Expire(context.Background(), attemptKey, 5*time.Minute)
			return false, nil
		}
		s.client.Del(context.Background(), key)
		s.client.Del(context.Background(), attemptKey)
		return true, nil
	}

	if err == redis.Nil {
		if fb, ok := s.fallback.get(key); ok {
			if fb != code {
				s.incrementFallbackAttempt(attemptKey)
				return false, nil
			}
			s.fallback.del(key)
			s.fallback.del(attemptKey)
			return true, nil
		}
		return false, nil
	}

	if fb, ok := s.fallback.get(key); ok {
		if fb != code {
			s.incrementFallbackAttempt(attemptKey)
			return false, nil
		}
		s.fallback.del(key)
		s.fallback.del(attemptKey)
		return true, nil
	}

	return false, err
}

func (s *CodeStore) incrementFallbackAttempt(attemptKey string) {
	var count int
	if v, ok := s.fallback.get(attemptKey); ok {
		count, _ = strconv.Atoi(v)
	}
	count++
	s.fallback.set(attemptKey, strconv.Itoa(count), 5*time.Minute)
}

func (s *CodeStore) attemptKey(email string) string {
	return fmt.Sprintf("attempt:%s", email)
}

func (s *CodeStore) key(email string) string {
	return fmt.Sprintf("verify:%s", email)
}

func (s *CodeStore) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.fallback.cleanup()
	}
}

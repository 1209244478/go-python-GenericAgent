package auth

import (
	"context"
	"fmt"
	"math/rand"
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
	code := fmt.Sprintf("%06d", rand.Intn(1000000))
	key := s.key(email)

	err := s.client.Set(context.Background(), key, code, s.ttl).Err()
	if err != nil {
		s.fallback.set(key, code, s.ttl)
	}
	return code, nil
}

func (s *CodeStore) VerifyCode(email string, code string) (bool, error) {
	key := s.key(email)

	stored, err := s.client.Get(context.Background(), key).Result()
	if err == nil {
		if stored != code {
			return false, nil
		}
		s.client.Del(context.Background(), key)
		return true, nil
	}

	if err == redis.Nil {
		if fb, ok := s.fallback.get(key); ok {
			if fb != code {
				return false, nil
			}
			s.fallback.del(key)
			return true, nil
		}
		return false, nil
	}

	if fb, ok := s.fallback.get(key); ok {
		if fb != code {
			return false, nil
		}
		s.fallback.del(key)
		return true, nil
	}

	return false, err
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

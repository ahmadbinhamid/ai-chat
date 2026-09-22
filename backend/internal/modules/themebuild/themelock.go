package themebuild

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Cross-replica critical-section guard (in-process mutexes don't serialize between replicas).
type themeLocker interface {
	Lock(ctx context.Context, key string) (unlock func(), err error)
}

// NEVER pass bare slug to Lock; slugs collide across tenants, serializing unrelated ops.
func themeLockKey(tenantID uint64, themeSlug string) string {
	return fmt.Sprintf("%d:%s", tenantID, themeSlug)
}

// Balance: allows disk writes, frees quickly if pod killed mid-section.
const themeLockTTL = 30 * time.Second

// Bounds retries; fail rather than queue indefinitely behind stuck holder.
const themeLockAcquireTimeout = 10 * time.Second

// Poll interval while waiting for lock to free.
const themeLockRetryInterval = 100 * time.Millisecond

// Lua atomic check: bare DEL would delete new holder's lock after TTL expires.
var releaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
else
	return 0
end
`)

// Uses Redis SET NX PX + Lua (no Redlock: single Redis instance, quorum unneeded).
type redisThemeLock struct {
	rdb *redis.Client
}

func newRedisThemeLock(rdb *redis.Client) *redisThemeLock {
	return &redisThemeLock{rdb: rdb}
}

func (l *redisThemeLock) Lock(ctx context.Context, key string) (func(), error) {
	redisKey := "lock:theme:" + key
	token := uuid.NewString()

	deadline := time.Now().Add(themeLockAcquireTimeout)
	for {
		ok, err := l.rdb.SetNX(ctx, redisKey, token, themeLockTTL).Result()
		if err != nil {
			return nil, fmt.Errorf("acquire theme lock %q: %w", key, err)
		}
		if ok {
			return func() { l.release(redisKey, key, token) }, nil
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("acquire theme lock %q: timed out after %s", key, themeLockAcquireTimeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(themeLockRetryInterval):
		}
	}
}

// release uses its own context, deliberately detached from the caller's (which may already be
// canceled by the time a deferred unlock runs). Failure is logged, not returned: worst case is TTL expiry.
func (l *redisThemeLock) release(redisKey, key, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), themeLockAcquireTimeout)
	defer cancel()
	if err := releaseScript.Run(ctx, l.rdb, []string{redisKey}, token).Err(); err != nil {
		slog.Warn("failed to release theme lock", "key", key, "error", err)
	}
}

// keyedMutex is the in-process themeLocker fallback: one lazily-created *sync.Mutex per key.
// Safe here only because theme slug is naturally bounded per tenant; an unbounded key space needs stripedMutex instead.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newKeyedMutex() *keyedMutex {
	return &keyedMutex{locks: make(map[string]*sync.Mutex)}
}

func (k *keyedMutex) Lock(_ context.Context, key string) (func(), error) {
	k.mu.Lock()
	lock, ok := k.locks[key]
	if !ok {
		lock = &sync.Mutex{}
		k.locks[key] = lock
	}
	k.mu.Unlock()

	lock.Lock()
	return lock.Unlock, nil
}

// stripedMutex is a fixed-size alternative to keyedMutex for an unbounded key space (e.g. chat
// ID): hashes into a fixed stripe count. A stripe collision stalls the other key for the full hold time — not negligible; size accordingly.
type stripedMutex struct {
	stripes []sync.Mutex
}

func newStripedMutex(n int) *stripedMutex {
	return &stripedMutex{stripes: make([]sync.Mutex, n)}
}

func (s *stripedMutex) Lock(_ context.Context, key string) (func(), error) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	stripe := &s.stripes[h.Sum32()%uint32(len(s.stripes))]
	stripe.Lock()
	return stripe.Unlock, nil
}

package themebuild

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sync"
	"time"

	"ai-chat/internal/safego"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Cross-replica critical-section guard (in-process mutexes don't serialize between replicas). Work inside the lock must
// use lockCtx: it is cancelled if the lock is lost, so the section stops before writing more.
type themeLocker interface {
	Lock(ctx context.Context, key string) (lockCtx context.Context, unlock func(), err error)
}

// errThemeLockLost is lockCtx's cause when renewal finds another holder (or none) owns the key.
var errThemeLockLost = errors.New("theme lock lost: another holder may now be writing")

// NEVER pass bare slug to Lock; slugs collide across tenants, serializing unrelated ops.
func themeLockKey(tenantID uint64, themeSlug string) string {
	return fmt.Sprintf("%d:%s", tenantID, themeSlug)
}

// Frees quickly if a pod dies mid-section; a live holder renews every themeLockTTL/3. A var so tests can shorten it.
var themeLockTTL = 30 * time.Second

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

// Extends the TTL only while the key still holds our token, so a renewal can never revive another holder's lock.
var renewScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("PEXPIRE", KEYS[1], ARGV[2])
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

func (l *redisThemeLock) Lock(ctx context.Context, key string) (context.Context, func(), error) {
	redisKey := "lock:theme:" + key
	token := uuid.NewString()
	ttl := themeLockTTL

	deadline := time.Now().Add(themeLockAcquireTimeout)
	for {
		ok, err := l.rdb.SetNX(ctx, redisKey, token, ttl).Result()
		if err != nil {
			return nil, nil, fmt.Errorf("acquire theme lock %q: %w", key, err)
		}
		if ok {
			return l.hold(ctx, redisKey, key, token, ttl)
		}

		if time.Now().After(deadline) {
			return nil, nil, fmt.Errorf("acquire theme lock %q: timed out after %s", key, themeLockAcquireTimeout)
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(themeLockRetryInterval):
		}
	}
}

// hold starts renewing an acquired lock; unlock stops renewal before releasing, so no renewal can run after the DEL.
func (l *redisThemeLock) hold(ctx context.Context, redisKey, key, token string, ttl time.Duration) (context.Context, func(), error) {
	lockCtx, cancel := context.WithCancelCause(ctx)
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		defer safego.Recover("themebuild.renewThemeLock")
		l.renew(redisKey, key, token, ttl, stop, cancel)
	}()
	var once sync.Once
	unlock := func() {
		once.Do(func() {
			close(stop)
			<-stopped
			cancel(nil)
			l.release(redisKey, key, token)
		})
	}
	return lockCtx, unlock, nil
}

// renew extends the lock every ttl/3 until stop; a renewal that finds the token gone cancels lockCtx.
func (l *redisThemeLock) renew(redisKey, key, token string, ttl time.Duration, stop <-chan struct{}, cancel context.CancelCauseFunc) {
	tick := time.NewTicker(ttl / 3)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		ctx, done := context.WithTimeout(context.Background(), ttl/3)
		extended, err := renewScript.Run(ctx, l.rdb, []string{redisKey}, token, ttl.Milliseconds()).Int()
		done()
		if err != nil {
			// Transient: the TTL still covers us; a renewal that finds the key expired reports the loss.
			slog.Warn("failed to renew theme lock", "key", key, "error", err)
			continue
		}
		if extended == 0 {
			slog.Error("theme lock lost while held; stopping the critical section", "key", key)
			cancel(errThemeLockLost)
			return
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

// An in-process lock can't be lost, so the caller's ctx comes back unchanged.
func (k *keyedMutex) Lock(ctx context.Context, key string) (context.Context, func(), error) {
	k.mu.Lock()
	lock, ok := k.locks[key]
	if !ok {
		lock = &sync.Mutex{}
		k.locks[key] = lock
	}
	k.mu.Unlock()

	lock.Lock()
	return ctx, lock.Unlock, nil
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

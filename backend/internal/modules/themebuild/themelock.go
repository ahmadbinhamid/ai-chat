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

// themeLocker is the theme-slug/chat-id critical-section lock Service holds
// around buildWritePlan/commitWritePlan (see doGenerate) and
// revertAppliedHistory's write loop. Two implementations: redisThemeLock
// (cross-replica, used whenever REDIS_URL is configured — see NewService)
// and keyedMutex (in-process fallback, when it isn't). Distributed locking
// matters here specifically because this service runs as more than one
// replica behind a load balancer: two replicas each holding their own
// in-process *sync.Mutex do not serialize against each other, so replica A
// and replica B could both believe they alone are staging theme-slug
// "spring-sale" at once and race their writes.
type themeLocker interface {
	// Lock blocks (bounded — see themeLockAcquireTimeout) until key's lock
	// is held, then returns the func to release it. An error means the
	// lock could not be acquired within the bound, or ctx was canceled
	// first — callers must treat that like any other failure to complete
	// the operation, never enter the critical section anyway.
	Lock(ctx context.Context, key string) (unlock func(), err error)
}

// themeLockTTL bounds how long a Redis-held lock survives without being
// explicitly released. The critical sections it guards (buildWritePlan/
// commitWritePlan, revertAppliedHistory's write loop) are disk-facing HTTP
// calls to flowpos-backend, not model calls — seconds, not minutes — so
// 30s is comfortably longer than a normal run ever needs, meaning the TTL
// never expires out from under a live holder. It's also short enough that
// a pod killed mid-section (crash, OOM, deploy) frees that theme's lock
// for the next request well within a merchant's patience, instead of
// wedging it for the lifetime of a longer TTL chosen "to be safe."
const themeLockTTL = 30 * time.Second

// themeLockAcquireTimeout bounds how long Lock retries before giving up —
// a short bounded retry rather than blocking forever. If the lock is still
// held after this long, its holder is either doing something well outside
// the fast disk work this lock is meant to guard, or — far more likely —
// a crashed holder's TTL simply hasn't expired yet; either way the caller
// is better off failing the request than queuing indefinitely behind it.
const themeLockAcquireTimeout = 10 * time.Second

// themeLockRetryInterval is how often Lock polls Redis while waiting for a
// held lock to free up.
const themeLockRetryInterval = 100 * time.Millisecond

// releaseScript deletes a lock key only if its value still matches the
// token this holder set when it acquired it. A bare DEL would risk
// deleting a DIFFERENT holder's lock: if this holder's own critical
// section somehow ran long enough for the TTL to expire, Redis may have
// already freed the key and handed it to a new holder by the time release
// runs. The check-then-delete has to happen atomically in Lua so a third
// acquirer can't slip in between the GET and the DEL — the standard fix
// for this race (see the Redlock spec's own SET NX PX / Lua-DEL pattern),
// applied here without pulling in a library for it.
var releaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
else
	return 0
end
`)

// redisThemeLock is the cross-replica themeLocker, backed by Redis SET NX
// PX to acquire and releaseScript to release. Hand-rolled rather than
// github.com/go-redsync/redsync: redsync implements the full Redlock
// algorithm (acquiring a quorum across several independent Redis masters),
// which solves a problem this service doesn't have — it already depends on
// exactly one Redis instance (see config.Config.RedisURL / NewRedisClient),
// so redsync's main value goes entirely unused here. SET NX PX plus a Lua
// compare-and-delete is the documented minimal-correct pattern for a
// single-instance lock, and needs no new dependency: this package already
// imports github.com/redis/go-redis/v9 for the event bus (see eventbus.go).
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

// release runs on its own short-lived context, deliberately detached from
// the caller's ctx (which may already be canceled/timed out by the time a
// deferred unlock runs) — a release that never gets attempted because the
// request context is already done would leave the key held until TTL
// expiry for no reason. Failure here is logged, not returned: the worst
// case is exactly that TTL-expiry fallback, never a permanently stuck lock.
func (l *redisThemeLock) release(redisKey, key, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), themeLockAcquireTimeout)
	defer cancel()
	if err := releaseScript.Run(ctx, l.rdb, []string{redisKey}, token).Err(); err != nil {
		slog.Warn("failed to release theme lock", "key", key, "error", err)
	}
}

// keyedMutex is the in-process themeLocker fallback used when REDIS_URL
// isn't set (see NewService) — hands out one *sync.Mutex per key, created
// lazily. The map grows by one entry per distinct key ever seen by this
// process, not per request — bounded by how many themes actually exist
// (this is only ever keyed by theme slug), unlike a per-token cache, so no
// sweep/eviction is needed here. A key space that ISN'T naturally bounded
// this way (e.g. one that grows with every chat ever created) needs
// stripedMutex below instead, not this — see its own doc comment. ctx is
// accepted only to satisfy themeLocker; a plain mutex has no way to respect
// cancellation while blocked, so it's ignored, matching this fallback's
// existing single-replica-only limitation (see eventBus's own in-process
// fallback for the same tradeoff).
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

// stripedMutex is a fixed-size alternative to keyedMutex for a lock whose
// key space is NOT bounded the way keyedMutex's own doc comment describes
// — historySummaryLocks (see history_summary.go) is keyed by chat ID, which
// grows for the life of a long-running process with no natural ceiling, so
// reusing keyedMutex there grew one permanent *sync.Mutex entry per chat
// that ever crossed summarizeHistoryThreshold, never freed. Hashing the key
// into one of a small, fixed number of stripes keeps memory O(stripe count)
// forever instead, at the cost of two DIFFERENT keys occasionally landing
// on the same stripe and serializing against each other unnecessarily — an
// acceptable tradeoff here: this lock exists only to stop two concurrent
// calls for the SAME chat from both paying for the same Summarize call, so
// an occasional false-contention stall on some unrelated chat costs a few
// extra ms once in a while, never a correctness problem.
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

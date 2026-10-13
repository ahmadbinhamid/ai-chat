package themebuild

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// shortLockTTL shrinks themeLockTTL for one test so renewal and loss happen within milliseconds.
func shortLockTTL(t *testing.T, ttl time.Duration) {
	t.Helper()
	prev := themeLockTTL
	themeLockTTL = ttl
	t.Cleanup(func() { themeLockTTL = prev })
}

func lockTestKey(t *testing.T, rdb *redis.Client) (key, redisKey string) {
	t.Helper()
	key = "test:" + uuid.NewString()
	redisKey = "lock:theme:" + key
	t.Cleanup(func() { _ = rdb.Del(context.Background(), redisKey).Err() })
	return key, redisKey
}

func TestRedisThemeLock_RenewalKeepsLockPastTTL(t *testing.T) {
	rdb := openTestRedis(t)
	shortLockTTL(t, 300*time.Millisecond)
	key, redisKey := lockTestKey(t, rdb)
	ctx := context.Background()

	lockCtx, unlock, err := newRedisThemeLock(rdb).Lock(ctx, key)
	if err != nil {
		t.Fatalf("Lock failed: %v", err)
	}
	defer unlock()

	time.Sleep(3 * themeLockTTL)
	if lockCtx.Err() != nil {
		t.Fatalf("lockCtx cancelled while the lock was held and renewed: %v", context.Cause(lockCtx))
	}
	if ok, err := rdb.SetNX(ctx, redisKey, "intruder", time.Minute).Result(); err != nil || ok {
		t.Fatalf("another holder acquired the key after %s (SetNX ok=%v, err=%v); renewal didn't keep it", 3*themeLockTTL, ok, err)
	}

	unlock()
	if n, _ := rdb.Exists(ctx, redisKey).Result(); n != 0 {
		t.Fatal("unlock left the key in place")
	}
}

func TestRedisThemeLock_LostLockCancelsContext(t *testing.T) {
	tests := []struct {
		name string
		lose func(ctx context.Context, rdb *redis.Client, redisKey string) error
		// wantKept is what's in the key after our unlock; "" means absent.
		wantKept string
	}{
		{"taken over by another holder", func(ctx context.Context, rdb *redis.Client, k string) error {
			return rdb.Set(ctx, k, "other-holder", time.Minute).Err()
		}, "other-holder"},
		{"expired", func(ctx context.Context, rdb *redis.Client, k string) error {
			return rdb.Del(ctx, k).Err()
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rdb := openTestRedis(t)
			shortLockTTL(t, 300*time.Millisecond)
			key, redisKey := lockTestKey(t, rdb)
			ctx := context.Background()

			lockCtx, unlock, err := newRedisThemeLock(rdb).Lock(ctx, key)
			if err != nil {
				t.Fatalf("Lock failed: %v", err)
			}
			if err := tt.lose(ctx, rdb, redisKey); err != nil {
				t.Fatalf("simulate loss: %v", err)
			}

			select {
			case <-lockCtx.Done():
			case <-time.After(2 * themeLockTTL):
				t.Fatal("lockCtx was not cancelled after the lock was lost")
			}
			if cause := context.Cause(lockCtx); !errors.Is(cause, errThemeLockLost) {
				t.Fatalf("lockCtx cause = %v, want errThemeLockLost", cause)
			}

			unlock()
			got, err := rdb.Get(ctx, redisKey).Result()
			if errors.Is(err, redis.Nil) {
				got = ""
			}
			if got != tt.wantKept {
				t.Fatalf("after our unlock the key holds %q, want %q", got, tt.wantKept)
			}
		})
	}
}

func TestRedisThemeLock_ReleaseNeverDeletesAnotherHoldersLock(t *testing.T) {
	rdb := openTestRedis(t)
	key, redisKey := lockTestKey(t, rdb)
	ctx := context.Background()

	_, unlock, err := newRedisThemeLock(rdb).Lock(ctx, key)
	if err != nil {
		t.Fatalf("Lock failed: %v", err)
	}
	// Our TTL lapsed and someone else took the key before our deferred unlock ran.
	if err := rdb.Set(ctx, redisKey, "next-holder", time.Minute).Err(); err != nil {
		t.Fatalf("set: %v", err)
	}
	unlock()

	if got, err := rdb.Get(ctx, redisKey).Result(); err != nil || got != "next-holder" {
		t.Fatalf("unlock touched another holder's lock: got %q (err %v)", got, err)
	}
}

func TestRedisThemeLock_UnlockStopsRenewal(t *testing.T) {
	rdb := openTestRedis(t)
	shortLockTTL(t, 300*time.Millisecond)
	key, redisKey := lockTestKey(t, rdb)
	ctx := context.Background()

	lockCtx, unlock, err := newRedisThemeLock(rdb).Lock(ctx, key)
	if err != nil {
		t.Fatalf("Lock failed: %v", err)
	}
	unlock()
	if lockCtx.Err() == nil {
		t.Fatal("unlock should end lockCtx")
	}
	if errors.Is(context.Cause(lockCtx), errThemeLockLost) {
		t.Fatal("a normal unlock must not report the lock as lost")
	}

	// A new holder's short TTL must run out on its own: nothing of ours may still be extending it.
	if err := rdb.Set(ctx, redisKey, "next-holder", 200*time.Millisecond).Err(); err != nil {
		t.Fatalf("set: %v", err)
	}
	time.Sleep(2 * themeLockTTL)
	if n, _ := rdb.Exists(ctx, redisKey).Result(); n != 0 {
		t.Fatal("the next holder's key outlived its TTL; renewal kept running after unlock")
	}
}

func TestRedisThemeLock_SerializesHolders(t *testing.T) {
	rdb := openTestRedis(t)
	key, _ := lockTestKey(t, rdb)
	ctx := context.Background()
	locks := newRedisThemeLock(rdb)

	_, unlockA, err := locks.Lock(ctx, key)
	if err != nil {
		t.Fatalf("first Lock failed: %v", err)
	}
	acquired := make(chan struct{})
	go func() {
		_, unlockB, err := locks.Lock(ctx, key)
		if err == nil {
			close(acquired)
			unlockB()
		}
	}()
	select {
	case <-acquired:
		t.Fatal("a second holder acquired the lock while the first held it")
	case <-time.After(300 * time.Millisecond):
	}
	unlockA()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("the second holder never acquired the lock after release")
	}
}

func TestKeyedMutex_ReturnsCallerContext(t *testing.T) {
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "caller")
	lockCtx, unlock, err := newKeyedMutex().Lock(ctx, "1:shop")
	if err != nil {
		t.Fatalf("Lock failed: %v", err)
	}
	defer unlock()
	if lockCtx != ctx {
		t.Fatal("keyedMutex must hand back the caller's ctx unchanged")
	}
}

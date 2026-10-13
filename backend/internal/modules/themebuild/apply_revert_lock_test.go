package themebuild

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"ai-chat/internal/modules/chat"
	"ai-chat/internal/themefs"

	"github.com/google/uuid"
)

// hookLocker records each key it's asked for and runs onLock as the lock is granted, standing in for whatever
// another holder finished just before this caller got the lock.
type hookLocker struct {
	mu     sync.Mutex
	keys   []string
	onLock func(key string)
}

func (h *hookLocker) Lock(ctx context.Context, key string) (context.Context, func(), error) {
	h.mu.Lock()
	h.keys = append(h.keys, key)
	h.mu.Unlock()
	if h.onLock != nil {
		h.onLock(key)
	}
	return ctx, func() {}, nil
}

// seedPendingTurn gives a fresh tenant's chat one finished generation on themeSlug and one pending draft turn.
func seedPendingTurn(t *testing.T, svc *Service, chatSvc *chat.Service, themeSlug string) (chat.Chat, chat.Message) {
	t.Helper()
	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())
	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	genID := uuid.NewString()
	if _, err := svc.repo.EnqueueGeneration(ctx, Generation{ID: genID, ChatID: c.ID, TenantID: tenantID, Prompt: "p", ThemeSlug: themeSlug}); err != nil {
		t.Fatalf("EnqueueGeneration failed: %v", err)
	}
	if _, err := svc.repo.DequeueNext(ctx, c.ID); err != nil {
		t.Fatalf("DequeueNext failed: %v", err)
	}
	if err := svc.repo.EndGeneration(ctx, c.ID, genID, nil); err != nil {
		t.Fatalf("EndGeneration failed: %v", err)
	}

	tx, err := svc.repo.BeginTx(ctx)
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	msg, err := chatSvc.RecordAssistantMessageInTx(ctx, tx, c, "Made the header dark.", chat.MessageStatusCompleted, 0, 0, chat.ApplyStatusPending, "", "", nil)
	if err != nil {
		t.Fatalf("RecordAssistantMessageInTx failed: %v", err)
	}
	now := time.Now().UTC()
	if err := svc.repo.CreateFileTx(ctx, tx, GeneratedFile{
		ID: uuid.NewString(), MessageID: msg.ID, ChatID: c.ID, FilePath: "components/css/header.css",
		Action: FileActionUpdate, Kind: GeneratedFileKindProposed, Language: "css", Content: "header{}", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateFileTx failed: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return c, msg
}

func newLockTestService(t *testing.T, locks themeLocker) (*Service, *chat.Service) {
	t.Helper()
	conn := openTestDB(t)
	chatSvc := chat.NewService(chat.NewRepository(conn))
	ts := newFakeThemeServer(t, map[string]string{})
	t.Cleanup(ts.Close)
	svc := NewService(NewRepository(conn), chatSvc, nil, themefs.NewStore(ts.URL), nil)
	svc.themeLocks = locks
	return svc, chatSvc
}

// Apply must decide what to write only once it holds the lock: a revert that discarded the draft just before must
// leave this apply with nothing to write, not push the discarded turns live.
func TestApplyDraft_ReadsPendingStateUnderTheLock(t *testing.T) {
	locks := &hookLocker{}
	svc, chatSvc := newLockTestService(t, locks)
	c, _ := seedPendingTurn(t, svc, chatSvc, "shop")
	locks.onLock = func(string) {
		if err := svc.repo.MarkMessagesDiscarded(context.Background(), c.ID); err != nil {
			t.Errorf("simulate the racing revert: %v", err)
		}
	}

	_, err := svc.ApplyDraft(context.Background(), c.TenantID, "t", c.ID, "shop")
	if !errors.Is(err, ErrNoPendingChanges) {
		t.Fatalf("ApplyDraft after a revert won the lock = %v, want ErrNoPendingChanges (nothing left to write)", err)
	}
	if len(locks.keys) != 1 || locks.keys[0] != themeLockKey(c.TenantID, "shop") {
		t.Fatalf("apply locked %v, want [%s]", locks.keys, themeLockKey(c.TenantID, "shop"))
	}
}

func TestRevertToMessage_TakesTheApplyLockForTheChatsTheme(t *testing.T) {
	locks := &hookLocker{}
	svc, chatSvc := newLockTestService(t, locks)
	c, msg := seedPendingTurn(t, svc, chatSvc, "shop")

	if _, err := svc.RevertToMessage(context.Background(), c.TenantID, "t", c.ID, msg.ID); err != nil {
		t.Fatalf("RevertToMessage failed: %v", err)
	}
	if want := themeLockKey(c.TenantID, "shop"); len(locks.keys) != 1 || locks.keys[0] != want {
		t.Fatalf("revert locked %v, want [%s] — the same key ApplyDraft takes", locks.keys, want)
	}
}

// The target's pending/applied status decides which revert runs, so it must be read after an in-flight apply finished.
func TestRevertToMessage_ReadsTargetUnderTheLock(t *testing.T) {
	locks := &hookLocker{}
	svc, chatSvc := newLockTestService(t, locks)
	c, msg := seedPendingTurn(t, svc, chatSvc, "shop")
	locks.onLock = func(string) {
		if err := svc.repo.MarkMessagesApplied(context.Background(), c.ID, time.Now().UTC()); err != nil {
			t.Errorf("simulate the racing apply: %v", err)
		}
	}

	if _, err := svc.RevertToMessage(context.Background(), c.TenantID, "t", c.ID, msg.ID); err != nil {
		t.Fatalf("RevertToMessage failed: %v", err)
	}
	got, err := chatSvc.GetMessage(context.Background(), c.ID, msg.ID)
	if err != nil {
		t.Fatalf("GetMessage failed: %v", err)
	}
	if got.ApplyStatus != chat.ApplyStatusApplied {
		t.Fatalf("target status = %q: the revert used the stale pending status and discarded an applied turn", got.ApplyStatus)
	}
}

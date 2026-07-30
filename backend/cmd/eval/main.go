// Command eval drives the real AI theme-builder pipeline —
// themebuild.Service.Generate, flowpos-backend, and Claude — against the
// fixed task list in internal/evals, the same way cmd/server does for a real
// request, just without the HTTP/gin layer. It requires a real, already
// logged-in tenant user's bearer token (EVAL_BEARER_TOKEN) and tenant ID
// (EVAL_TENANT_ID): this service has no service-to-service auth of its own
// (see internal/auth's package doc comment) and never will just for this
// tool, so a human obtaining a token via a real login is the only way in.
//
// Every task runs against the same persistent per-tenant "builder" chat (see
// themebuild.Service.Generate) — there is no per-task chat reset — but each
// task's GenerationMode restriction (if any) is set explicitly via
// evals.Task.Mode, not inferred from turn count, so this is safe to re-run
// against the same EVAL_TENANT_ID repeatedly.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/config"
	"ai-chat/internal/db"
	"ai-chat/internal/evals"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/modules/themebuild"

	"ai-chat/internal/themefs"

	"github.com/joho/godotenv"
)

// pollInterval/pollTimeout bound how long eval waits for one async
// Generate call to finish — generous enough for a slow xhigh-effort
// generation (see cmd/server/main.go's writeTimeout comment), not unbounded.
const (
	pollInterval = 2 * time.Second
	pollTimeout  = 5 * time.Minute
)

type taskResult struct {
	task   evals.Task
	passed bool
	detail string
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Printf("no .env file loaded: %v", err)
	}
	cfg := config.Load()

	token := os.Getenv("EVAL_BEARER_TOKEN")
	if token == "" {
		log.Fatal("EVAL_BEARER_TOKEN is required — log in as a real test tenant user and paste their bearer token; " +
			"this service has no service-to-service auth of its own")
	}
	tenantID, err := strconv.ParseUint(os.Getenv("EVAL_TENANT_ID"), 10, 64)
	if err != nil {
		log.Fatalf("EVAL_TENANT_ID must be a valid tenant id: %v", err)
	}

	conn, err := db.Connect(cfg)
	if err != nil {
		log.Fatalf("database connection failed: %v", err)
	}
	defer conn.Close()

	generator, err := ai.New(cfg.AnthropicAPIKey, cfg.AnthropicModel, cfg.AnthropicEffort)
	if err != nil {
		log.Fatalf("ai.New failed: %v", err)
	}
	store := themefs.NewStore(cfg.FlowposAPIBase)

	rdb, err := themebuild.NewRedisClient(cfg.RedisURL)
	if err != nil {
		log.Fatalf("redis client failed: %v", err)
	}

	chatRepo := chat.NewRepository(conn)
	chatSvc := chat.NewService(chatRepo)

	buildRepo := themebuild.NewRepository(conn)
	buildSvc := themebuild.NewService(buildRepo, chatSvc, generator, store, rdb)

	ctx := context.Background()

	results := make([]taskResult, 0, len(evals.Tasks))
	for _, task := range evals.Tasks {
		res := runTask(ctx, buildSvc, chatSvc, tenantID, token, task)
		results = append(results, res)

		status := "FAIL"
		if res.passed {
			status = "PASS"
		}
		fmt.Printf("%s  %-32s  %s\n", status, task.ID, res.detail)
	}

	passed := 0
	for _, r := range results {
		if r.passed {
			passed++
		}
	}
	total := len(results)
	pct := 100 * float64(passed) / float64(total)
	fmt.Printf("\n%d / %d passed (%.1f%%)\n", passed, total, pct)

	if pct < 90 {
		log.Fatalf("pass rate %.1f%% below the 90%% threshold", pct)
	}
}

// runTask creates a fresh test theme, sends the task's prompt, waits for the
// background generation to finish, and checks whether files were actually
// written to this specific turn against task.ExpectedOK.
func runTask(ctx context.Context, buildSvc *themebuild.Service, chatSvc *chat.Service, tenantID uint64, token string, task evals.Task) taskResult {
	slug, err := buildSvc.CreateThemeFromBase(ctx, tenantID, token)
	if err != nil {
		return taskResult{task: task, passed: false, detail: fmt.Sprintf("create theme: %v", err)}
	}

	outcome, err := buildSvc.Generate(ctx, themebuild.GenerateInput{
		TenantID:  tenantID,
		UserName:  "eval",
		Token:     token,
		ThemeSlug: slug,
		Prompt:    task.Prompt,
		Mode:      task.Mode,
	})
	if err != nil {
		return taskResult{task: task, passed: false, detail: fmt.Sprintf("generate: %v", err)}
	}

	genErr, err := waitForGeneration(ctx, buildSvc, outcome.Chat.ID)
	if err != nil {
		return taskResult{task: task, passed: false, detail: fmt.Sprintf("poll generation: %v", err)}
	}

	filesWritten, err := filesWrittenThisTurn(ctx, buildSvc, chatSvc, tenantID, outcome.Chat.ID)
	if err != nil {
		return taskResult{task: task, passed: false, detail: fmt.Sprintf("check result: %v", err)}
	}

	actualOK := genErr == "" && filesWritten
	passed := actualOK == task.ExpectedOK

	detail := fmt.Sprintf("theme=%s files_written=%v", slug, filesWritten)
	if genErr != "" {
		detail += fmt.Sprintf(" generation_error=%q", genErr)
	}
	return taskResult{task: task, passed: passed, detail: detail}
}

// waitForGeneration polls Service.GenerationStatus until the background
// Generate call for chatID finishes, returning its error message (empty on
// success).
func waitForGeneration(ctx context.Context, buildSvc *themebuild.Service, chatID string) (string, error) {
	deadline := time.Now().Add(pollTimeout)
	for {
		generating, errMsg := buildSvc.GenerationStatus(ctx, chatID)
		if !generating {
			return errMsg, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out after %s waiting for generation to finish", pollTimeout)
		}
		time.Sleep(pollInterval)
	}
}

// filesWrittenThisTurn reports whether the chat's most recent message is an
// assistant reply that wrote at least one file — i.e. this specific turn's
// outcome, not the theme's cumulative file history.
func filesWrittenThisTurn(ctx context.Context, buildSvc *themebuild.Service, chatSvc *chat.Service, tenantID uint64, chatID string) (bool, error) {
	messages, err := chatSvc.ListMessages(ctx, tenantID, chatID)
	if err != nil {
		return false, fmt.Errorf("list messages: %w", err)
	}
	if len(messages) == 0 {
		return false, nil
	}
	last := messages[len(messages)-1]
	if last.Role != chat.RoleAssistant {
		// The generation failed before an assistant reply was ever recorded.
		return false, nil
	}

	files, err := buildSvc.FilesForChat(ctx, chatID)
	if err != nil {
		return false, fmt.Errorf("files for chat: %w", err)
	}
	for _, f := range files {
		if f.MessageID == last.ID {
			return true, nil
		}
	}
	return false, nil
}

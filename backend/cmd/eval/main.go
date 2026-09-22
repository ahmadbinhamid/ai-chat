// Command eval runs the real theme-builder pipeline against internal/evals' task list,
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

// pollInterval/pollTimeout bound how long eval waits for one async Generate call to finish.
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
	// The AI builder never creates a theme, only edits an already-installed one, so
	// a human must install/activate a real theme for EVAL_TENANT_ID first.
	themeSlug := os.Getenv("EVAL_THEME_SLUG")
	if themeSlug == "" {
		log.Fatal("EVAL_THEME_SLUG is required — install/activate a real theme for EVAL_TENANT_ID first " +
			"(the AI theme builder never creates one itself), then set this to its slug")
	}

	conn, err := db.Connect(cfg)
	if err != nil {
		log.Fatalf("database connection failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	streamTimeouts := ai.StreamTimeouts{
		Idle:            cfg.StreamIdleTimeout,
		FirstTokenEdit:  cfg.FirstTokenTimeoutEdit,
		FirstTokenBrand: cfg.FirstTokenTimeoutBrand,
		FirstTokenCopy:  cfg.FirstTokenTimeoutCopy,
		FirstTokenPages: cfg.FirstTokenTimeoutPages,
	}

	generator, err := ai.New(cfg.APIKey, cfg.BaseURL, cfg.Model, cfg.Effort, cfg.VisionModel, cfg.MaxTokens, streamTimeouts)
	if err != nil {
		// Process exit reclaims conn's fd regardless of the deferred Close.
		log.Fatalf("ai.New failed: %v", err) //nolint:gocritic // process exit reclaims conn's fd either way
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
		res := runTask(ctx, buildSvc, chatSvc, tenantID, token, themeSlug, task)
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

// runTask sends the task's prompt, waits for generation to finish, and checks
// whether files were written this turn against task.ExpectedOK.
func runTask(ctx context.Context, buildSvc *themebuild.Service, chatSvc *chat.Service, tenantID uint64, token, themeSlug string, task evals.Task) taskResult {
	outcome, err := buildSvc.Generate(ctx, themebuild.GenerateInput{
		TenantID:  tenantID,
		UserName:  "eval",
		Token:     token,
		ThemeSlug: themeSlug,
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

	detail := fmt.Sprintf("theme=%s files_written=%v", themeSlug, filesWritten)
	if genErr != "" {
		detail += fmt.Sprintf(" generation_error=%q", genErr)
	}
	return taskResult{task: task, passed: passed, detail: detail}
}

// waitForGeneration polls until the background Generate call for chatID finishes,
// returning its error message (empty on success).
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

// filesWrittenThisTurn reports whether the chat's last message wrote a file this turn.
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

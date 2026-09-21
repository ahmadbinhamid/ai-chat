package themebuild

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/builderplan"
)

// activeBuilderTarget is bounded chat-scoped state for follow-up resolution
// (e.g. "now it is not working" → last blog page). Process-local only.
type activeBuilderTarget struct {
	Type      string // "page"
	Identity  string // slug
	Path      string // pages/blog.liquid
	Operation string // last op kind
	UpdatedAt time.Time
}

type activeTargetCache struct {
	mu      sync.Mutex
	byChat  map[string]activeBuilderTarget
	maxSize int
}

func newActiveTargetCache() *activeTargetCache {
	return &activeTargetCache{
		byChat:  make(map[string]activeBuilderTarget, 64),
		maxSize: 512,
	}
}

func (c *activeTargetCache) get(tenantID uint64, chatID string) (activeBuilderTarget, bool) {
	if c == nil || chatID == "" {
		return activeBuilderTarget{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.byChat[fmt.Sprintf("%d:%s", tenantID, chatID)]
	return t, ok
}

func (c *activeTargetCache) put(tenantID uint64, chatID string, t activeBuilderTarget) {
	if c == nil || chatID == "" || t.Identity == "" {
		return
	}
	t.UpdatedAt = time.Now()
	if t.Type == "" {
		t.Type = "page"
	}
	key := fmt.Sprintf("%d:%s", tenantID, chatID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.byChat) >= c.maxSize {
		// Drop one arbitrary oldest-ish entry (bounded; not LRU-perfect).
		var drop string
		var oldest time.Time
		first := true
		for k, v := range c.byChat {
			if first || v.UpdatedAt.Before(oldest) {
				drop, oldest, first = k, v.UpdatedAt, false
			}
		}
		if drop != "" {
			delete(c.byChat, drop)
		}
	}
	c.byChat[key] = t
}

func resolveTroubleshootSlug(prompt string, plan builderplan.BuilderPlan, active activeBuilderTarget) string {
	if s := builderplanTroubleshootSlug(prompt, plan); s != "" {
		return s
	}
	if active.Identity != "" {
		return active.Identity
	}
	return ""
}

func builderplanTroubleshootSlug(prompt string, plan builderplan.BuilderPlan) string {
	for _, op := range plan.Operations {
		if op.Kind == builderplan.OpDiagnoseExistingPage || op.Kind == builderplan.OpFixExistingPage {
			if id := pageIDFromLiquidPath(op.Target); id != "" {
				return id
			}
		}
	}
	for _, t := range plan.Targets {
		if id := pageIDFromLiquidPath(t); id != "" {
			return id
		}
	}
	p := strings.ToLower(prompt)
	switch {
	case strings.Contains(p, "blog"):
		return "blog"
	case strings.Contains(p, "pricing"):
		return "pricing"
	case strings.Contains(p, "about"):
		return "about-us"
	case strings.Contains(p, "contact"):
		return "contact-us"
	case strings.Contains(p, "faq"):
		return "faq"
	case strings.Contains(p, "home"):
		return "home"
	}
	return ""
}

func activeTargetFromResult(result *ai.Result, plannedOp string) (activeBuilderTarget, bool) {
	if result == nil {
		return activeBuilderTarget{}, false
	}
	if result.PageRegistryEntry != nil {
		slug := strings.TrimSpace(result.PageRegistryEntry.Page)
		if slug == "" {
			slug = strings.TrimSpace(result.PageRegistryEntry.Slug)
		}
		if slug != "" {
			return activeBuilderTarget{
				Type: "page", Identity: slug, Path: "pages/" + slug + ".liquid", Operation: plannedOp,
			}, true
		}
	}
	for _, f := range result.Files {
		id := pageIDFromLiquidPath(f.Path)
		if id == "" || id == "home" {
			continue
		}
		act := strings.ToLower(strings.TrimSpace(f.Action))
		if act == "create" || act == "update" || act == "edit" {
			return activeBuilderTarget{
				Type: "page", Identity: id, Path: "pages/" + id + ".liquid", Operation: plannedOp,
			}, true
		}
	}
	return activeBuilderTarget{}, false
}

func enrichPlanTroubleshootTarget(plan *builderplan.BuilderPlan, slug string) {
	if plan == nil || slug == "" {
		return
	}
	path := "pages/" + slug + ".liquid"
	for i := range plan.Operations {
		if plan.Operations[i].Kind == builderplan.OpDiagnoseExistingPage ||
			plan.Operations[i].Kind == builderplan.OpFixExistingPage {
			if plan.Operations[i].Target == "" {
				plan.Operations[i].Target = path
			}
		}
	}
	if len(plan.Targets) == 0 {
		plan.Targets = []string{path}
	}
	if len(plan.RequiredFiles) == 0 {
		plan.RequiredFiles = builderplan.SelectContextFiles(*plan)
	}
}

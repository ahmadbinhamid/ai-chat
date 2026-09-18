package themefs

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// FlowPOSCounters tracks theme-API HTTP calls for one generation via context.
// Bounded to the generation lifetime — never a global map keyed by tenant/chat.
type FlowPOSCounters struct {
	ListFiles  atomic.Int64
	ReadFile   atomic.Int64
	WriteFile  atomic.Int64
	DeleteFile atomic.Int64
	ElapsedMs  atomic.Int64
}

// Add records one completed theme API operation.
func (c *FlowPOSCounters) Add(op string, elapsedMs int64) {
	if c == nil {
		return
	}
	c.ElapsedMs.Add(elapsedMs)
	switch op {
	case "ListFiles":
		c.ListFiles.Add(1)
	case "ReadFile":
		c.ReadFile.Add(1)
	case "WriteFile":
		c.WriteFile.Add(1)
	case "DeleteFile":
		c.DeleteFile.Add(1)
	}
}

type flowPOSMetricsKey struct{}

// ContextWithFlowPOSCounters attaches generation-scoped FlowPOS counters to
// ctx. Store methods increment them when present.
func ContextWithFlowPOSCounters(ctx context.Context, c *FlowPOSCounters) context.Context {
	if c == nil {
		return ctx
	}
	return context.WithValue(ctx, flowPOSMetricsKey{}, c)
}

func flowPOSCountersFrom(ctx context.Context) *FlowPOSCounters {
	c, _ := ctx.Value(flowPOSMetricsKey{}).(*FlowPOSCounters)
	return c
}

// FlowPOSCountersFromContext returns generation-scoped FlowPOS counters when
// present (see ContextWithFlowPOSCounters). Used by themebuild tools to report
// actual underlying API calls rather than every ThemeStore method invocation
// (which may be satisfied by the generation-scoped CachingStore).
func FlowPOSCountersFromContext(ctx context.Context) *FlowPOSCounters {
	return flowPOSCountersFrom(ctx)
}

// logThemeAPIRequest emits performance telemetry for one FlowPOS theme call.
// Never logs Authorization, tokens, bodies, or file contents.
func logThemeAPIRequest(ctx context.Context, operation string, tenantID uint64, start time.Time, httpStatus int, err error) {
	elapsed := time.Since(start).Milliseconds()
	if c := flowPOSCountersFrom(ctx); c != nil {
		c.Add(operation, elapsed)
	}
	success := err == nil && (httpStatus == 0 || (httpStatus >= 200 && httpStatus < 300) || httpStatus == 404)
	if operation == "ReadFile" && httpStatus == 404 && err == nil {
		success = true
	}
	attrs := []any{
		"operation", operation,
		"elapsed_ms", elapsed,
		"success", success,
		"tenant_id", tenantID,
	}
	if httpStatus > 0 {
		attrs = append(attrs, "http_status", httpStatus)
	}
	if err != nil {
		attrs = append(attrs, "error", true)
	}
	slog.Info("themefs: api request", attrs...)
}

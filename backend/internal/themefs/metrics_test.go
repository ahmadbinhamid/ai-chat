package themefs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFlowPOSCounters_Add(t *testing.T) {
	t.Parallel()
	c := &FlowPOSCounters{}
	c.Add("ListFiles", 10)
	c.Add("ReadFile", 20)
	c.Add("ReadFile", 5)
	c.Add("WriteFile", 7)
	c.Add("DeleteFile", 3)
	if c.ListFiles.Load() != 1 || c.ReadFile.Load() != 2 || c.WriteFile.Load() != 1 || c.DeleteFile.Load() != 1 {
		t.Fatalf("counts list=%d read=%d write=%d delete=%d",
			c.ListFiles.Load(), c.ReadFile.Load(), c.WriteFile.Load(), c.DeleteFile.Load())
	}
	if c.ElapsedMs.Load() != 45 {
		t.Fatalf("elapsed=%d", c.ElapsedMs.Load())
	}
}

func TestLogThemeAPIRequest_NoSecretsInAttrs(t *testing.T) {
	t.Parallel()
	// Ensure helper never takes body/auth parameters — signature guard.
	var _ func(context.Context, string, uint64, time.Time, int, error) = logThemeAPIRequest
}

func TestStore_ListFiles_IncrementsCounters(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(strings.ToLower(r.Header.Get("Authorization")), "secret-token") {
			// Token is sent on the wire (required) but must not appear in our metrics logs.
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"files":[]}}`))
	}))
	defer ts.Close()

	store := NewStore(ts.URL)
	counters := &FlowPOSCounters{}
	ctx := ContextWithFlowPOSCounters(context.Background(), counters)
	_, err := store.ListFiles(ctx, RequestAuth{Token: "secret-token", TenantID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if counters.ListFiles.Load() != 1 {
		t.Fatalf("expected 1 ListFiles, got %d", counters.ListFiles.Load())
	}
}

func TestStore_ReadFile_404IsSuccessTelemetry(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	store := NewStore(ts.URL)
	counters := &FlowPOSCounters{}
	ctx := ContextWithFlowPOSCounters(context.Background(), counters)
	content, err := store.ReadFile(ctx, RequestAuth{Token: "t", TenantID: 1}, "pages/missing.liquid")
	if err != nil {
		t.Fatal(err)
	}
	if content != "" {
		t.Fatalf("want empty content, got %q", content)
	}
	if counters.ReadFile.Load() != 1 {
		t.Fatalf("expected 1 ReadFile, got %d", counters.ReadFile.Load())
	}
}

package stockimages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseInput(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantQuery string
		wantCount int
		wantErr   bool
	}{
		{"query and count", `{"query":"espresso cup","count":3}`, "espresso cup", 3, false},
		{"default count", `{"query":"coffee beans"}`, "coffee beans", defaultCount, false},
		{"max count", `{"query":"cafe interior","count":6}`, "cafe interior", 6, false},
		{"whitespace collapsed", `{"query":"  latte   art \n"}`, "latte art", defaultCount, false},
		{"count over max", `{"query":"coffee","count":7}`, "", 0, true},
		{"negative count", `{"query":"coffee","count":-1}`, "", 0, true},
		{"empty query", `{"query":"  "}`, "", 0, true},
		{"one character", `{"query":"a"}`, "", 0, true},
		{"too long", `{"query":"` + strings.Repeat("a", maxQueryLen+1) + `"}`, "", 0, true},
		{"a URL", `{"query":"https://evil.example/x.jpg"}`, "", 0, true},
		{"a bare host", `{"query":"www.example.com"}`, "", 0, true},
		{"not JSON", `{"query":`, "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseInput(json.RawMessage(tt.raw))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseInput(%s) err = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
			if err != nil {
				if !errors.Is(err, ErrInvalidInput) {
					t.Errorf("want ErrInvalidInput, got %v", err)
				}
				return
			}
			if got.Query != tt.wantQuery || got.Count != tt.wantCount {
				t.Errorf("ParseInput(%s) = %+v, want query %q count %d", tt.raw, got, tt.wantQuery, tt.wantCount)
			}
		})
	}
}

const pexelsBody = `{"photos":[
 {"width":4000,"height":2667,"url":"https://www.pexels.com/photo/coffee-1/","photographer":"Ana","photographer_url":"https://www.pexels.com/@ana",
  "alt":"Coffee beans in a sack","src":{"large2x":"https://images.pexels.com/photos/1/a.jpeg?w=1880","medium":"https://images.pexels.com/photos/1/a.jpeg?h=350"}},
 {"width":10,"height":10,"url":"https://www.pexels.com/photo/2/","photographer":"Eve","photographer_url":"https://www.pexels.com/@eve",
  "alt":"off host","src":{"large2x":"https://evil.example/b.jpeg","medium":"https://images.pexels.com/photos/2/b.jpeg"}}
]}`

// fakePexels serves body (or status) and counts requests, checking the key and query the client sends.
func fakePexels(t *testing.T, status int, body string) (*Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/search" || r.Header.Get("Authorization") != "key" || r.URL.Query().Get("query") == "" {
			t.Errorf("unexpected request %s %v", r.URL, r.Header)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(ts.Close)
	c := New("key", ts.Client())
	c.baseURL = ts.URL
	return c, &calls
}

func TestSearch_MapsTheResponseAndDropsOffHostImages(t *testing.T) {
	c, _ := fakePexels(t, http.StatusOK, pexelsBody)
	imgs, err := c.Search(context.Background(), 1, Input{Query: "coffee", Count: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := Image{
		URL: "https://images.pexels.com/photos/1/a.jpeg?w=1880", URLSmall: "https://images.pexels.com/photos/1/a.jpeg?h=350",
		Alt: "Coffee beans in a sack", Width: 4000, Height: 2667,
		Photographer: "Ana", PhotographerURL: "https://www.pexels.com/@ana", PageURL: "https://www.pexels.com/photo/coffee-1/",
	}
	if len(imgs) != 1 || imgs[0] != want {
		t.Errorf("got %+v, want only %+v", imgs, want)
	}
}

func TestSearch_CachesPerTenantAndQuery(t *testing.T) {
	c, calls := fakePexels(t, http.StatusOK, pexelsBody)
	ctx := context.Background()
	for _, call := range []struct {
		tenant uint64
		query  string
	}{{1, "coffee"}, {1, "Coffee"}, {2, "coffee"}, {1, "tea"}} {
		if _, err := c.Search(ctx, call.tenant, Input{Query: call.query, Count: 2}); err != nil {
			t.Fatal(err)
		}
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("want 3 provider calls (repeat query cached, other tenant and query not), got %d", n)
	}
}

func TestSearch_Errors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"rate limited", http.StatusTooManyRequests, `{}`, ErrRateLimited},
		{"server error", http.StatusInternalServerError, `{}`, ErrUnavailable},
		{"bad key", http.StatusUnauthorized, `{}`, ErrUnavailable},
		{"garbled body", http.StatusOK, `{"photos":`, ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := fakePexels(t, tt.status, tt.body)
			if _, err := c.Search(context.Background(), 1, Input{Query: "coffee", Count: 1}); !errors.Is(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
		})
	}
}

func TestCache_ExpiresAndStaysCapped(t *testing.T) {
	now := time.Now()
	c := newCache(2, time.Hour)
	c.now = func() time.Time { return now }
	c.set("a", nil)
	c.set("b", nil)
	c.set("c", nil)
	if len(c.entries) != 2 {
		t.Errorf("cache grew past its cap: %d entries", len(c.entries))
	}
	c.set("d", []Image{{URL: "u"}})
	now = now.Add(2 * time.Hour)
	if _, ok := c.get("d"); ok {
		t.Error("an expired entry was returned")
	}
}

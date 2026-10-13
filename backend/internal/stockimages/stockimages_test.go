package stockimages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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

const pixabayBody = `{"total":3,"totalHits":3,"hits":[
 {"id":101,"pageURL":"https://pixabay.com/photos/coffee-101/","tags":"coffee, beans, roast","largeImageURL":"https://pixabay.com/get/a_1280.jpg",
  "imageWidth":6000,"imageHeight":4000,"user":"Ana"},
 {"id":102,"tags":"latte","largeImageURL":"https://evil.example/b_1280.jpg","imageWidth":100,"imageHeight":100,"user":"Eve"},
 {"id":103,"tags":"espresso, cup","largeImageURL":"https://cdn.pixabay.com/photo/c_1280.jpg","imageWidth":800,"imageHeight":1200,"user":"Bo"}
]}`

// fakePixabay serves body (or status) and counts requests, checking the key, query and page size the client sends.
func fakePixabay(t *testing.T, status int, body string) (*Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		if r.URL.Path != "/api/" || q.Get("key") != "secret-key" || q.Get("q") == "" || q.Get("image_type") != "photo" {
			t.Errorf("unexpected request %s", r.URL)
		}
		if n, _ := strconv.Atoi(q.Get("per_page")); n < minPerPage {
			t.Errorf("per_page %d is under Pixabay's minimum of %d", n, minPerPage)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(ts.Close)
	c := New("secret-key", ts.Client())
	c.baseURL = ts.URL
	return c, &calls
}

func TestSearch_MapsTheResponse(t *testing.T) {
	c, _ := fakePixabay(t, http.StatusOK, pixabayBody)
	photos, err := c.Search(context.Background(), 1, Input{Query: "coffee", Count: 6})
	if err != nil {
		t.Fatal(err)
	}
	want := []Photo{
		{ID: 101, Description: "coffee, beans, roast", Width: 1280, Height: 853, Photographer: "Ana", url: "https://pixabay.com/get/a_1280.jpg"},
		{ID: 103, Description: "espresso, cup", Width: 800, Height: 1200, Photographer: "Bo", url: "https://cdn.pixabay.com/photo/c_1280.jpg"},
	}
	if len(photos) != len(want) || photos[0] != want[0] || photos[1] != want[1] {
		t.Errorf("got %+v, want %+v (off-host hit dropped, sizes scaled to 1280)", photos, want)
	}
	out, _ := json.Marshal(photos)
	if strings.Contains(string(out), "http") {
		t.Errorf("the model's view of a photo must carry no URL: %s", out)
	}
}

func TestSearch_TrimsToCount(t *testing.T) {
	c, _ := fakePixabay(t, http.StatusOK, pixabayBody)
	photos, err := c.Search(context.Background(), 1, Input{Query: "coffee", Count: 1})
	if err != nil || len(photos) != 1 || photos[0].ID != 101 {
		t.Errorf("got %+v, %v; want only the first photo", photos, err)
	}
}

func TestSearch_CachesPerTenantAndQuery(t *testing.T) {
	c, calls := fakePixabay(t, http.StatusOK, pixabayBody)
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
		{"rate limited", http.StatusTooManyRequests, `API rate limit exceeded`, ErrRateLimited},
		{"bad key", http.StatusBadRequest, `[ERROR 400] Invalid or missing API key`, ErrUnavailable},
		{"server error", http.StatusInternalServerError, `{}`, ErrUnavailable},
		{"garbled body", http.StatusOK, `{"hits":`, ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := fakePixabay(t, tt.status, tt.body)
			_, err := c.Search(context.Background(), 1, Input{Query: "coffee", Count: 1})
			if !errors.Is(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
		})
	}
}

// A transport error must never echo the search URL, which carries the API key.
func TestSearch_ErrorNeverLeaksTheKey(t *testing.T) {
	c := New("secret-key", &http.Client{Timeout: time.Second})
	c.baseURL = "http://127.0.0.1:1"
	_, err := c.Search(context.Background(), 1, Input{Query: "coffee", Count: 1})
	if err == nil || strings.Contains(err.Error(), "secret-key") {
		t.Errorf("err = %v; want a failure that does not contain the key", err)
	}
}

// fakeImages serves a JPEG of size bytes at /photo.jpg, and redirects /moved to /photo.jpg.
func fakeImages(t *testing.T, size int) *httptest.Server {
	t.Helper()
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, max(size-4, 0))...)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/photo.jpg":
			_, _ = w.Write(jpeg)
		case "/moved":
			http.Redirect(w, r, "/photo.jpg", http.StatusFound)
		case "/away":
			http.Redirect(w, r, "https://evil.example/x.jpg", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestDownload(t *testing.T) {
	ts := fakeImages(t, 1000)
	noRedirects := ts.Client()
	noRedirects.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	tests := []struct {
		name     string
		path     string
		maxBytes int
		allowed  bool
		want     error
	}{
		{"downloads the photo", "/photo.jpg", 2000, true, nil},
		{"follows a redirect on an allowed host", "/moved", 2000, true, nil},
		{"over the size limit", "/photo.jpg", 999, true, ErrTooLarge},
		{"not a Pixabay host", "/photo.jpg", 2000, false, ErrUnavailable},
		{"redirected off Pixabay", "/away", 2000, true, ErrUnavailable},
		{"missing", "/gone.jpg", 2000, true, ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New("k", noRedirects)
			c.allowed = func(raw string) bool { return tt.allowed && strings.HasPrefix(raw, ts.URL) }
			data, err := c.Download(context.Background(), Photo{ID: 7, url: ts.URL + tt.path}, tt.maxBytes)
			if tt.want != nil {
				if !errors.Is(err, tt.want) {
					t.Fatalf("err = %v, want %v", err, tt.want)
				}
				return
			}
			if err != nil || len(data) != 1000 {
				t.Fatalf("got %d bytes, %v", len(data), err)
			}
		})
	}
}

func TestOnDownloadHost(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://pixabay.com/get/a.jpg":           true,
		"https://cdn.pixabay.com/photo/a.jpg":     true,
		"http://pixabay.com/get/a.jpg":            false,
		"https://pixabay.com.evil.example/a.jpg":  false,
		"https://images.pexels.com/photos/1.jpeg": false,
		"not a url": false,
	} {
		if got := onDownloadHost(raw); got != want {
			t.Errorf("onDownloadHost(%q) = %v, want %v", raw, got, want)
		}
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
	c.set("d", []Photo{{ID: 1}})
	now = now.Add(2 * time.Hour)
	if _, ok := c.get("d"); ok {
		t.Error("an expired entry was returned")
	}
}

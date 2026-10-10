// Package stockimages searches Pixabay for licensed stock photos and downloads the ones a design uses. Pixabay forbids
// permanent hotlinking, so a page only ever references the downloaded copy saved in the theme.
package stockimages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	defaultBaseURL = "https://pixabay.com"
	MaxCount       = 6
	defaultCount   = 4
	// minPerPage is Pixabay's smallest per_page; a smaller count is trimmed after the call.
	minPerPage    = 3
	minQueryLen   = 2
	maxQueryLen   = 100
	searchTimeout = 8 * time.Second
	// downloadTimeout bounds one photo's download; largeImageURL is at most 1280px, so a few hundred KB.
	downloadTimeout = 15 * time.Second
	maxBodyBytes    = 1 << 20
	maxRedirects    = 3
	// largeEdge is largeImageURL's longest side, which Width/Height are scaled to.
	largeEdge = 1280
	// cacheTTL is Pixabay's required 24-hour caching of search responses.
	cacheMaxSize = 512
	cacheTTL     = 24 * time.Hour
)

// downloadHosts are the only hosts a photo is downloaded from, redirects included.
var downloadHosts = map[string]bool{"pixabay.com": true, "cdn.pixabay.com": true}

var (
	ErrInvalidInput = errors.New("invalid search_stock_images input")
	ErrRateLimited  = errors.New("stock image search is rate limited right now")
	ErrUnavailable  = errors.New("stock image search is unavailable right now")
	ErrTooLarge     = errors.New("stock photo is over the size limit")
)

// Input is one search_stock_images call.
type Input struct {
	Query string `json:"query"`
	Count int    `json:"count"`
}

// ParseInput validates a tool call; the query is a search term only, never a URL the server would fetch.
func ParseInput(raw json.RawMessage) (Input, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return Input{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	in.Query = strings.Join(strings.Fields(in.Query), " ")
	if n := utf8.RuneCountInString(in.Query); n < minQueryLen || n > maxQueryLen {
		return Input{}, fmt.Errorf("%w: query must be %d-%d characters", ErrInvalidInput, minQueryLen, maxQueryLen)
	}
	if strings.Contains(in.Query, "://") || strings.HasPrefix(strings.ToLower(in.Query), "www.") {
		return Input{}, fmt.Errorf("%w: query is a search term, not a URL", ErrInvalidInput)
	}
	if in.Count == 0 {
		in.Count = defaultCount
	}
	if in.Count < 1 || in.Count > MaxCount {
		return Input{}, fmt.Errorf("%w: count must be 1-%d", ErrInvalidInput, MaxCount)
	}
	return in, nil
}

// Photo is one search result. Its download URL is unexported, so marshalling a Photo for the model never reveals it.
type Photo struct {
	ID           int    `json:"id"`
	Description  string `json:"description"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Photographer string `json:"photographer"`
	url          string
}

// Client searches and downloads from Pixabay. Safe for concurrent use.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
	cache   *cache
	// allowed reports whether a download may be fetched from a URL; tests point it at a fake server.
	allowed func(raw string) bool
}

// New builds a Client; httpClient must be SSRF-guarded and must not follow redirects (urlfetch.NewGuardedClient).
func New(apiKey string, httpClient *http.Client) *Client {
	return &Client{apiKey: apiKey, baseURL: defaultBaseURL, http: httpClient, cache: newCache(cacheMaxSize, cacheTTL), allowed: onDownloadHost}
}

// Search returns up to in.Count photos for in.Query; results are cached per tenant and query.
func (c *Client) Search(ctx context.Context, tenantID uint64, in Input) ([]Photo, error) {
	key := strconv.FormatUint(tenantID, 10) + "|" + strings.ToLower(in.Query) + "|" + strconv.Itoa(in.Count)
	if photos, ok := c.cache.get(key); ok {
		return photos, nil
	}
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	q := url.Values{
		"key": {c.apiKey}, "q": {in.Query}, "image_type": {"photo"}, "safesearch": {"true"},
		"per_page": {strconv.Itoa(max(in.Count, minPerPage))},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build request", ErrUnavailable)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, withoutURL(err))
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, ErrRateLimited
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, withoutURL(err))
	}
	photos, err := mapResponse(body)
	if err != nil {
		return nil, err
	}
	if len(photos) > in.Count {
		photos = photos[:in.Count]
	}
	c.cache.set(key, photos)
	return photos, nil
}

// Download fetches p's image, refusing anything over maxBytes or served from outside Pixabay.
func (c *Client) Download(ctx context.Context, p Photo, maxBytes int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	target := p.url
	for hop := 0; ; hop++ {
		if !c.allowed(target) {
			return nil, fmt.Errorf("%w: photo %d is not served from Pixabay", ErrUnavailable, p.ID)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, fmt.Errorf("%w: build request", ErrUnavailable)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, withoutURL(err))
		}
		if loc := resp.Header.Get("Location"); resp.StatusCode >= 300 && resp.StatusCode < 400 && loc != "" {
			_ = resp.Body.Close()
			next, err := resp.Request.URL.Parse(loc)
			if err != nil || hop+1 >= maxRedirects {
				return nil, fmt.Errorf("%w: photo %d redirected too often", ErrUnavailable, p.ID)
			}
			target = next.String()
			continue
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%w: photo %d download status %d", ErrUnavailable, p.ID, resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, withoutURL(err))
		}
		if len(data) > maxBytes {
			return nil, fmt.Errorf("%w: photo %d", ErrTooLarge, p.ID)
		}
		return data, nil
	}
}

type pixabayResponse struct {
	Hits []struct {
		ID            int    `json:"id"`
		Tags          string `json:"tags"`
		LargeImageURL string `json:"largeImageURL"`
		ImageWidth    int    `json:"imageWidth"`
		ImageHeight   int    `json:"imageHeight"`
		User          string `json:"user"`
	} `json:"hits"`
}

// mapResponse keeps only photos downloadable from Pixabay's own hosts.
func mapResponse(body []byte) ([]Photo, error) {
	var r pixabayResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("%w: bad response: %w", ErrUnavailable, err)
	}
	photos := make([]Photo, 0, len(r.Hits))
	for _, h := range r.Hits {
		if h.ID <= 0 || !onDownloadHost(h.LargeImageURL) {
			continue
		}
		w, hgt := scaleToEdge(h.ImageWidth, h.ImageHeight, largeEdge)
		photos = append(photos, Photo{ID: h.ID, Description: h.Tags, Width: w, Height: hgt, Photographer: h.User, url: h.LargeImageURL})
	}
	return photos, nil
}

// scaleToEdge is w×h shrunk so its longest side is at most edge, as Pixabay scales largeImageURL.
func scaleToEdge(w, h, edge int) (int, int) {
	if w <= 0 || h <= 0 || (w <= edge && h <= edge) {
		return w, h
	}
	if w >= h {
		return edge, h * edge / w
	}
	return w * edge / h, edge
}

func onDownloadHost(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && downloadHosts[u.Hostname()]
}

// withoutURL drops the request URL from a transport error: a search URL carries the API key.
func withoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// cache is capped: entries keyed by tenant and query would otherwise grow for the life of the process.
type cache struct {
	mu      sync.Mutex
	max     int
	ttl     time.Duration
	now     func() time.Time
	entries map[string]cacheEntry
}

type cacheEntry struct {
	photos  []Photo
	expires time.Time
}

func newCache(max int, ttl time.Duration) *cache {
	return &cache{max: max, ttl: ttl, now: time.Now, entries: make(map[string]cacheEntry)}
}

func (c *cache) get(key string) ([]Photo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || c.now().After(e.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return e.photos, true
}

func (c *cache) set(key string, photos []Photo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.max {
		for k := range c.entries {
			delete(c.entries, k)
			break
		}
	}
	c.entries[key] = cacheEntry{photos: photos, expires: c.now().Add(c.ttl)}
}

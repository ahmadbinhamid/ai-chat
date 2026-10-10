// Package stockimages searches Pexels for licensed stock photos a design can hotlink, with the credit Pexels asks for.
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

// Host is where every returned image lives; a page may hotlink it, so theme validation allows it.
const Host = "images.pexels.com"

const (
	defaultBaseURL = "https://api.pexels.com"
	MaxCount       = 6
	defaultCount   = 4
	minQueryLen    = 2
	maxQueryLen    = 100
	searchTimeout  = 8 * time.Second
	maxBodyBytes   = 1 << 20
	cacheMaxSize   = 512
	cacheTTL       = 24 * time.Hour
)

var (
	ErrInvalidInput = errors.New("invalid search_stock_images input")
	ErrRateLimited  = errors.New("stock image search is rate limited right now")
	ErrUnavailable  = errors.New("stock image search is unavailable right now")
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

// Image is one search result, ready to hotlink and credit.
type Image struct {
	URL             string `json:"url"`
	URLSmall        string `json:"url_small"`
	Alt             string `json:"alt"`
	Width           int    `json:"width"`
	Height          int    `json:"height"`
	Photographer    string `json:"photographer"`
	PhotographerURL string `json:"photographer_url"`
	PageURL         string `json:"page_url"`
}

// Client searches Pexels. Safe for concurrent use.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
	cache   *cache
}

// New builds a Client; httpClient should be SSRF-guarded (urlfetch.NewGuardedClient).
func New(apiKey string, httpClient *http.Client) *Client {
	return &Client{apiKey: apiKey, baseURL: defaultBaseURL, http: httpClient, cache: newCache(cacheMaxSize, cacheTTL)}
}

// Search returns up to in.Count images for in.Query; results are cached per tenant and query.
func (c *Client) Search(ctx context.Context, tenantID uint64, in Input) ([]Image, error) {
	key := strconv.FormatUint(tenantID, 10) + "|" + strings.ToLower(in.Query) + "|" + strconv.Itoa(in.Count)
	if imgs, ok := c.cache.get(key); ok {
		return imgs, nil
	}
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	q := url.Values{"query": {in.Query}, "per_page": {strconv.Itoa(in.Count)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/search?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
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
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	imgs, err := mapResponse(body)
	if err != nil {
		return nil, err
	}
	c.cache.set(key, imgs)
	return imgs, nil
}

type pexelsResponse struct {
	Photos []struct {
		Width           int    `json:"width"`
		Height          int    `json:"height"`
		URL             string `json:"url"`
		Photographer    string `json:"photographer"`
		PhotographerURL string `json:"photographer_url"`
		Alt             string `json:"alt"`
		Src             struct {
			Large2x string `json:"large2x"`
			Medium  string `json:"medium"`
		} `json:"src"`
	} `json:"photos"`
}

// mapResponse keeps only photos served from Host, so the model is never handed an arbitrary URL.
func mapResponse(body []byte) ([]Image, error) {
	var r pexelsResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("%w: bad response: %w", ErrUnavailable, err)
	}
	imgs := make([]Image, 0, len(r.Photos))
	for _, p := range r.Photos {
		if !onHost(p.Src.Large2x) || !onHost(p.Src.Medium) {
			continue
		}
		imgs = append(imgs, Image{
			URL: p.Src.Large2x, URLSmall: p.Src.Medium, Alt: p.Alt, Width: p.Width, Height: p.Height,
			Photographer: p.Photographer, PhotographerURL: p.PhotographerURL, PageURL: p.URL,
		})
	}
	return imgs, nil
}

func onHost(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == Host
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
	imgs    []Image
	expires time.Time
}

func newCache(max int, ttl time.Duration) *cache {
	return &cache{max: max, ttl: ttl, now: time.Now, entries: make(map[string]cacheEntry)}
}

func (c *cache) get(key string) ([]Image, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || c.now().After(e.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return e.imgs, true
}

func (c *cache) set(key string, imgs []Image) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.max {
		for k := range c.entries {
			delete(c.entries, k)
			break
		}
	}
	c.entries[key] = cacheEntry{imgs: imgs, expires: c.now().Add(c.ttl)}
}

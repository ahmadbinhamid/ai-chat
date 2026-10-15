package themebuild

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"

	"ai-chat/internal/themefs"
)

// treeThemeStore is mapThemeStore with a real file listing, counting reads per path and failing those in failOn.
type treeThemeStore struct {
	mapThemeStore
	failOn map[string]bool

	mu    sync.Mutex
	reads map[string]int
}

func newTreeThemeStore(files map[string]string) *treeThemeStore {
	return &treeThemeStore{mapThemeStore: mapThemeStore{files: files}, reads: map[string]int{}}
}

func (s *treeThemeStore) ReadFile(ctx context.Context, auth themefs.RequestAuth, p string) (string, error) {
	s.mu.Lock()
	s.reads[p]++
	s.mu.Unlock()
	if s.failOn[p] {
		return "", errors.New("upstream down")
	}
	return s.mapThemeStore.ReadFile(ctx, auth, p)
}

func (s *treeThemeStore) ListFiles(context.Context, themefs.RequestAuth) ([]themefs.FileTreeEntry, error) {
	entries := make([]themefs.FileTreeEntry, 0, len(s.files))
	for p := range s.files {
		entries = append(entries, themefs.FileTreeEntry{Name: p, Path: p, Type: "file"})
	}
	return entries, nil
}

func samplePageTheme() map[string]string {
	return map[string]string{
		"pages.json":                 `[{"slug":"home","path":"pages","page":"home"}]`,
		"pages/home.liquid":          `{% render 'liquid/layout-start' %}{% render 'components/hero' %}`,
		"pages/about.liquid":         `{% render 'liquid/layout-start' %}{% render 'components/team' %}`,
		"liquid/layout-start.liquid": `<link href="{{ 'css/base.css' | asset_url }}"><img src="{{ 'img/logo.png' | asset_url }}">`,
		"components/hero.liquid":     `<script src="/theme-assets/js/hero.js"></script>{% render 'components/hero' %}`,
		"components/team.liquid":     `<div>team</div>`,
		"css/base.css":               `body{}`,
		"css/unused.css":             `.x{}`,
		"js/hero.js":                 `1`,
		"img/logo.png":               `png`,
		"defaults.json":              `{}`,
	}
}

func keysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestLoadPageFiles(t *testing.T) {
	auth := themefs.RequestAuth{Token: "t", TenantID: 1}
	tests := []struct {
		name         string
		files        map[string]string
		entry        string
		wantFiles    []string
		wantComplete bool
	}{
		{
			name:  "only what the page renders, plus pages.json",
			files: samplePageTheme(),
			entry: "pages/home.liquid",
			wantFiles: []string{
				"components/hero.liquid", "css/base.css", "js/hero.js", "liquid/layout-start.liquid", "pages.json", "pages/home.liquid",
			},
		},
		{
			name:      "another page shares the layout but not the hero",
			files:     samplePageTheme(),
			entry:     "pages/about.liquid",
			wantFiles: []string{"components/team.liquid", "css/base.css", "liquid/layout-start.liquid", "pages.json", "pages/about.liquid"},
		},
		{
			name:      "missing entry returns pages.json so the route can be resolved",
			files:     samplePageTheme(),
			entry:     "pages/shop.liquid",
			wantFiles: []string{"pages.json"},
		},
		{
			name:      "reference to a file that doesn't exist is skipped",
			files:     map[string]string{"pages/home.liquid": `{% render 'components/gone' %}`},
			entry:     "pages/home.liquid",
			wantFiles: []string{"pages/home.liquid"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTreeThemeStore(tt.files)
			got, err := LoadPageFiles(context.Background(), store, auth, tt.entry)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(keysOf(got.Files), tt.wantFiles) || got.Complete != tt.wantComplete {
				t.Errorf("got %v complete=%v; want %v complete=%v", keysOf(got.Files), got.Complete, tt.wantFiles, tt.wantComplete)
			}
			for p, content := range got.Files {
				if content != tt.files[p] {
					t.Errorf("%s: got %q, want %q", p, content, tt.files[p])
				}
			}
			for p, n := range store.reads {
				if n != 1 {
					t.Errorf("%s read %d times, want once", p, n)
				}
			}
		})
	}
}

func TestLoadPageFiles_DynamicReferenceLoadsWhatLoadThemeFilesDoes(t *testing.T) {
	files := samplePageTheme()
	files["components/hero.liquid"] = `{% render hero_variant %}`
	auth := themefs.RequestAuth{Token: "t", TenantID: 1}

	got, err := LoadPageFiles(context.Background(), newTreeThemeStore(files), auth, "pages/home.liquid")
	if err != nil {
		t.Fatal(err)
	}
	want, err := (&Service{}).LoadThemeFiles(context.Background(), newTreeThemeStore(files), auth, true)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Complete || !reflect.DeepEqual(got.Files, want) {
		t.Errorf("got %v complete=%v; want LoadThemeFiles' %v", keysOf(got.Files), got.Complete, keysOf(want))
	}
}

func TestLoadPageFiles_ReadErrorFailsTheLoad(t *testing.T) {
	store := newTreeThemeStore(samplePageTheme())
	store.failOn = map[string]bool{"components/hero.liquid": true}
	if _, err := LoadPageFiles(context.Background(), store, themefs.RequestAuth{}, "pages/home.liquid"); err == nil {
		t.Fatal("want an error when a page's file can't be read")
	}
}

func TestLoadPageFiles_CancelledContextFailsTheLoad(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LoadPageFiles(ctx, newTreeThemeStore(samplePageTheme()), themefs.RequestAuth{}, "pages/home.liquid"); err == nil {
		t.Fatal("want an error, not a partial page, when the request is cancelled")
	}
}

func TestLoadPageFiles_ReadsTheDraft(t *testing.T) {
	draft := map[string]string{
		"pages/home.liquid":       `{% render 'components/promo' %}`,
		"components/promo.liquid": `<b>new</b>`,
	}
	store := themefs.NewOverlayStore(newTreeThemeStore(samplePageTheme()), draft)
	got, err := LoadPageFiles(context.Background(), store, themefs.RequestAuth{}, "pages/home.liquid")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"components/promo.liquid", "pages.json", "pages/home.liquid"}
	if !reflect.DeepEqual(keysOf(got.Files), want) || got.Files["components/promo.liquid"] != `<b>new</b>` {
		t.Errorf("got %v; want the draft's own references %v with draft content", got.Files, want)
	}
}

func TestDraftPageFiles_RejectsUnsafeEntry(t *testing.T) {
	for _, entry := range []string{"pages/home.css", "../secret.liquid", "/etc/x.liquid", "pages/../../x.liquid", ""} {
		if _, err := (&Service{}).DraftPageFiles(context.Background(), 1, "t", "chat", entry); !errors.Is(err, ErrInvalidEntry) {
			t.Errorf("entry %q: got %v, want ErrInvalidEntry", entry, err)
		}
	}
}

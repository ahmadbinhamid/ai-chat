package prefetch

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const testPagesJSON = `[
	{"title": "Home", "slug": "home", "path": "/pages", "page": "home"},
	{"title": "Shop", "slug": "shop", "path": "/pages", "page": "products"},
	{"title": "About Us", "slug": "about-us", "path": "/pages", "page": "about"},
	{"title": "Contact", "slug": "contact", "path": "/pages", "page": "contact"},
	{"title": "Login", "slug": "login", "path": "/pages/auth", "page": "login"},
	{"title": "Product", "slug": "product", "path": "/pages", "page": "product"}
]`

func TestEntryPathForRoute(t *testing.T) {
	entries := parsePages(testPagesJSON)
	tests := []struct {
		route string
		want  string
	}{
		{"", "pages/home.liquid"},
		{"/", "pages/home.liquid"},
		{"shop", "pages/products.liquid"},
		{"/shop?sort=price#top", "pages/products.liquid"},
		{"auth/login", "pages/auth/login.liquid"},
		{"product/red-hat", "pages/product.liquid"},
		{"faq", "pages/faq.liquid"},
		{"auth/forgot", "pages/auth/forgot.liquid"},
		{"category/hats", "pages/category.liquid"},
	}
	for _, tt := range tests {
		if got := entryPathForRoute(tt.route, entries); got != tt.want {
			t.Errorf("entryPathForRoute(%q) = %q, want %q", tt.route, got, tt.want)
		}
	}
}

func TestEntryPathForRoute_NoRegistry(t *testing.T) {
	if got := entryPathForRoute("shop", parsePages("not json")); got != "pages/shop.liquid" {
		t.Errorf("without pages.json, shop = %q, want pages/shop.liquid", got)
	}
}

func TestRenderedPaths(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{"render and include, both quote styles", `{% render 'components/header' %}<main>{%- include "liquid/hero.liquid" -%}</main>`,
			[]string{"components/header.liquid", "liquid/hero.liquid"}},
		{"with arguments", `{% render 'components/card', product: p %}`, []string{"components/card.liquid"}},
		{"duplicates collapse", `{% render 'components/a' %}{% render 'components/a' %}`, []string{"components/a.liquid"}},
		{"variable target is not a file", `{% render section_name %}`, nil},
		{"no tags", `<h1>{{ shop.name }}</h1>`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RenderedPaths(tt.content); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RenderedPaths = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNamedPagePaths(t *testing.T) {
	entries := parsePages(testPagesJSON)
	tests := []struct {
		prompt string
		want   []string
	}{
		{"make the about us page darker", []string{"pages/about.liquid"}},
		{"On the Contact page, add a map", []string{"pages/contact.liquid"}},
		{"fix page shop's header", []string{"pages/products.liquid"}},
		{"change the button on /about-us", []string{"pages/about.liquid"}},
		{"add more products to the grid", nil},
		{"make the header sticky", nil},
		{"link the shop page to the contact page", []string{"pages/products.liquid", "pages/contact.liquid"}},
		{"the /aboutus banner", nil},
	}
	for _, tt := range tests {
		t.Run(tt.prompt, func(t *testing.T) {
			if got := namedPagePaths(tt.prompt, entries); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("namedPagePaths(%q) = %v, want %v", tt.prompt, got, tt.want)
			}
		})
	}
}

// fakeTheme is a ReadFunc over an in-memory theme; missing paths read as "" like the real store.
type fakeTheme map[string]string

func (f fakeTheme) read(_ context.Context, p string) (string, error) {
	if p == "boom.liquid" {
		return "", errors.New("read failed")
	}
	return f[p], nil
}

func paths(r Result) []string {
	out := make([]string, 0, len(r.Files))
	for _, f := range r.Files {
		out = append(out, f.Path)
	}
	return out
}

func TestSelect(t *testing.T) {
	theme := fakeTheme{
		"pages/products.liquid":          `{% render 'components/header' %}{% render 'components/product-grid' %}`,
		"pages/about.liquid":             `<h1>About</h1>`,
		"pages/contact.liquid":           `<h1>Contact</h1>{% render 'components/map' %}`,
		"components/header.liquid":       `<header>{% render 'components/nav' %}</header>`,
		"components/nav.liquid":          `<nav></nav>`,
		"components/product-grid.liquid": `<div class="grid"></div>`,
		"components/map.liquid":          `<div id="map"></div>`,
		"components/footer.liquid":       `<footer>{% render 'components/social' %}</footer>`,
		"components/social.liquid":       `<a></a>`,
		"components/css/footer.css":      `footer { color: red; }`,
		"assets/images/hero.png":         "\x89PNG",
		"pages.json":                     testPagesJSON,
	}
	tests := []struct {
		name   string
		in     Input
		want   []string
		reason []Reason
	}{
		{"route page then what it renders, one level deep",
			Input{Route: "shop", HasRoute: true},
			[]string{"pages/products.liquid", "components/header.liquid", "components/product-grid.liquid"},
			[]Reason{ReasonRoutePage, ReasonRendered, ReasonRendered}},
		{"focus file and its components follow the route page",
			Input{Route: "shop", HasRoute: true, FocusFile: "components/footer.liquid"},
			[]string{"pages/products.liquid", "components/footer.liquid", "components/header.liquid", "components/product-grid.liquid", "components/social.liquid"},
			[]Reason{ReasonRoutePage, ReasonFocusFile, ReasonRendered, ReasonRendered, ReasonRendered}},
		{"named pages come last",
			Input{Route: "shop", HasRoute: true, Prompt: "copy the hero from the about us page"},
			[]string{"pages/products.liquid", "components/header.liquid", "components/product-grid.liquid", "pages/about.liquid"},
			[]Reason{ReasonRoutePage, ReasonRendered, ReasonRendered, ReasonNamedPage}},
		{"a named page that is the route page appears once",
			Input{Route: "about-us", HasRoute: true, Prompt: "darken the about us page"},
			[]string{"pages/about.liquid"},
			[]Reason{ReasonRoutePage}},
		{"no route known: focus file only",
			Input{FocusFile: "components/css/footer.css"},
			[]string{"components/css/footer.css"},
			[]Reason{ReasonFocusFile}},
		{"route given as home with no home page file", Input{Route: "", HasRoute: true}, []string{}, nil},
		{"nothing known", Input{Prompt: "make it pop"}, []string{}, nil},
		{"unsafe or non-text focus files are ignored",
			Input{FocusFile: "../../etc/passwd"}, []string{}, nil},
		{"images are never preloaded", Input{FocusFile: "assets/images/hero.png"}, []string{}, nil},
		{"pages.json is already in context", Input{FocusFile: "pages.json"}, []string{}, nil},
		{"a failing read is skipped", Input{FocusFile: "boom.liquid"}, []string{}, nil},
		{"Skip excludes draft-only paths",
			Input{Route: "shop", HasRoute: true, Skip: func(p string) bool { return p == "components/header.liquid" }},
			[]string{"pages/products.liquid", "components/product-grid.liquid"},
			[]Reason{ReasonRoutePage, ReasonRendered}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.in
			in.PagesJSON = testPagesJSON
			got := Select(context.Background(), in, theme.read)
			if !reflect.DeepEqual(paths(got), tt.want) {
				t.Fatalf("paths = %v, want %v", paths(got), tt.want)
			}
			for i, f := range got.Files {
				if f.Reason != tt.reason[i] {
					t.Errorf("%s reason = %s, want %s", f.Path, f.Reason, tt.reason[i])
				}
				if f.Content != theme[f.Path] {
					t.Errorf("%s content not the file's", f.Path)
				}
			}
			total := 0
			for _, f := range got.Files {
				total += len(f.Content)
			}
			if got.Bytes != total {
				t.Errorf("Bytes = %d, want %d", got.Bytes, total)
			}
		})
	}
}

func TestSelect_Caps(t *testing.T) {
	big := strings.Repeat("x", 15_000)
	var page strings.Builder
	theme := fakeTheme{}
	for i := 0; i < 12; i++ {
		name := "components/c" + string(rune('a'+i))
		page.WriteString("{% render '" + name + "' %}")
		theme[name+".liquid"] = "<p>" + string(rune('a'+i)) + "</p>"
	}
	theme["pages/products.liquid"] = page.String()

	tests := []struct {
		name      string
		theme     fakeTheme
		focus     string
		wantFiles int
		wantPaths []string
	}{
		{"at most MaxFiles files", theme, "", MaxFiles, nil},
		{"a file over the byte budget is skipped, later ones still fit",
			fakeTheme{
				"pages/products.liquid": "{% render 'components/a' %}{% render 'components/b' %}{% render 'components/c' %}",
				"components/a.liquid":   big,
				"components/b.liquid":   big,
				"components/c.liquid":   big,
			}, "", 3,
			[]string{"pages/products.liquid", "components/a.liquid", "components/b.liquid"}},
		{"route page wins the budget over the focus file",
			fakeTheme{"pages/products.liquid": strings.Repeat("y", 30_000), "components/f.liquid": big},
			"components/f.liquid", 1, []string{"pages/products.liquid"}},
		{"a focus file too big to send still has its components considered",
			fakeTheme{"pages/products.liquid": "<p></p>", "components/f.liquid": strings.Repeat("z", MaxBytes) + "{% render 'components/g' %}",
				"components/g.liquid": "<g></g>"},
			"components/f.liquid", 2, []string{"pages/products.liquid", "components/g.liquid"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Select(context.Background(), Input{Route: "shop", HasRoute: true, FocusFile: tt.focus, PagesJSON: testPagesJSON}, tt.theme.read)
			if len(got.Files) != tt.wantFiles {
				t.Fatalf("got %d files %v, want %d", len(got.Files), paths(got), tt.wantFiles)
			}
			if got.Bytes > MaxBytes {
				t.Fatalf("Bytes = %d, over MaxBytes", got.Bytes)
			}
			if tt.wantPaths != nil && !reflect.DeepEqual(paths(got), tt.wantPaths) {
				t.Fatalf("paths = %v, want %v", paths(got), tt.wantPaths)
			}
		})
	}
}

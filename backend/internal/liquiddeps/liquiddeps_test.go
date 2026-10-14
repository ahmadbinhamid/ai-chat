package liquiddeps

import (
	"reflect"
	"testing"
)

func TestReferences(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantRefs    []string
		wantDynamic bool
	}{
		{name: "no references", content: `<h1>{{ store.name }}</h1>{% if x %}y{% endif %}`, wantRefs: []string{}},
		{
			name:     "render with single and double quotes and args",
			content:  `{% render 'liquid/layout-start', page: page %}{%- render "components/hero" -%}`,
			wantRefs: []string{"components/hero.liquid", "liquid/layout-start.liquid"},
		},
		{name: "include", content: `{% include 'snippets/a' %}`, wantRefs: []string{"snippets/a.liquid"}},
		{name: "multi-line tag", content: "{% render\n  'components/card',\n  product: p\n%}", wantRefs: []string{"components/card.liquid"}},
		{name: "duplicates collapse", content: `{% render 'a' %}{% render 'a' %}`, wantRefs: []string{"a.liquid"}},
		{name: "extension is always appended", content: `{% render 'a.liquid' %}`, wantRefs: []string{"a.liquid.liquid"}},
		{name: "render inside a comment still counts", content: `{% comment %}{% render 'old' %}{% endcomment %}`, wantRefs: []string{"old.liquid"}},
		{name: "layout literal", content: `{% layout 'theme' %}`, wantRefs: []string{"theme.liquid"}},
		{name: "layout none", content: `{% layout none %}`, wantRefs: []string{}},
		{
			name:     "asset_url css and js, query stripped",
			content:  `<link href="{{ 'css/a.css' | asset_url }}"><script src="{{ "js/b.js?v=2" | asset_url }}"></script>`,
			wantRefs: []string{"css/a.css", "js/b.js"},
		},
		{name: "asset_url image is not a reference", content: `<img src="{{ 'img/logo.png' | asset_url }}">`, wantRefs: []string{}},
		{name: "literal theme-assets path", content: `<link href="/theme-assets/css/c.css?x=1">`, wantRefs: []string{"css/c.css"}},
		{name: "render variable", content: `{% render partial_name %}`, wantRefs: []string{}, wantDynamic: true},
		{name: "relative render", content: `{% render './sibling' %}`, wantRefs: []string{}, wantDynamic: true},
		{name: "parent render", content: `{% render '../up' %}`, wantRefs: []string{}, wantDynamic: true},
		{name: "absolute render", content: `{% render '/abs' %}`, wantRefs: []string{}, wantDynamic: true},
		{name: "liquid in the name", content: `{% render 'a{{ x }}' %}`, wantRefs: []string{}, wantDynamic: true},
		{name: "liquid tag", content: "{% liquid\n render 'a'\n%}", wantRefs: []string{}, wantDynamic: true},
		{name: "layout variable", content: `{% layout theme_layout %}`, wantRefs: []string{}, wantDynamic: true},
		{name: "asset_url on a variable", content: `{{ stylesheet | asset_url }}`, wantRefs: []string{}, wantDynamic: true},
		{
			name:        "known references survive a dynamic one",
			content:     `{% render 'a' %}{% render b %}`,
			wantRefs:    []string{"a.liquid"},
			wantDynamic: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refs, dynamic := References(tt.content)
			if !reflect.DeepEqual(refs, tt.wantRefs) || dynamic != tt.wantDynamic {
				t.Errorf("References() = %v, dynamic %v; want %v, dynamic %v", refs, dynamic, tt.wantRefs, tt.wantDynamic)
			}
		})
	}
}

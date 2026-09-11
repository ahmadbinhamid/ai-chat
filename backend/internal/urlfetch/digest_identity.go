package urlfetch

import (
	"fmt"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// metaDescriptionMaxChars bounds the PAGE section's description field.
const metaDescriptionMaxChars = 300

type pageIdentity struct {
	title       string
	description string
	lang        string
}

func extractPage(htmlSrc string) pageIdentity {
	var page pageIdentity
	inTitle := false
	var titleBuf strings.Builder
	z := html.NewTokenizer(strings.NewReader(htmlSrc))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return page
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			switch t.Data {
			case "html":
				if lang := attrVal(t, "lang"); lang != "" && page.lang == "" {
					page.lang = lang
				}
			case "title":
				// First <title> wins — a second one is malformed markup,
				// not a real second choice.
				if page.title == "" {
					inTitle = true
					titleBuf.Reset()
				}
			case "meta":
				if page.description == "" && strings.EqualFold(attrVal(t, "name"), "description") {
					page.description = truncateBytes(collapseWhitespace(attrVal(t, "content")), metaDescriptionMaxChars)
				}
			}
		case html.EndTagToken:
			if inTitle {
				t := z.Token()
				if t.Data == "title" {
					page.title = collapseWhitespace(titleBuf.String())
					inTitle = false
				}
			}
		case html.TextToken:
			if inTitle {
				titleBuf.WriteString(z.Token().Data)
			}
		}
	}
}

func writeIdentitySection(b *strings.Builder, finalURL *url.URL, page pageIdentity) {
	b.WriteString("PAGE\n")
	if finalURL != nil {
		fmt.Fprintf(b, "url: %s\n", finalURL.String())
	}
	if page.title != "" {
		fmt.Fprintf(b, "title: %s\n", page.title)
	}
	if page.description != "" {
		fmt.Fprintf(b, "description: %s\n", page.description)
	}
	if page.lang != "" {
		fmt.Fprintf(b, "lang: %s\n", page.lang)
	}
	b.WriteString("\n")
}

package urlfetch

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

// maxCopyChars bounds the visible-body-text section; maxInteractiveLabels
// and interactiveLabelMaxChars bound the separate interactive-labels list
// (link/button/form-field text) — see BuildDigest's own doc comment for
// why these are kept apart from the general copy.
const (
	maxCopyChars             = 2000
	maxInteractiveLabels     = 40
	interactiveLabelMaxChars = 60
)

// interactiveLabelTags are the elements whose visible text BuildDigest
// collects separately from general copy (see extractCopy) — what a
// merchant usually means by "match the buttons/nav" on a reference page.
var interactiveLabelTags = map[string]bool{"a": true, "button": true, "label": true}

type copyInfo struct {
	bodyText string
	labels   []string
}

// labelFrame mirrors landmarkFrame for interactive elements (a, button,
// label) — also stack-based since a <label> commonly wraps other markup
// (an <input> plus its own text) rather than being a leaf.
type labelFrame struct {
	tag string
	buf strings.Builder
}

func extractCopy(htmlSrc string) copyInfo {
	var body strings.Builder
	var labels []string
	seenLabels := make(map[string]bool)
	var labelStack []*labelFrame
	excludeDepth := 0

	addLabel := func(text string) {
		text = truncateBytes(text, interactiveLabelMaxChars)
		if text == "" || seenLabels[text] || len(labels) >= maxInteractiveLabels {
			return
		}
		seenLabels[text] = true
		labels = append(labels, text)
	}

	z := html.NewTokenizer(strings.NewReader(htmlSrc))
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return copyInfo{bodyText: truncateBytes(collapseWhitespace(body.String()), maxCopyChars), labels: labels}
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			if excludedTextTags[t.Data] {
				if tt == html.StartTagToken {
					excludeDepth++
				}
				continue
			}
			if interactiveLabelTags[t.Data] && tt == html.StartTagToken {
				labelStack = append(labelStack, &labelFrame{tag: t.Data})
				continue
			}
			// input[type=submit|button|reset] has no closing content of its
			// own to capture text from — its visible label is its value
			// attribute instead.
			if t.Data == "input" {
				switch strings.ToLower(attrVal(t, "type")) {
				case "submit", "button", "reset":
					addLabel(collapseWhitespace(attrVal(t, "value")))
				}
			}
		case html.EndTagToken:
			t := z.Token()
			if excludedTextTags[t.Data] {
				if excludeDepth > 0 {
					excludeDepth--
				}
				continue
			}
			if interactiveLabelTags[t.Data] {
				for i := len(labelStack) - 1; i >= 0; i-- {
					if labelStack[i].tag != t.Data {
						continue
					}
					frame := labelStack[i]
					labelStack = append(labelStack[:i], labelStack[i+1:]...)
					addLabel(collapseWhitespace(frame.buf.String()))
					break
				}
			}
		case html.TextToken:
			if excludeDepth > 0 {
				continue
			}
			text := z.Token().Data
			body.WriteString(text)
			body.WriteByte(' ')
			for _, frame := range labelStack {
				frame.buf.WriteString(text)
			}
		}
	}
}

func writeCopySection(b *strings.Builder, c copyInfo) {
	if c.bodyText == "" && len(c.labels) == 0 {
		return
	}
	b.WriteString("COPY\n")
	if c.bodyText != "" {
		fmt.Fprintf(b, "%s\n", c.bodyText)
	}
	if len(c.labels) > 0 {
		fmt.Fprintf(b, "interactive labels: %s\n", strings.Join(c.labels, ", "))
	}
	b.WriteString("\n")
}

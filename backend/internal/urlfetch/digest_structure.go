package urlfetch

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

// maxHeadings and maxLandmarks bound the structure outline; landmarkPreviewChars
// bounds each landmark's one-line text preview.
const (
	maxHeadings          = 40
	maxLandmarks         = 20
	landmarkPreviewChars = 120
)

// maxHeadingChars caps a single heading's text — every other per-item field
// in this file is already length-capped one way or another; without this,
// one pathological heading (a merchant's own copy dumped into an <h1>)
// could dominate the whole budget on its own before DigestHardCapBytes'
// final truncation ever gets a say in which section loses ground. See
// maxCSSValueChars in digest_tokens.go for the same reasoning applied to a
// declared CSS value.
const maxHeadingChars = 200

// headingTags maps a heading tag name to its outline depth/level.
var headingTags = map[string]int{"h1": 1, "h2": 2, "h3": 3}

// landmarkTags are the elements STRUCTURE's outline previews — the HTML
// landmark roles a merchant's own section rhythm is usually built from.
var landmarkTags = map[string]bool{
	"header": true, "nav": true, "main": true, "section": true,
	"article": true, "aside": true, "footer": true,
}

type heading struct {
	level int
	text  string
}

type landmark struct {
	tag     string
	preview string
}

type structureInfo struct {
	headings  []heading
	landmarks []landmark
}

// landmarkFrame tracks one currently-open landmark element's accumulating
// preview text — a stack of these, not a single variable, since landmarks
// nest constantly in real markup (a <nav> inside a <header>, a <section>
// inside <main>).
type landmarkFrame struct {
	tag  string
	buf  strings.Builder
	done bool // stop appending once buf has enough for a preview, but the frame stays open so close-tag bookkeeping (matching by tag name) stays correct
}

func extractStructure(htmlSrc string) structureInfo {
	var out structureInfo
	headingLevel := 0
	var headingBuf strings.Builder
	var landmarkStack []*landmarkFrame
	excludeDepth := 0

	z := html.NewTokenizer(strings.NewReader(htmlSrc))
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return out
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			if excludedTextTags[t.Data] {
				if tt == html.StartTagToken {
					excludeDepth++
				}
				continue
			}
			if level, ok := headingTags[t.Data]; ok && tt == html.StartTagToken {
				headingLevel = level
				headingBuf.Reset()
				continue
			}
			if landmarkTags[t.Data] && tt == html.StartTagToken {
				landmarkStack = append(landmarkStack, &landmarkFrame{tag: t.Data})
			}
		case html.EndTagToken:
			t := z.Token()
			if excludedTextTags[t.Data] {
				if excludeDepth > 0 {
					excludeDepth--
				}
				continue
			}
			if level, ok := headingTags[t.Data]; ok && headingLevel == level {
				text := truncateBytes(collapseWhitespace(headingBuf.String()), maxHeadingChars)
				if text != "" && len(out.headings) < maxHeadings {
					out.headings = append(out.headings, heading{level: level, text: text})
				}
				headingLevel = 0
				continue
			}
			if landmarkTags[t.Data] {
				for i := len(landmarkStack) - 1; i >= 0; i-- {
					if landmarkStack[i].tag != t.Data {
						continue
					}
					frame := landmarkStack[i]
					landmarkStack = append(landmarkStack[:i], landmarkStack[i+1:]...)
					preview := collapseWhitespace(frame.buf.String())
					if preview != "" && len(out.landmarks) < maxLandmarks {
						out.landmarks = append(out.landmarks, landmark{tag: frame.tag, preview: truncateBytes(preview, landmarkPreviewChars)})
					}
					break
				}
			}
		case html.TextToken:
			if excludeDepth > 0 {
				continue
			}
			text := z.Token().Data
			if headingLevel > 0 {
				headingBuf.WriteString(text)
			}
			for _, frame := range landmarkStack {
				if !frame.done {
					frame.buf.WriteString(text)
					if frame.buf.Len() >= landmarkPreviewChars {
						frame.done = true
					}
				}
			}
		}
	}
}

func writeStructureSection(b *strings.Builder, s structureInfo) {
	if len(s.headings) == 0 && len(s.landmarks) == 0 {
		return
	}
	b.WriteString("STRUCTURE\n")
	if len(s.headings) > 0 {
		b.WriteString("headings:\n")
		for _, h := range s.headings {
			fmt.Fprintf(b, "  %sh%d: %s\n", strings.Repeat("  ", h.level-1), h.level, h.text)
		}
	}
	if len(s.landmarks) > 0 {
		b.WriteString("landmarks:\n")
		for _, l := range s.landmarks {
			fmt.Fprintf(b, "  <%s>: %s\n", l.tag, l.preview)
		}
	}
	b.WriteString("\n")
}

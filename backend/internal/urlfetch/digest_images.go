package urlfetch

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

// imageAltPreviewMaxChars and maxImages bound the IMAGES section.
const (
	imageAltPreviewMaxChars = 100
	maxImages               = 20
)

type imageInfo struct {
	alt  string
	stem string
}

// filenameStem reduces an <img> src to its base filename ("hero-banner") —
// often a real clue about the image's subject, unlike the rest of the URL.
func filenameStem(src string) string {
	if src == "" {
		return ""
	}
	if i := strings.IndexAny(src, "?#"); i >= 0 {
		src = src[:i]
	}
	if i := strings.LastIndexByte(src, '/'); i >= 0 {
		src = src[i+1:]
	}
	if i := strings.LastIndexByte(src, '.'); i > 0 {
		src = src[:i]
	}
	return src
}

func extractImages(htmlSrc string) []imageInfo {
	var out []imageInfo
	seen := make(map[string]bool)
	z := html.NewTokenizer(strings.NewReader(htmlSrc))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return out
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			if t.Data != "img" {
				continue
			}
			alt := truncateBytes(collapseWhitespace(attrVal(t, "alt")), imageAltPreviewMaxChars)
			stem := filenameStem(attrVal(t, "src"))
			if alt == "" && stem == "" {
				continue
			}
			key := alt + "|" + stem
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, imageInfo{alt: alt, stem: stem})
			if len(out) >= maxImages {
				return out
			}
		}
	}
}

func writeImagesSection(b *strings.Builder, images []imageInfo) {
	if len(images) == 0 {
		return
	}
	b.WriteString("IMAGES\n")
	for _, img := range images {
		switch {
		case img.stem != "" && img.alt != "":
			fmt.Fprintf(b, "  %s (alt: %s)\n", img.stem, img.alt)
		case img.alt != "":
			fmt.Fprintf(b, "  (alt: %s)\n", img.alt)
		default:
			fmt.Fprintf(b, "  %s\n", img.stem)
		}
	}
}

// Package imageplacement validates a merchant's attached image being placed into the theme by use_attachments.
// The model only names the attachment and a path; the platform copies the real bytes on Apply.
package imageplacement

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image/jpeg"
	"path"
	"regexp"
	"strings"
)

// DefaultMaxBytes matches FlowPOS's real ceiling, PHP's upload_max_filesize (2 MB by default); FlowPOS has no image rule of its own.
const DefaultMaxBytes = 2 * 1024 * 1024

// Dir is the only folder a placed image may land in.
const Dir = "images/"

// referencePrefix marks a draft row's content as a pointer to an attachment, never the bytes themselves.
const referencePrefix = "attachment:"

// Placement is one use_attachments entry: the 1-based "Attached image N" number and the theme path to put it at.
type Placement struct {
	Attachment int
	Path       string
}

// Format is an image type FlowPOS accepts for theme upload. Ext is the only extension FlowPOS allows for it.
type Format struct {
	Ext       string
	MediaType string
}

var (
	formatPNG  = Format{Ext: ".png", MediaType: "image/png"}
	formatJPG  = Format{Ext: ".jpg", MediaType: "image/jpeg"}
	formatWEBP = Format{Ext: ".webp", MediaType: "image/webp"}
)

// flowposPathRe mirrors flowpos-backend's ThemeFilePath rule, so a path accepted here is never refused on Apply.
var flowposPathRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// Sniff identifies data by its magic bytes, never a stored media type or a filename. SVG is never recognised: it can carry scripts.
func Sniff(data []byte) (Format, bool) {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return formatPNG, true
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return formatJPG, true
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return formatWEBP, true
	default:
		return Format{}, false
	}
}

// FormatSize renders n bytes as megabytes, e.g. "2 MB" or "3.4 MB".
func FormatSize(n int) string {
	mb := float64(n) / (1024 * 1024)
	if n%(1024*1024) == 0 {
		return fmt.Sprintf("%d MB", n/(1024*1024))
	}
	return fmt.Sprintf("%.1f MB", mb)
}

// IsReferenced reports whether any of texts mentions imagePath, matched as plain text so Liquid asset_url, CSS url()
// and HTML src all count. A mention inside a comment counts too: uploading a stray image is cheaper than a broken one.
func IsReferenced(imagePath string, texts []string) bool {
	for _, t := range texts {
		if strings.Contains(t, imagePath) {
			return true
		}
	}
	return false
}

// HeadBytes is how much of an image Dimensions needs: enough for PNG/WebP headers and a JPEG's frame header after
// typical metadata. A JPEG whose frame header comes later reports no dimensions rather than loading the whole file.
const HeadBytes = 64 * 1024

// Dimensions reads an image's pixel size from its first bytes (see HeadBytes), by format rather than by decoding it.
func Dimensions(head []byte) (width, height int, ok bool) {
	format, known := Sniff(head)
	if !known {
		return 0, 0, false
	}
	switch format {
	case formatPNG:
		if len(head) < 24 {
			return 0, 0, false
		}
		return int(binary.BigEndian.Uint32(head[16:20])), int(binary.BigEndian.Uint32(head[20:24])), true
	case formatJPG:
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(head))
		if err != nil {
			return 0, 0, false
		}
		return cfg.Width, cfg.Height, true
	default:
		return webpDimensions(head)
	}
}

// webpDimensions handles the three WebP layouts: lossy (VP8), lossless (VP8L) and extended (VP8X).
func webpDimensions(head []byte) (int, int, bool) {
	if len(head) < 30 {
		return 0, 0, false
	}
	switch string(head[12:16]) {
	case "VP8 ":
		return int(binary.LittleEndian.Uint16(head[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(head[28:30]) & 0x3fff), true
	case "VP8L":
		b := head[21:25]
		w := 1 + (int(b[0]) | int(b[1]&0x3f)<<8)
		h := 1 + (int(b[1]>>6) | int(b[2])<<2 | int(b[3]&0x0f)<<10)
		return w, h, true
	case "VP8X":
		le24 := func(b []byte) int { return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 }
		return 1 + le24(head[24:27]), 1 + le24(head[27:30]), true
	default:
		return 0, 0, false
	}
}

// Reference is the draft content standing in for an attachment's bytes until Apply uploads them.
func Reference(attachmentID string) string { return referencePrefix + attachmentID }

// ParseReference returns the attachment ID a draft row's content points at.
func ParseReference(content string) (string, bool) {
	id, ok := strings.CutPrefix(content, referencePrefix)
	return id, ok && id != ""
}

// CheckPath applies the rules that need no attachment: under images/, a FlowPOS-safe name, and a raster extension it accepts.
func CheckPath(p string) error {
	if !strings.HasPrefix(p, Dir) || len(p) == len(Dir) {
		return fmt.Errorf("path %q must be a file under %s, e.g. %shero.jpg", p, Dir, Dir)
	}
	if !flowposPathRe.MatchString(p) || strings.Contains(p, "..") || strings.Contains(p, "//") {
		return fmt.Errorf("path %q may only use letters, digits, '.', '_', '-' and '/', with no '..'", p)
	}
	switch strings.ToLower(path.Ext(p)) {
	case formatPNG.Ext, formatJPG.Ext, formatWEBP.Ext:
		return nil
	case ".jpeg":
		return fmt.Errorf("path %q: name a JPEG image .jpg — the theme does not accept .jpeg", p)
	case ".svg":
		return fmt.Errorf("path %q: an SVG can't be placed, since it can contain scripts — only .png, .jpg or .webp", p)
	default:
		return fmt.Errorf("path %q: only .png, .jpg or .webp images can be placed", p)
	}
}

// Check reports why p can't be staged, or nil. found is false when no attachment has p's number; pathTaken when the
// theme or draft already has a file at p.Path, which a placement must never overwrite.
func Check(p Placement, data []byte, found, pathTaken bool, maxBytes int) error {
	if err := CheckPath(p.Path); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("there is no attached image %d in this chat — use a number from the attached images list", p.Attachment)
	}
	if len(data) > maxBytes {
		return fmt.Errorf("attached image %d is %s, over the %s limit for theme images — it can't be placed; tell the "+
			"merchant to attach a smaller version", p.Attachment, FormatSize(len(data)), FormatSize(maxBytes))
	}
	format, ok := Sniff(data)
	if !ok {
		return fmt.Errorf("attached image %d is not a PNG, JPEG or WebP image, so it can't be placed", p.Attachment)
	}
	if strings.ToLower(path.Ext(p.Path)) != format.Ext {
		return fmt.Errorf("attached image %d is really a %s file, so its path must end in %s, not %s",
			p.Attachment, strings.TrimPrefix(format.Ext, "."), format.Ext, path.Ext(p.Path))
	}
	if pathTaken {
		return fmt.Errorf("%s already exists in the theme — pick a new file name under %s; an existing image is never overwritten",
			p.Path, Dir)
	}
	return nil
}
